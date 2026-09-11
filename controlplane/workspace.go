package controlplane

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Git is always invoked with an explicit cwd, bounded runtime and no interactive
// credential prompts. Never change the process cwd or the project's checkout.
func workspaceGit(ctx context.Context, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if len(detail) > 2000 {
			detail = detail[:2000]
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, detail)
	}
	return strings.TrimSpace(string(output)), nil
}

func projectBranches(root string) (ProjectBranches, error) {
	result := ProjectBranches{Branches: []string{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inside, err := workspaceGit(ctx, root, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return result, nil
	}
	result.IsGit = true
	remotes, err := workspaceGit(ctx, root, "remote")
	if err != nil {
		return result, err
	}
	refs, err := workspaceGit(ctx, root, "for-each-ref", "--format=%(refname:strip=2)%09%(symref)", "refs/remotes/")
	if err != nil {
		return result, err
	}
	heads := map[string]string{}
	for _, line := range strings.Split(refs, "\n") {
		name, target, _ := strings.Cut(line, "\t")
		for _, remote := range strings.Fields(remotes) {
			if !strings.HasPrefix(name, remote+"/") {
				continue
			}
			if target != "" {
				if name == remote+"/HEAD" {
					heads[remote] = strings.TrimPrefix(target, "refs/remotes/")
				}
			} else if name != "" {
				result.Branches = append(result.Branches, name)
			}
			break
		}
	}
	sort.Strings(result.Branches)
	result.DefaultBranch = heads["origin"]
	if result.DefaultBranch == "" {
		for _, remote := range strings.Fields(remotes) {
			if heads[remote] != "" {
				result.DefaultBranch = heads[remote]
				break
			}
		}
	}
	found := false
	for _, branch := range result.Branches {
		if branch == result.DefaultBranch {
			found = true
		}
	}
	if !found {
		result.DefaultBranch = ""
	}
	return result, nil
}

func (s *Service) ProjectBranches(id string) (ProjectBranches, error) {
	p, err := s.GetProject(id)
	if err != nil {
		return ProjectBranches{}, err
	}
	result, err := projectBranches(p.Root)
	if err != nil {
		return result, problem(400, "invalid_workspace", err.Error())
	}
	return result, nil
}

func normalizeWorkspace(root string, selection WorkspaceSelection) (WorkspaceSelection, error) {
	if selection.Mode == "" {
		selection.Mode = "current_checkout"
	}
	if selection.Mode != "current_checkout" && selection.Mode != "worktree" {
		return selection, problem(400, "invalid_workspace", "workspace mode must be current_checkout or worktree")
	}
	info, err := projectBranches(root)
	if err != nil {
		return selection, problem(400, "invalid_workspace", err.Error())
	}
	if selection.BaseBranch == "" {
		selection.BaseBranch = info.DefaultBranch
	}
	if selection.Mode == "current_checkout" {
		return selection, nil
	}
	if !info.IsGit {
		return selection, problem(400, "invalid_workspace", "worktrees require a Git working directory")
	}
	for _, branch := range info.Branches {
		if branch == selection.BaseBranch {
			return selection, nil
		}
	}
	return selection, problem(400, "invalid_workspace", "choose an existing remote branch, such as origin/main")
}

func (s *Service) UpdateWorkspace(id string, selection WorkspaceSelection) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Agent{}, err
	}
	a, err := s.recordLocked(id)
	if err != nil {
		return Agent{}, err
	}
	if a.Agent.Workspace.Locked {
		return Agent{}, problem(409, "workspace_locked", "workspace choices are permanently locked after the first message")
	}
	selection, err = normalizeWorkspace(s.state.Projects[a.Agent.ProjectID].Root, selection)
	if err != nil {
		return Agent{}, err
	}
	before := copyJSON(s.state)
	a.Agent.Workspace = Workspace{WorkspaceSelection: selection, Status: "draft"}
	s.eventLocked(a, "agent.updated", s.summaryLocked(a))
	if err := s.commitLocked(before); err != nil {
		return Agent{}, err
	}
	return copyJSON(a.Agent), nil
}

// This is part of the first message's transaction, before any external effects.
func (s *Service) lockWorkspaceLocked(a *storedAgent) {
	w := &a.Agent.Workspace
	if w.Locked {
		return
	}
	w.Locked = true
	if w.Mode == "" {
		w.Mode = "current_checkout"
	}
	if w.Shared {
		// A child shares the recorded directory, not the provisioning lifecycle.
		w.Status = "ready"
	} else if w.Mode == "current_checkout" {
		w.Status = "ready"
		if w.Path == "" {
			w.Path = s.state.Projects[a.Agent.ProjectID].Root
		}
	} else {
		w.Status = "fetching"
		w.Path = filepath.Join(s.dir, "worktrees", a.Agent.ProjectID, a.Agent.ID)
		slug := strings.Trim(strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			return '-'
		}, strings.ToLower(a.Agent.Title)), "-")
		if len(slug) > 40 {
			slug = strings.TrimRight(slug[:40], "-")
		}
		if slug == "" {
			slug = "chat"
		}
		w.Branch = "ted/" + slug + "-" + a.Agent.ID
	}
}

func workspaceFailure(w Workspace) error {
	return problem(409, "workspace_failed", w.Error)
}

func (s *Service) saveWorkspace(id string, update func(*Workspace)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storageErr != nil {
		return s.storageErr
	}
	before := copyJSON(s.state)
	a := s.state.Agents[id]
	update(&a.Agent.Workspace)
	s.eventLocked(a, "agent.updated", s.summaryLocked(a))
	return s.commitLocked(before)
}

func (s *Service) prepareWorkspace(ctx context.Context, id string, project Project) (path string, err error) {
	s.mu.Lock()
	w := s.state.Agents[id].Agent.Workspace
	s.mu.Unlock()
	defer func() {
		if err != nil && w.Status != "failed" {
			message := "Workspace unavailable: " + strings.TrimRight(err.Error(), ".\n ") + ". Create a new chat to try again."
			if saveErr := s.saveWorkspace(id, func(w *Workspace) { w.Status = "failed"; w.Error = message }); saveErr != nil {
				err = saveErr
			} else {
				err = problem(409, "workspace_failed", message)
			}
		}
	}()
	if w.Status == "failed" {
		return "", workspaceFailure(w)
	}
	if w.Mode == "current_checkout" || w.Mode == "" {
		path = w.Path
		if path == "" {
			path = project.Root
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", statErr
		}
		if !info.IsDir() {
			return "", fmt.Errorf("workspace path is not a directory")
		}
		return path, nil
	}
	if w.Status == "ready" {
		// A deleted worktree replaced by an ordinary directory must not be mistaken
		// for the old workspace (nor may git walk up to a containing checkout).
		root, e := workspaceGit(ctx, w.Path, "rev-parse", "--show-toplevel")
		if e != nil {
			return "", e
		}
		canonical, e := filepath.EvalSymlinks(w.Path)
		if e != nil {
			return "", e
		}
		if filepath.Clean(root) != filepath.Clean(canonical) {
			return "", fmt.Errorf("worktree is missing from its recorded path")
		}
		if info, e := os.Stat(filepath.Join(w.Path, ".git")); e != nil || info.IsDir() {
			return "", fmt.Errorf("recorded workspace is no longer a linked Git worktree")
		}
		return w.Path, nil
	}
	if w.Status != "fetching" {
		return "", fmt.Errorf("workspace setup cannot be retried")
	}
	repoRoot, err := workspaceGit(ctx, project.Root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	stateRoot, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(repoRoot, stateRoot)
	if err != nil {
		return "", err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", fmt.Errorf("control-plane data directory must be outside the project checkout to create worktrees")
	}
	remotes, err := workspaceGit(ctx, project.Root, "remote")
	if err != nil {
		return "", err
	}
	remote, branch := "", ""
	for _, name := range strings.Fields(remotes) {
		if strings.HasPrefix(w.BaseBranch, name+"/") && len(name) > len(remote) {
			remote = name
			branch = strings.TrimPrefix(w.BaseBranch, name+"/")
		}
	}
	if remote == "" || strings.HasPrefix(remote, "-") {
		return "", fmt.Errorf("selected remote no longer exists")
	}
	if _, err = workspaceGit(ctx, project.Root, "check-ref-format", "refs/heads/"+branch); err != nil {
		return "", err
	}
	// A thread-private ref prevents concurrent fetches from changing the commit
	// between our fetch and rev-parse. Clear the configured refmap so parallel
	// chats do not contend over remote-tracking refs, and leave FETCH_HEAD alone.
	ref := "refs/ted/workspaces/" + id + "/base"
	if _, err = workspaceGit(ctx, project.Root, "fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", "--", remote, "+refs/heads/"+branch+":"+ref); err != nil {
		return "", err
	}
	commit, err := workspaceGit(ctx, project.Root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if err = s.saveWorkspace(id, func(w *Workspace) { w.Status = "creating"; w.BaseCommit = commit }); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(w.Path), 0700); err != nil {
		return "", err
	}
	if _, err = workspaceGit(ctx, project.Root, "worktree", "add", "-b", w.Branch, "--", w.Path, commit); err != nil {
		return "", err
	}
	if err = s.saveWorkspace(id, func(w *Workspace) { w.Status = "ready" }); err != nil {
		return "", err
	}
	return w.Path, nil
}

package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

// localRemote is a completely local clone/bare-repository fixture. The bare
// remote is deliberately advanced after checkout is cloned, so a test can
// distinguish fetching the remote tip from using the checkout's stale refs.
type localRemote struct {
	remote, seed, checkout    string
	originalCommit, tipCommit string
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Workspace Test", "GIT_AUTHOR_EMAIL=workspace@example.test",
		"GIT_COMMITTER_NAME=Workspace Test", "GIT_COMMITTER_EMAIL=workspace@example.test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newLocalRemote(t *testing.T) localRemote {
	t.Helper()
	base := t.TempDir()
	f := localRemote{
		remote:   filepath.Join(base, "remote.git"),
		seed:     filepath.Join(base, "seed"),
		checkout: filepath.Join(base, "checkout"),
	}
	if err := os.MkdirAll(f.seed, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, base, "init", "--bare", "--initial-branch=main", f.remote)
	runGit(t, f.seed, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(f.seed, "tracked.txt"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.seed, "add", "tracked.txt")
	runGit(t, f.seed, "commit", "-m", "original")
	f.originalCommit = runGit(t, f.seed, "rev-parse", "HEAD")
	runGit(t, f.seed, "remote", "add", "origin", f.remote)
	runGit(t, f.seed, "push", "-u", "origin", "main")
	runGit(t, base, "clone", f.remote, f.checkout)

	// The checkout intentionally never fetches this commit. Workspace setup must
	// fetch it directly from the bare remote into its private base ref.
	if err := os.WriteFile(filepath.Join(f.seed, "tracked.txt"), []byte("remote tip\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.seed, "remote-only.txt"), []byte("fresh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.seed, "add", "tracked.txt", "remote-only.txt")
	runGit(t, f.seed, "commit", "-m", "advance remote")
	f.tipCommit = runGit(t, f.seed, "rev-parse", "HEAD")
	runGit(t, f.seed, "push", "origin", "main")
	return f
}

func newWorkspaceTestService(t *testing.T, root string, defaults WorkspaceSelection) (*Service, *controlledProvider, Project) {
	t.Helper()
	p := &controlledProvider{calls: make(chan agent.CompletionRequest, 50), results: make(chan error, 50)}
	s, err := NewService(t.TempDir(), nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testShutdownTimeout)
		defer cancel()
		_ = s.Close(ctx)
	})
	project, err := s.CreateProject(CreateProjectRequest{
		Name: "worktree project", Root: root, WorkspaceDefaults: &defaults,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, p, project
}

const testShutdownTimeout = 3 * time.Second

func finishWorkspaceTurn(t *testing.T, s *Service, p *controlledProvider, id string) Agent {
	t.Helper()
	p.results <- nil
	return awaitAgent(t, s, id, func(a Agent) bool { return a.State == "idle" })
}

func requireProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var p *Error
	if !errors.As(err, &p) || p.Status != status || p.Code != code {
		t.Fatalf("error = %#v, want status %d code %q", err, status, code)
	}
}

func worktreeCount(t *testing.T, checkout string) int {
	t.Helper()
	out := runGit(t, checkout, "worktree", "list", "--porcelain")
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			count++
		}
	}
	return count
}

func TestWorktreeFetchesFreshRemoteAndLeavesOriginalDirtyCheckoutAlone(t *testing.T) {
	git := newLocalRemote(t)
	if got := runGit(t, git.checkout, "rev-parse", "HEAD"); got != git.originalCommit {
		t.Fatalf("checkout unexpectedly advanced: %s", got)
	}
	if got := runGit(t, git.checkout, "rev-parse", "origin/main"); got != git.originalCommit {
		t.Fatalf("remote-tracking ref unexpectedly advanced: %s", got)
	}
	if err := os.WriteFile(filepath.Join(git.checkout, "tracked.txt"), []byte("dirty checkout\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(git.checkout, "untracked.txt"), []byte("do not copy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	originalStatus := runGit(t, git.checkout, "status", "--porcelain=v1", "--untracked-files=all")

	s, provider, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	branches, err := s.ProjectBranches(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !branches.IsGit || branches.DefaultBranch != "origin/main" || len(branches.Branches) != 1 || branches.Branches[0] != "origin/main" {
		t.Fatalf("branch discovery = %+v", branches)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Title: "Fresh Worktree"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Workspace.Locked || a.Workspace.Status != "draft" || a.Workspace.Path != "" {
		t.Fatalf("new workspace = %+v", a.Workspace)
	}
	if _, err = s.Submit(a.ID, "inspect the fresh checkout", ""); err != nil {
		t.Fatal(err)
	}
	request := awaitCall(t, provider)
	if got := request.Messages[len(request.Messages)-1].Content.Text(); got != "inspect the fresh checkout" {
		t.Fatalf("provider received %q", got)
	}
	ready, err := s.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	w := ready.Workspace
	if !w.Locked || w.Status != "ready" || w.BaseCommit != git.tipCommit || w.Path == "" || w.Branch == "" {
		t.Fatalf("ready workspace = %+v", w)
	}
	if got := runGit(t, w.Path, "rev-parse", "HEAD"); got != git.tipCommit {
		t.Fatalf("worktree HEAD = %s, want remote tip %s", got, git.tipCommit)
	}
	if got := runGit(t, w.Path, "branch", "--show-current"); got != w.Branch {
		t.Fatalf("worktree branch = %q, want %q", got, w.Branch)
	}
	if got := runGit(t, w.Path, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
		t.Fatalf("new worktree is dirty:\n%s", got)
	}
	contents, err := os.ReadFile(filepath.Join(w.Path, "tracked.txt"))
	if err != nil || string(contents) != "remote tip\n" {
		t.Fatalf("worktree tracked file = %q, %v", contents, err)
	}
	if _, err = os.Stat(filepath.Join(w.Path, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("original untracked file was copied: %v", err)
	}

	if got := runGit(t, git.checkout, "rev-parse", "HEAD"); got != git.originalCommit {
		t.Fatalf("original HEAD changed to %s", got)
	}
	if got := runGit(t, git.checkout, "status", "--porcelain=v1", "--untracked-files=all"); got != originalStatus {
		t.Fatalf("original status changed:\nbefore: %q\nafter:  %q", originalStatus, got)
	}
	contents, err = os.ReadFile(filepath.Join(git.checkout, "tracked.txt"))
	if err != nil || string(contents) != "dirty checkout\n" {
		t.Fatalf("original tracked file changed: %q, %v", contents, err)
	}

	s.mu.Lock()
	instance := s.instances[a.ID]
	s.mu.Unlock()
	if instance == nil || instance.WorkingDir() != w.Path || instance.ProjectRoot() != git.checkout || instance.ThreadID() != a.ID {
		t.Fatalf("runtime identity: instance=%v", instance)
	}
	done := finishWorkspaceTurn(t, s, provider, a.ID)
	if done.Held || done.Queue[0].Status != "completed" {
		t.Fatalf("turn did not complete: %+v", done)
	}
}

func TestWorktreeToolUsesWorktreeCWDAndControlPlaneIdentity(t *testing.T) {
	git := newLocalRemote(t)
	processCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	s, err := NewService(stateDir, nil, []agent.Provider{workspaceProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	selection := WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}
	project, err := s.CreateProject(CreateProjectRequest{Name: "p", Root: git.checkout, WorkspaceDefaults: &selection})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Prompt: "report identity"}, "")
	if err != nil {
		t.Fatal(err)
	}
	done := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" })
	if done.Held || done.Queue[0].Status != "completed" || done.Workspace.Status != "ready" {
		t.Fatalf("identity turn failed: %+v", done)
	}
	want := strings.Join([]string{
		done.Workspace.Path,
		a.ID,
		git.checkout,
		filepath.Join(stateDir, "runtime"),
		"",
	}, "\n")
	events, err := s.Events(a.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type != "output" {
			continue
		}
		var output agent.AgentResponse
		if err := json.Unmarshal(event.Data, &output); err != nil {
			t.Fatal(err)
		}
		if output.ResponseType != "tool_result" {
			continue
		}
		found = true
		if output.FullToolOutput != want {
			t.Fatalf("tool cwd/identity = %q, want %q", output.FullToolOutput, want)
		}
	}
	if !found {
		t.Fatal("missing identity tool result")
	}
	if got, err := os.Getwd(); err != nil || got != processCWD {
		t.Fatalf("service changed process cwd from %q to %q (%v)", processCWD, got, err)
	}
}

func TestWorktreeThreadReuseAcrossTurnsRestartAndModelSwitching(t *testing.T) {
	git := newLocalRemote(t)
	provider := &controlledProvider{calls: make(chan agent.CompletionRequest, 20), results: make(chan error, 20)}
	stateDir := t.TempDir()
	s, err := NewService(stateDir, nil, []agent.Provider{provider})
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(CreateProjectRequest{Name: "p", Root: git.checkout, WorkspaceDefaults: &WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Submit(a.ID, "first", ""); err != nil {
		t.Fatal(err)
	}
	firstRequest := awaitCall(t, provider)
	if firstRequest.Model != "test/one" {
		t.Fatalf("first model = %q", firstRequest.Model)
	}
	s.mu.Lock()
	firstInstance := s.instances[a.ID]
	s.mu.Unlock()
	firstDone := finishWorkspaceTurn(t, s, provider, a.ID)
	path, branch, base := firstDone.Workspace.Path, firstDone.Workspace.Branch, firstDone.Workspace.BaseCommit

	model, effort := "test/two", "low"
	if _, err = s.UpdateSettings(a.ID, SettingsPatch{Model: &model, Effort: &effort}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Submit(a.ID, "second", ""); err != nil {
		t.Fatal(err)
	}
	secondRequest := awaitCall(t, provider)
	if secondRequest.Model != model || secondRequest.Effort != agent.EffortLow {
		t.Fatalf("second settings = %q/%q", secondRequest.Model, secondRequest.Effort)
	}
	if len(secondRequest.Messages) < 3 || secondRequest.Messages[len(secondRequest.Messages)-1].Content.Text() != "second" {
		t.Fatalf("second turn did not reuse conversation: %+v", secondRequest.Messages)
	}
	s.mu.Lock()
	secondInstance := s.instances[a.ID]
	s.mu.Unlock()
	if secondInstance == nil || secondInstance != firstInstance || secondInstance.WorkingDir() != path || secondInstance.ThreadID() != a.ID {
		t.Fatal("successive turns did not reuse the worktree-bound runtime")
	}
	finishWorkspaceTurn(t, s, provider, a.ID)
	if got := worktreeCount(t, git.checkout); got != 2 {
		t.Fatalf("worktree count before restart = %d, want checkout + one agent worktree", got)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	restartedProvider := &controlledProvider{calls: make(chan agent.CompletionRequest, 20), results: make(chan error, 20)}
	restarted, err := NewService(stateDir, nil, []agent.Provider{restartedProvider})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	restored, err := restarted.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Workspace.Status != "ready" || restored.Workspace.Path != path || restored.Workspace.Branch != branch || restored.Workspace.BaseCommit != base {
		t.Fatalf("workspace changed across restart: %+v", restored.Workspace)
	}
	model, effort = "test/one", "high"
	if _, err = restarted.UpdateSettings(a.ID, SettingsPatch{Model: &model, Effort: &effort}); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Submit(a.ID, "third", ""); err != nil {
		t.Fatal(err)
	}
	thirdRequest := awaitCall(t, restartedProvider)
	if thirdRequest.Model != model || thirdRequest.Effort != agent.EffortHigh {
		t.Fatalf("restarted settings = %q/%q", thirdRequest.Model, thirdRequest.Effort)
	}
	if len(thirdRequest.Messages) < 5 || thirdRequest.Messages[len(thirdRequest.Messages)-1].Content.Text() != "third" {
		t.Fatalf("conversation not restored: %+v", thirdRequest.Messages)
	}
	restarted.mu.Lock()
	restartedInstance := restarted.instances[a.ID]
	restarted.mu.Unlock()
	if restartedInstance == nil || restartedInstance == firstInstance || restartedInstance.WorkingDir() != path || restartedInstance.ProjectRoot() != git.checkout || restartedInstance.ThreadID() != a.ID {
		t.Fatal("restarted runtime did not restore identity in the recorded worktree")
	}
	finishWorkspaceTurn(t, restarted, restartedProvider, a.ID)
	if got := worktreeCount(t, git.checkout); got != 2 {
		t.Fatalf("restart created another worktree: count %d", got)
	}
}

func TestWorkspaceDefaultsOverridesAndFirstMessagePermanentlyLocks(t *testing.T) {
	git := newLocalRemote(t)
	s, provider, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Workspace.Mode != "worktree" || a.Workspace.BaseBranch != "origin/main" || a.Workspace.Locked {
		t.Fatalf("project defaults not copied: %+v", a.Workspace)
	}
	current, err := s.UpdateWorkspace(a.ID, WorkspaceSelection{Mode: "current_checkout"})
	if err != nil || current.Workspace.Mode != "current_checkout" || current.Workspace.Locked || current.Workspace.Status != "draft" {
		t.Fatalf("draft update = %+v, %v", current.Workspace, err)
	}
	if _, err = s.Submit(a.ID, "  \n", ""); err == nil {
		t.Fatal("empty submit unexpectedly succeeded")
	} else {
		requireProblem(t, err, 400, "invalid_message")
	}
	stillDraft, _ := s.GetAgent(a.ID)
	if stillDraft.Workspace.Locked {
		t.Fatal("rejected empty message locked the workspace")
	}
	updated, err := s.UpdateWorkspace(a.ID, WorkspaceSelection{Mode: "worktree"})
	if err != nil || updated.Workspace.BaseBranch != "origin/main" {
		t.Fatalf("default branch normalization = %+v, %v", updated.Workspace, err)
	}
	if _, err = s.Submit(a.ID, "first real message", ""); err != nil {
		t.Fatal(err)
	}
	locked, _ := s.GetAgent(a.ID)
	if !locked.Workspace.Locked || locked.Workspace.Path == "" || locked.Workspace.Branch == "" {
		t.Fatalf("first submit did not synchronously lock location: %+v", locked.Workspace)
	}
	_, err = s.UpdateWorkspace(a.ID, WorkspaceSelection{Mode: "current_checkout"})
	requireProblem(t, err, 409, "workspace_locked")
	awaitCall(t, provider)
	finishWorkspaceTurn(t, s, provider, a.ID)
	_, err = s.UpdateWorkspace(a.ID, WorkspaceSelection{Mode: "current_checkout"})
	requireProblem(t, err, 409, "workspace_locked")

	// A per-agent override wins over project defaults, and a nonempty create
	// prompt is itself the first message transaction.
	override := WorkspaceSelection{Mode: "current_checkout"}
	prompted, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Title: "override", Prompt: "start", Workspace: &override}, "")
	if err != nil {
		t.Fatal(err)
	}
	if prompted.Workspace.Mode != "current_checkout" || !prompted.Workspace.Locked || prompted.Workspace.Status != "ready" || prompted.Workspace.Path != git.checkout {
		t.Fatalf("prompt override = %+v", prompted.Workspace)
	}
	awaitCall(t, provider)
	finishWorkspaceTurn(t, s, provider, prompted.ID)
	_, err = s.UpdateWorkspace(prompted.ID, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	requireProblem(t, err, 409, "workspace_locked")
}

func TestWorkspaceSetupFailureIsPermanentAndCannotBeRetried(t *testing.T) {
	git := newLocalRemote(t)
	s, provider, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	missingRemote := git.remote + ".offline"
	if err = os.Rename(git.remote, missingRemote); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Submit(a.ID, "will fail setup", ""); err != nil {
		t.Fatal(err)
	}
	failed := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" && a.Workspace.Status == "failed" })
	if !failed.Workspace.Locked || !failed.Held || failed.Queue[0].Status != "failed" || !strings.Contains(failed.Workspace.Error, "Create a new chat") {
		t.Fatalf("failed workspace = %+v, queue=%+v", failed.Workspace, failed.Queue)
	}
	noCall(t, provider)
	if err = os.Rename(missingRemote, git.remote); err != nil {
		t.Fatal(err)
	}
	_, err = s.Submit(a.ID, "do not retry", "")
	requireProblem(t, err, 409, "workspace_failed")
	_, err = s.Continue(a.ID)
	requireProblem(t, err, 409, "workspace_failed")
	_, err = s.UpdateWorkspace(a.ID, WorkspaceSelection{Mode: "current_checkout"})
	requireProblem(t, err, 409, "workspace_locked")
	noCall(t, provider)
	if _, err = os.Stat(failed.Workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("failed setup left a usable path: %v", err)
	}
	if got := worktreeCount(t, git.checkout); got != 1 {
		t.Fatalf("failed setup created %d worktrees", got-1)
	}
}

func TestDeletedWorktreeFailsWithoutFallingBackToContainingRepository(t *testing.T) {
	git := newLocalRemote(t)
	if err := os.WriteFile(filepath.Join(git.checkout, "untouched.txt"), []byte("original only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	originalStatus := runGit(t, git.checkout, "status", "--porcelain=v1", "--untracked-files=all")
	s, provider, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Prompt: "first"}, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitCall(t, provider)
	a = finishWorkspaceTurn(t, s, provider, a.ID)
	path := a.Workspace.Path
	runGit(t, git.checkout, "worktree", "remove", "--force", path)

	// Recreate the recorded path as an ordinary directory below another Git
	// checkout. rev-parse can walk up to that checkout, but it must never be used
	// in place of the deleted linked worktree.
	parent := filepath.Dir(path)
	runGit(t, parent, "init", "--initial-branch=fallback")
	if err = os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "sentinel"), []byte("ordinary directory\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Submit(a.ID, "must not run elsewhere", ""); err != nil {
		t.Fatal(err)
	}
	failed := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" && a.Workspace.Status == "failed" })
	if failed.Queue[1].Status != "failed" || !strings.Contains(failed.Workspace.Error, "recorded path") {
		t.Fatalf("deleted worktree result: workspace=%+v queue=%+v", failed.Workspace, failed.Queue)
	}
	noCall(t, provider)
	if got := runGit(t, git.checkout, "status", "--porcelain=v1", "--untracked-files=all"); got != originalStatus {
		t.Fatalf("fallback touched original checkout: before %q after %q", originalStatus, got)
	}
	contents, err := os.ReadFile(filepath.Join(path, "sentinel"))
	if err != nil || string(contents) != "ordinary directory\n" {
		t.Fatalf("replacement directory was touched: %q, %v", contents, err)
	}
}

func TestConcurrentIdempotencyCreatesOnlyOneAgentMessageAndWorktree(t *testing.T) {
	for _, tc := range []struct {
		name             string
		createWithPrompt bool
	}{
		{name: "CreateAgent", createWithPrompt: true},
		{name: "Submit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git := newLocalRemote(t)
			s, provider, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
			const callers = 24
			ids := make(chan string, callers)
			errs := make(chan error, callers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			if tc.createWithPrompt {
				req := CreateAgentRequest{ProjectID: project.ID, Title: "same", Prompt: "one", Workspace: &WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}}
				for i := 0; i < callers; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						a, err := s.CreateAgent(req, "same-create-key")
						errs <- err
						ids <- a.ID
					}()
				}
			} else {
				a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < callers; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						m, err := s.Submit(a.ID, "one", "same-submit-key")
						errs <- err
						ids <- m.ID
					}()
				}
			}
			close(start)
			wg.Wait()
			close(ids)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			oneID := ""
			for id := range ids {
				if id == "" {
					t.Fatal("idempotent call returned an empty id")
				}
				if oneID == "" {
					oneID = id
				} else if id != oneID {
					t.Fatalf("idempotent calls returned %q and %q", oneID, id)
				}
			}
			awaitCall(t, provider)
			agents := s.Agents(true, project.ID)
			if len(agents) != 1 || len(agents[0].Queue) != 1 {
				t.Fatalf("durable duplicates: %+v", agents)
			}
			if got := worktreeCount(t, git.checkout); got != 2 {
				t.Fatalf("concurrent retry made %d linked worktrees, want 1", got-1)
			}
			finishWorkspaceTurn(t, s, provider, agents[0].ID)
			noCall(t, provider)
		})
	}
}

func TestWorkspaceValidationForNonGitLocalAndRemoteBranches(t *testing.T) {
	t.Run("non-git", func(t *testing.T) {
		root := t.TempDir()
		s, _, project := newWorkspaceTestService(t, root, WorkspaceSelection{Mode: "current_checkout"})
		branches, err := s.ProjectBranches(project.ID)
		if err != nil || branches.IsGit || branches.DefaultBranch != "" || len(branches.Branches) != 0 {
			t.Fatalf("non-git branches = %+v, %v", branches, err)
		}
		selection := WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}
		_, err = s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Workspace: &selection}, "")
		requireProblem(t, err, 400, "invalid_workspace")
		a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.UpdateWorkspace(a.ID, selection)
		requireProblem(t, err, 400, "invalid_workspace")
		_, err = s.UpdateProject(project.ID, nil, nil, &selection)
		requireProblem(t, err, 400, "invalid_workspace")
	})

	t.Run("local-branch-is-not-a-worktree-base", func(t *testing.T) {
		root := t.TempDir()
		runGit(t, root, "init", "--initial-branch=main")
		if err := os.WriteFile(filepath.Join(root, "file"), []byte("local\n"), 0600); err != nil {
			t.Fatal(err)
		}
		runGit(t, root, "add", "file")
		runGit(t, root, "commit", "-m", "local")
		s, _, project := newWorkspaceTestService(t, root, WorkspaceSelection{Mode: "current_checkout"})
		branches, err := s.ProjectBranches(project.ID)
		if err != nil || !branches.IsGit || len(branches.Branches) != 0 || branches.DefaultBranch != "" {
			t.Fatalf("local-only branches = %+v, %v", branches, err)
		}
		for _, base := range []string{"", "main", "origin/main"} {
			selection := WorkspaceSelection{Mode: "worktree", BaseBranch: base}
			_, err = s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Workspace: &selection}, "")
			requireProblem(t, err, 400, "invalid_workspace")
		}
	})

	t.Run("only-existing-remote-qualified-branch", func(t *testing.T) {
		git := newLocalRemote(t)
		s, _, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "current_checkout"})
		branches, err := s.ProjectBranches(project.ID)
		if err != nil || !branches.IsGit || branches.DefaultBranch != "origin/main" || len(branches.Branches) != 1 || branches.Branches[0] != "origin/main" {
			t.Fatalf("remote branches = %+v, %v", branches, err)
		}
		for _, base := range []string{"main", "origin/missing", "origin/-invalid"} {
			selection := WorkspaceSelection{Mode: "worktree", BaseBranch: base}
			_, err = s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Workspace: &selection}, "")
			requireProblem(t, err, 400, "invalid_workspace")
		}
		valid := WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}
		if _, err = s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Workspace: &valid}, ""); err != nil {
			t.Fatalf("valid remote branch rejected: %v", err)
		}
		invalidMode := WorkspaceSelection{Mode: "directory", BaseBranch: "origin/main"}
		if _, err = s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Workspace: &invalidMode}, ""); err == nil {
			t.Fatal("invalid workspace mode accepted")
		} else {
			requireProblem(t, err, 400, "invalid_workspace")
		}
	})
}

func TestInterruptedWorkspaceSetupBecomesFailedOnRestart(t *testing.T) {
	for _, setupStatus := range []string{"fetching", "creating"} {
		t.Run(setupStatus, func(t *testing.T) {
			provider := &controlledProvider{calls: make(chan agent.CompletionRequest, 5), results: make(chan error, 5)}
			stateDir := t.TempDir()
			s, err := NewService(stateDir, nil, []agent.Provider{provider})
			if err != nil {
				t.Fatal(err)
			}
			project, err := s.CreateProject(CreateProjectRequest{Name: "p", Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}

			// Model the exact durable boundary left by a process interruption. A
			// restart must never interpret either setup phase as permission to retry.
			state, err := loadState(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			record := state.Agents[a.ID]
			record.Agent.Workspace = Workspace{
				WorkspaceSelection: WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"},
				Locked:             true, Status: setupStatus,
				Path:   filepath.Join(stateDir, "worktrees", project.ID, a.ID),
				Branch: "ted/interrupted-" + a.ID,
			}
			if setupStatus == "creating" {
				record.Agent.Workspace.BaseCommit = strings.Repeat("a", 40)
			}
			record.Agent.Queue = []QueuedMessage{{ID: "pending", Text: "never retry", Status: "pending"}}
			if err = saveState(stateDir, state); err != nil {
				t.Fatal(err)
			}

			restarted, err := NewService(stateDir, nil, []agent.Provider{provider})
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close(context.Background())
			failed, err := restarted.GetAgent(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failed.Workspace.Status != "failed" || !failed.Workspace.Locked || !failed.Held || failed.State != "idle" || !strings.Contains(failed.Workspace.Error, "Server stopped during workspace setup") {
				t.Fatalf("restart did not fail interrupted %s setup: %+v", setupStatus, failed)
			}
			if failed.Queue[0].Status != "pending" {
				t.Fatalf("restart unexpectedly ran pending message: %+v", failed.Queue)
			}
			noCall(t, provider)
			_, err = restarted.Submit(a.ID, "new", "")
			requireProblem(t, err, 409, "workspace_failed")
			_, err = restarted.Continue(a.ID)
			requireProblem(t, err, 409, "workspace_failed")
			_, err = restarted.UpdateWorkspace(a.ID, WorkspaceSelection{Mode: "current_checkout"})
			requireProblem(t, err, 409, "workspace_locked")
			noCall(t, provider)
		})
	}
}

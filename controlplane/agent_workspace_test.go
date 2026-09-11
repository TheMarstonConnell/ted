package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestChildSharedWorktreePersistsAndRunsInParentsCurrentBranch(t *testing.T) {
	git := newLocalRemote(t)
	dir := t.TempDir()
	s, err := NewService(dir, nil, []agent.Provider{workspaceProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	defaults := WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}
	project, err := s.CreateProject(CreateProjectRequest{Name: "repo", Root: git.checkout, WorkspaceDefaults: &defaults})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Prompt: "identity"}, "")
	if err != nil {
		t.Fatal(err)
	}
	parent = awaitAgent(t, s, parent.ID, func(a Agent) bool { return a.State == "idle" })
	if parent.Workspace.Status != "ready" {
		t.Fatal(parent.Workspace)
	}
	path := parent.Workspace.Path
	runGit(t, path, "switch", "-c", "parent-current-branch")
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("parent dirty edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "untracked-child.txt"), []byte("shared\n"), 0600); err != nil {
		t.Fatal(err)
	}
	count := worktreeCount(t, git.checkout)
	// Sharing must still work with the remote offline: no implicit fetch.
	if err := os.Rename(git.remote, git.remote+"-offline"); err != nil {
		t.Fatal(err)
	}
	childReq := CreateAgentRequest{ProjectID: project.ID, ParentAgentID: parent.ID}
	child, err := s.CreateAgent(childReq, "child-key")
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentAgentID != parent.ID || child.ProjectID != project.ID || !child.Workspace.Shared || child.Workspace.Path != path || child.Workspace.Locked || child.Workspace.Status != "draft" {
		t.Fatalf("unexpected child: %+v", child)
	}
	again, err := s.CreateAgent(childReq, "child-key")
	if err != nil || again.ID != child.ID {
		t.Fatalf("idempotency: %+v %v", again, err)
	}
	// A grandchild can inherit a shared draft's already-established worktree.
	grandchild, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, ParentAgentID: child.ID}, "")
	if err != nil || grandchild.Workspace.Path != path || !grandchild.Workspace.Shared {
		t.Fatalf("grandchild: %+v %v", grandchild, err)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err = NewService(dir, nil, []agent.Provider{workspaceProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := s.GetAgent(child.ID)
	if err != nil || restored.ParentAgentID != parent.ID || restored.Workspace != child.Workspace {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	if _, err = s.Submit(child.ID, "identity", ""); err != nil {
		t.Fatal(err)
	}
	done := awaitAgent(t, s, child.ID, func(a Agent) bool { return a.State == "idle" })
	if done.Held || !done.Workspace.Locked || done.Workspace.Status != "ready" || done.Workspace.Path != path {
		t.Fatalf("child failed: %+v", done)
	}
	s.mu.Lock()
	runtime := s.instances[child.ID]
	s.mu.Unlock()
	if runtime == nil || runtime.WorkingDir() != path || runtime.ProjectRoot() != git.checkout || runtime.ThreadID() != child.ID {
		t.Fatal("wrong child runtime identity")
	}
	want := strings.Join([]string{path, child.ID, git.checkout, filepath.Join(dir, "runtime"), ""}, "\n")
	events, err := s.Events(child.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == "output" {
			var output agent.AgentResponse
			if err = json.Unmarshal(e.Data, &output); err != nil {
				t.Fatal(err)
			}
			if output.ResponseType == "tool_result" {
				found = true
				if output.FullToolOutput != want {
					t.Fatalf("tool output %q, want %q", output.FullToolOutput, want)
				}
			}
		}
	}
	if !found {
		t.Fatal("no bash output")
	}
	if worktreeCount(t, git.checkout) != count {
		t.Fatal("sharing created another worktree")
	}
	if got := runGit(t, path, "branch", "--show-current"); got != "parent-current-branch" {
		t.Fatal(got)
	}
	data, _ := os.ReadFile(filepath.Join(path, "tracked.txt"))
	if string(data) != "parent dirty edit\n" {
		t.Fatalf("edit lost: %q", data)
	}
	if _, err = os.Stat(filepath.Join(path, "untracked-child.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateWorkspace(child.ID, WorkspaceSelection{Mode: "current_checkout"}); err == nil {
		t.Fatal("shared workspace not locked")
	}
	// Settling the parent is independent of every child.
	if _, err = s.SetSettled(parent.ID, true); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetAgent(child.ID)
	if current.Settled {
		t.Fatal("parent cascaded settling")
	}
	// Missing shared worktrees fail without recreation/fallback to checkout.
	if err = os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Submit(child.ID, "again", ""); err != nil {
		t.Fatal(err)
	}
	failed := awaitAgent(t, s, child.ID, func(a Agent) bool { return a.State == "idle" })
	if failed.Workspace.Status != "failed" || failed.Workspace.Path != path {
		t.Fatal(failed.Workspace)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("shared path recreated: %v", err)
	}
}

func TestChildWorkspaceCreationPrecedence(t *testing.T) {
	git := newLocalRemote(t)
	s, provider, p := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	parent, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, Prompt: "start"}, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitCall(t, provider)
	parent = finishWorkspaceTurn(t, s, provider, parent.ID)
	checkout := WorkspaceSelection{Mode: "current_checkout"}
	fresh := WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}
	for _, tt := range []struct {
		name, cwd string
		workspace *WorkspaceSelection
		shared    bool
		mode      string
	}{
		{name: "inherit", shared: true, mode: "worktree"},
		{name: "explicit checkout", workspace: &checkout, mode: "current_checkout"},
		{name: "explicit fresh", workspace: &fresh, mode: "worktree"},
		{name: "cwd parent tree", cwd: parent.Workspace.Path, shared: true, mode: "worktree"},
		{name: "cwd checkout follows project defaults", cwd: p.Root, mode: "worktree"},
		{name: "cwd checkout explicit local", cwd: p.Root, workspace: &checkout, mode: "current_checkout"},
		{name: "cwd parent tree local", cwd: parent.Workspace.Path, workspace: &checkout, shared: true, mode: "worktree"},
		{name: "cwd parent tree fresh", cwd: parent.Workspace.Path, workspace: &fresh, mode: "worktree"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, ParentAgentID: parent.ID, WorkingDirectory: tt.cwd, Workspace: tt.workspace}, "")
			if err != nil {
				t.Fatal(err)
			}
			if a.ParentAgentID != parent.ID || a.Workspace.Shared != tt.shared || a.Workspace.Mode != tt.mode {
				t.Fatalf("workspace %+v", a)
			}
			if tt.shared && a.Workspace.Path != parent.Workspace.Path {
				t.Fatal(a.Workspace)
			}
		})
	}
	// Both canonical and symlinked paths retain the same managed-worktree owner.
	alias := filepath.Join(t.TempDir(), "tree-link")
	if err := os.Symlink(parent.Workspace.Path, alias); err != nil {
		t.Fatal(err)
	}
	aAlias, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, WorkingDirectory: alias}, "")
	if err != nil || !aAlias.Workspace.Shared {
		t.Fatalf("alias %+v %v", aAlias, err)
	}
	s.mu.Lock()
	s.state.Agents[parent.ID].Agent.Workspace.Path = alias
	s.mu.Unlock()
	aCanonical, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, WorkingDirectory: parent.Workspace.Path}, "")
	if err != nil || !aCanonical.Workspace.Shared {
		t.Fatalf("canonical %+v %v", aCanonical, err)
	}
	// Project defaults apply if the parent has no established worktree.
	localParent, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, Workspace: &checkout}, "")
	if err != nil {
		t.Fatal(err)
	}
	localChild, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, ParentAgentID: localParent.ID}, "")
	if err != nil || localChild.Workspace.Shared || localChild.Workspace.Mode != "worktree" {
		t.Fatalf("defaults %+v %v", localChild, err)
	}
	// Explicit cwd works without parentage, including settled worktree owners.
	if _, err = s.SetSettled(parent.ID, true); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, WorkingDirectory: parent.Workspace.Path}, "")
	if err != nil || !a.Workspace.Shared {
		t.Fatalf("cwd %+v %v", a, err)
	}
	// A draft workspace override intentionally replaces its inherited location.
	a, err = s.UpdateWorkspace(a.ID, checkout)
	if err != nil || a.Workspace.Shared || a.Workspace.Path != "" {
		t.Fatalf("draft override %+v %v", a, err)
	}
}

func TestChildCreationValidationAndHTTPMetadata(t *testing.T) {
	f := newHTTPFixture(t, nil)
	p := f.project()
	parent := decodeHTTP[Agent](t, f.request("POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q}`, p.ID), "", 201))
	body := fmt.Sprintf(`{"project_id":%q,"parent_agent_id":%q,"working_directory":%q}`, p.ID, parent.ID, p.Root)
	child := decodeHTTP[Agent](t, f.request("POST", "/v1/agents", body, "new-child", 201))
	if child.ParentAgentID != parent.ID || child.Workspace.Path != p.Root {
		t.Fatalf("child %+v", child)
	}
	got := decodeHTTP[Agent](t, f.request("GET", "/v1/agents/"+child.ID, "", "", 200))
	if got.ParentAgentID != parent.ID {
		t.Fatal(got)
	}
	inventory, err := summaryWS(child)
	if err != nil || inventory.ParentAgentId == nil || *inventory.ParentAgentId != parent.ID {
		t.Fatalf("inventory %+v %v", inventory, err)
	}
	events, _ := f.s.Events(child.ID, 0, 100)
	var update map[string]any
	if err = json.Unmarshal(events[0].Data, &update); err != nil {
		t.Fatal(err)
	}
	if update["parent_agent_id"] != parent.ID {
		t.Fatal(update)
	}
	// The relationship cannot be claimed or changed through PATCH.
	f.request("PATCH", "/v1/agents/"+child.ID, fmt.Sprintf(`{"settled":false,"parent_agent_id":%q}`, parent.ID), "", 400)
	f.request("POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q,"parent_agent_id":"missing"}`, p.ID), "", 404)
	f.request("POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q,"parent_agent_id":""}`, p.ID), "", 400)
	f.request("POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q,"working_directory":""}`, p.ID), "", 400)
	f.request("POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q,"working_directory":"relative"}`, p.ID), "", 400)
	f.request("POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q,"working_directory":%q}`, p.ID, t.TempDir()), "", 400)
	// Idempotency includes parentage and cwd, not merely the project ID.
	f.request("POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q}`, p.ID), "new-child", 409)
	if len(f.s.Agents(true, "")) != 2 {
		t.Fatal("invalid requests created agents")
	}
}

func TestChildUnavailableParentRequiresExplicitWorkspace(t *testing.T) {
	s, _, p := newWorkspaceTestService(t, t.TempDir(), WorkspaceSelection{Mode: "current_checkout"})
	parent, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"fetching", "creating", "failed"} {
		t.Run(status, func(t *testing.T) {
			s.mu.Lock()
			s.state.Agents[parent.ID].Agent.Workspace = Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "worktree"}, Locked: true, Status: status}
			s.mu.Unlock()
			_, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, ParentAgentID: parent.ID}, "")
			requireProblem(t, err, 409, "workspace_unavailable")
			child, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, ParentAgentID: parent.ID, Workspace: &WorkspaceSelection{Mode: "current_checkout"}}, "")
			if err != nil || child.Workspace.Shared || child.Workspace.Mode != "current_checkout" {
				t.Fatalf("override %+v %v", child, err)
			}
		})
	}
}

package controlplane

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaleWorkspaceDefaultStillAllowsDraftOverride(t *testing.T) {
	git := newLocalRemote(t)
	s, provider, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	// A previously valid default can vanish from the local remote inventory.
	runGit(t, git.checkout, "update-ref", "-d", "refs/remotes/origin/main")
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Workspace.Locked || a.Workspace.BaseBranch != "origin/main" {
		t.Fatalf("defaults not snapshotted: %+v", a.Workspace)
	}
	if _, err := s.UpdateWorkspace(a.ID, WorkspaceSelection{Mode: "current_checkout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(a.ID, "use the original checkout", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, provider)
	finishWorkspaceTurn(t, s, provider, a.ID)
}

func TestWorktreeDataDirectoryInsideCheckoutFailsWithoutAgentExecution(t *testing.T) {
	git := newLocalRemote(t)
	s, provider, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	// Exercise the symlink-aware guard with a data-dir alias pointing inside Git.
	inside := filepath.Join(git.checkout, "app-state")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(inside, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s.mu.Lock()
	originalDir := s.dir
	s.dir = alias
	s.mu.Unlock()
	t.Cleanup(func() { s.mu.Lock(); s.dir = originalDir; s.mu.Unlock() })
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Prompt: "must not run"}, "")
	if err != nil {
		t.Fatal(err)
	}
	failed := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.Workspace.Status == "failed" && a.State == "idle" })
	if !failed.Workspace.Locked {
		t.Fatal("failure unlocked workspace")
	}
	noCall(t, provider)
	if got := worktreeCount(t, git.checkout); got != 1 {
		t.Fatalf("created worktree inside original checkout: %d", got)
	}
}

func TestDistinctChatsProvisionConcurrentlyWithoutSharedFetchRefs(t *testing.T) {
	git := newLocalRemote(t)
	s, provider, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	const count = 6
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Prompt: "isolate this chat"}, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	for range ids {
		awaitCall(t, provider)
	}
	paths := map[string]bool{}
	for _, id := range ids {
		a, err := s.GetAgent(id)
		if err != nil {
			t.Fatal(err)
		}
		w := a.Workspace
		if w.Status != "ready" || w.BaseCommit != git.tipCommit || paths[w.Path] {
			t.Fatalf("incorrect concurrent workspace: %+v", w)
		}
		paths[w.Path] = true
	}
	// Fetch only the thread-private ref: don't race over (or alter) origin/main.
	if got := runGit(t, git.checkout, "rev-parse", "origin/main"); got != git.originalCommit {
		t.Fatalf("fetch changed shared tracking ref: %s", got)
	}
	if got := worktreeCount(t, git.checkout); got != count+1 {
		t.Fatalf("got %d worktrees", got)
	}
	for range ids {
		provider.results <- nil
	}
	for _, id := range ids {
		awaitAgent(t, s, id, func(a Agent) bool { return a.State == "idle" })
	}
}

func TestModeOnlyCreationOverrideKeepsProjectBaseBranch(t *testing.T) {
	git := newLocalRemote(t)
	runGit(t, git.checkout, "update-ref", "refs/remotes/origin/release", git.originalCommit)
	s, _, project := newWorkspaceTestService(t, git.checkout, WorkspaceSelection{Mode: "current_checkout", BaseBranch: "origin/release"})
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Workspace: &WorkspaceSelection{Mode: "worktree"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Workspace.BaseBranch != "origin/release" {
		t.Fatalf("mode override lost project base: %+v", a.Workspace)
	}
}

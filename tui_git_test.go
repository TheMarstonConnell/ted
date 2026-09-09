package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestCurrentGitBranch(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := currentGitBranch(dir); got != "" {
		t.Fatal(got)
	}
	git("init", "-b", "main")
	if got := currentGitBranch(dir); got != "main" {
		t.Fatal(got)
	}
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	git("checkout", "-b", "feature/status")
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if got := currentGitBranch(sub); got != "feature/status" {
		t.Fatal(got)
	}
	worktree := filepath.Join(t.TempDir(), "worktree")
	git("worktree", "add", "-b", "worktree-branch", worktree)
	if got := currentGitBranch(worktree); got != "worktree-branch" {
		t.Fatal(got)
	}
	git("checkout", "--detach")
	if got, want := currentGitBranch(dir), "detached "+git("rev-parse", "--short", "HEAD"); got != want {
		t.Fatalf("%q != %q", got, want)
	}
}

func TestGitBranchStatus(t *testing.T) {
	m := tuiTestModel()
	m.directory = "/repo"
	next, cmd := m.Update(gitBranchMsg("main"))
	m = next.(model)
	if cmd == nil {
		t.Fatal("refresh not scheduled")
	}
	if got := ansi.Strip(m.statusView()); !strings.Contains(got, "/repo (main)") {
		t.Fatal(got)
	}
	m.gitBranch = strings.Repeat("long-branch", 100)
	if lipgloss.Width(m.statusView()) > lipgloss.Width(m.inputView()) {
		t.Fatal("status overflows")
	}
	if !strings.Contains(ansi.Strip(m.statusView()), "ctx 100% left") {
		t.Fatal("missing context meter")
	}
	next, _ = m.Update(gitBranchMsg(""))
	m = next.(model)
	if got := ansi.Strip(m.statusView()); strings.Contains(got, "/repo (") {
		t.Fatal(got)
	}
}

type branchTestAgent struct {
	tuiAgent
	branch    string
	directory string
	err       error
}

func (a *branchTestAgent) GitBranch() (string, error) { return a.branch, a.err }
func (a *branchTestAgent) WorkingDir() string {
	if a.directory != "" {
		return a.directory
	}
	return a.tuiAgent.WorkingDir()
}

func TestServerBranchPolling(t *testing.T) {
	m := tuiTestModel()
	source := &branchTestAgent{tuiAgent: m.agent, branch: "server-branch"}
	m.agent = source
	m.directory = "/not/on/this/client"
	next, cmd := m.Update(m.readBranch()())
	m = next.(model)
	if m.gitBranch != "server-branch" || cmd == nil {
		t.Fatal("missing branch or refresh")
	}
	source.branch = "changed"
	_, poll := m.Update(gitBranchRefreshMsg{})
	next, _ = m.Update(poll())
	m = next.(model)
	if m.gitBranch != "changed" {
		t.Fatal(m.gitBranch)
	}
	source.err = fmt.Errorf("offline")
	next, cmd = m.Update(m.readBranch()())
	m = next.(model)
	if m.gitBranch != "changed" || cmd == nil {
		t.Fatal("transient failure lost branch or stopped polling")
	}
	source.err = nil
	source.branch = ""
	next, cmd = m.Update(m.readBranch()())
	if next.(model).gitBranch != "" || cmd == nil {
		t.Fatal("non-repository should clear branch and keep polling")
	}
}

func TestRemoteWorkspaceDirectoryRefreshesFooter(t *testing.T) {
	m := tuiTestModel()
	source := &branchTestAgent{
		tuiAgent:  m.agent,
		branch:    "ted/chat-a",
		directory: "/server/worktrees/chat-a",
	}
	m.agent = source
	m.directory = "/server/project"

	// The branch lookup is an asynchronous Tea command. Remote GitBranch may
	// refresh its snapshot, so the result also carries the subsequent cwd.
	next, cmd := m.Update(m.readBranch()())
	m = next.(model)
	if cmd == nil || m.directory != source.directory || m.gitBranch != source.branch {
		t.Fatalf("directory=%q branch=%q cmd=%v", m.directory, m.gitBranch, cmd)
	}
	if got := ansi.Strip(m.statusView()); !strings.Contains(got, "/server/worktrees/chat-a (ted/ch") {
		t.Fatal(got)
	}

	// Websocket agent.updated inventory/state notifications update the path
	// immediately from the already-local snapshot, with no rendering-time I/O.
	source.directory = "/server/worktrees/chat-a-ready"
	next, _ = m.Update(remoteStateMsg(false))
	m = next.(model)
	if m.directory != source.directory {
		t.Fatalf("state refresh directory = %q", m.directory)
	}
}

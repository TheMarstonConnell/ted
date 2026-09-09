package main

import (
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

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type gitBranchMsg string
type gitBranchRefreshMsg struct{}

func scheduleGitBranchRefresh() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return gitBranchRefreshMsg{} })
}

// Git runs off the UI loop, with a deadline and no overlapping polls. Using
// git rather than reading .git also supports subdirectories and worktrees.
func readGitBranch(directory string) tea.Cmd {
	return func() tea.Msg { return gitBranchMsg(currentGitBranch(directory)) }
}

func currentGitBranch(directory string) string {
	if directory == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = directory
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if branch := run("symbolic-ref", "--quiet", "--short", "HEAD"); branch != "" {
		return branch
	}
	if commit := run("rev-parse", "--short", "HEAD"); commit != "" {
		return "detached " + commit
	}
	return ""
}

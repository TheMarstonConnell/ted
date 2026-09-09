package main

import (
	"time"

	"github.com/TheMarstonConnell/ted/internal/gitstatus"

	tea "charm.land/bubbletea/v2"
)

type gitBranchMsg string
type gitBranchRefreshMsg struct{}

func scheduleGitBranchRefresh() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return gitBranchRefreshMsg{} })
}

// Branch lookup runs off the UI loop, without overlapping polls.
func readGitBranch(directory string) tea.Cmd {
	return func() tea.Msg { return gitBranchMsg(currentGitBranch(directory)) }
}
func currentGitBranch(directory string) string { return gitstatus.Branch(directory) }

func (m model) readBranch() tea.Cmd {
	if source, ok := m.agent.(interface{ GitBranch() (string, error) }); ok {
		return func() tea.Msg {
			branch, err := source.GitBranch()
			if err != nil {
				return gitBranchErrorMsg{}
			}
			return gitBranchMsg(branch)
		}
	}
	return readGitBranch(m.directory)
}

type gitBranchErrorMsg struct{}

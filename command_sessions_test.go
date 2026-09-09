package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestSessionsCommand(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	run := func() string {
		t.Helper()
		cmd := newRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"sessions"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := run(); !strings.Contains(got, "No saved sessions") {
		t.Fatal(got)
	}
	a := agent.NewAgent(nil, []agent.Provider{agent.NewCodexProvider(nil)})
	if err := a.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	if got := run(); !strings.Contains(got, a.ThreadID()) || !strings.Contains(got, "New conversation") {
		t.Fatal(got)
	}
}

func TestResumeFlagsOnTUI(t *testing.T) {
	root := newRootCommand()
	root.SetArgs([]string{"tui", "--continue", "--resume", "id"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "were all set") {
		t.Fatal(err)
	}
}

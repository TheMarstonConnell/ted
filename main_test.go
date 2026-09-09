package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootShowsHelp(t *testing.T) {
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Usage:", "Available Commands:", "tui"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q: %s", want, out.String())
		}
	}
}

func TestTUIFlagsOnlyOnTUI(t *testing.T) {
	root := newRootCommand()
	tui, _, err := root.Find([]string{"tui"})
	if err != nil {
		t.Fatal(err)
	}
	if tui.RunE == nil {
		t.Fatal("tui must have a startup handler")
	}
	for _, name := range []string{"prompt", "model", "effort", "resume", "continue"} {
		if root.Flags().Lookup(name) != nil {
			t.Errorf("root has TUI flag %q", name)
		}
		if tui.Flags().Lookup(name) == nil {
			t.Errorf("tui missing flag %q", name)
		}
	}
}

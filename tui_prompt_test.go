package main

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/TheMarstonConnell/ted/agent"
)

func TestTUIPromptFlag(t *testing.T) {
	cmd := newTUICommand()
	if err := cmd.ParseFlags([]string{"--prompt", "my prompt here"}); err != nil {
		t.Fatal(err)
	}
	if got, err := cmd.Flags().GetString("prompt"); err != nil || got != "my prompt here" {
		t.Fatalf("prompt = %q, err = %v", got, err)
	}
	if err := cmd.ValidateArgs([]string{"unexpected"}); err == nil {
		t.Fatal("positional arguments should still be rejected")
	}
}

func TestInitialPrompt(t *testing.T) {
	for _, prompt := range []string{"my prompt here", "/help", "  multiline\nprompt  ", strings.Repeat("long prompt ", 1000)} {
		t.Run(prompt[:min(len(prompt), 20)], func(t *testing.T) {
			m := tuiTestModel()
			m.initialPrompt = prompt
			batch, ok := m.Init()().(tea.BatchMsg)
			if !ok {
				t.Fatal("initial prompt not scheduled")
			}
			var startup tea.Msg
			for _, cmd := range batch {
				msg := cmd()
				if _, ok := msg.(initialPromptMsg); ok {
					startup = msg
				}
			}
			if startup == nil {
				t.Fatal("missing startup message")
			}
			next, cmd := m.Update(startup)
			m = next.(model)
			if cmd == nil || !m.busy || m.initialPrompt != "" {
				t.Fatal("turn not reserved and prompt not consumed")
			}
			if len(m.messages) != 2 || m.messages[1].kind != userMessage || m.messages[1].content != prompt {
				t.Fatal("prompt not recorded verbatim")
			}
			if m.textarea.Value() != "" || m.picker != nil {
				t.Fatal("prompt should be submitted as chat")
			}
			next, cmd = m.Update(initialPromptMsg{})
			m = next.(model)
			if cmd != nil || len(m.messages) != 2 {
				t.Fatal("prompt submitted twice")
			}
			m, cmd = submit(m, "another turn")
			if cmd != nil || m.messages[len(m.messages)-1].content != agent.ErrBusy.Error() {
				t.Fatal("overlapping turn allowed")
			}
			next, _ = m.Update(turnDoneMsg{err: errors.New("failed")})
			m = next.(model)
			if m.busy || m.messages[len(m.messages)-1].content != "Error: failed" {
				t.Fatal("turn completion not handled")
			}
		})
	}
}

func TestEmptyInitialPrompt(t *testing.T) {
	for _, prompt := range []string{"", " \n\t "} {
		m := tuiTestModel()
		m.initialPrompt = prompt
		if batch, ok := m.Init()().(tea.BatchMsg); ok {
			for _, cmd := range batch {
				if _, ok := cmd().(initialPromptMsg); ok {
					t.Fatal("empty prompt scheduled")
				}
			}
		}
		next, cmd := m.Update(initialPromptMsg{})
		m = next.(model)
		if cmd != nil || m.busy || len(m.messages) != 1 {
			t.Fatal("empty prompt submitted")
		}
	}
}

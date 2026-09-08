package main

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/TheMarstonConnell/harness/agent"
)

func tuiTestModel() model {
	a := agent.NewAgent(nil, []agent.Provider{agent.NewCodexProvider(nil), agent.NewOpenRouterProvider("")})
	m := initialModel(a)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	return next.(model)
}
func press(m model, code rune) (model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyPressMsg{Code: code})
	return next.(model), cmd
}
func submit(m model, text string) (model, tea.Cmd) {
	m.textarea.SetValue(text)
	return press(m, tea.KeyEnter)
}

func TestModelPickerFilteringAndConfirm(t *testing.T) {
	m := tuiTestModel()
	before := m.agent.Settings()
	m, cmd := submit(m, "/model")
	if cmd != nil || m.picker == nil || m.picker.selection.Current != before.Model {
		t.Fatal("picker did not open")
	}
	if !strings.Contains(m.View().Content, "Choose a model") {
		t.Fatal("picker not rendered")
	}
	// Typing a filter uses the ordinary input widget without submitting chat.
	next, _ := m.Update(tea.KeyPressMsg{Code: 's', Text: "terra"})
	m = next.(model)
	if got := m.pickerChoices(); len(got) != 1 || got[0].ID != "codex/gpt-5.6-terra" {
		t.Fatal(got)
	}
	m, _ = press(m, tea.KeyEnter)
	if m.picker != nil || m.agent.Settings().Model != "codex/gpt-5.6-terra" {
		t.Fatal("selection not applied")
	}
	if len(m.agent.Messages()) != 1 {
		t.Fatal("command entered model history")
	}
	if !strings.Contains(m.statusView(), "medium") {
		t.Fatal("effort missing from status")
	}
}

func TestEffortPickerCancelAndConfirm(t *testing.T) {
	m := tuiTestModel()
	m, _ = submit(m, "/effort")
	if m.picker == nil || m.picker.index != 1 {
		t.Fatal("current effort not selected")
	}
	m, _ = press(m, tea.KeyDown)
	m, cmd := press(m, tea.KeyEscape)
	if cmd != nil || m.picker != nil || m.agent.Settings().Effort != agent.EffortMedium {
		t.Fatal("escape changed settings or quit")
	}
	m, _ = submit(m, "/effort")
	m, _ = press(m, tea.KeyDown)
	m, _ = press(m, tea.KeyEnter)
	if m.agent.Settings().Effort != agent.EffortHigh {
		t.Fatal("effort not applied")
	}
}

func TestPickerNoMatchesAndResize(t *testing.T) {
	m := tuiTestModel()
	m, _ = submit(m, "/model")
	m.textarea.SetValue("no matches")
	m, cmd := press(m, tea.KeyEnter)
	if cmd != nil || m.picker == nil {
		t.Fatal("empty selection should stay open")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 32, Height: 18})
	m = next.(model)
	if !strings.Contains(m.View().Content, "No matching choices") {
		t.Fatal("missing empty-state feedback")
	}
}

func TestLocalErrorsAndBusyReservation(t *testing.T) {
	m := tuiTestModel()
	m, cmd := submit(m, "/nonesuch")
	if cmd != nil || m.busy || !strings.Contains(m.messages[len(m.messages)-1].content, "unknown command") {
		t.Fatal("unknown command was not local")
	}
	// Do not execute the returned network command: the UI must reserve the
	// turn even before Bubble Tea schedules it.
	m, cmd = submit(m, "hello")
	if cmd == nil || !m.busy {
		t.Fatal("turn not reserved")
	}
	settings := m.agent.Settings()
	m, cmd = submit(m, "/effort high")
	if cmd != nil || m.agent.Settings() != settings {
		t.Fatal("settings changed before worker started")
	}
	m, cmd = submit(m, "another turn")
	if cmd != nil || m.textarea.Value() != "another turn" {
		t.Fatal("overlapping turn or lost draft")
	}
	next, _ := m.Update(agent.AgentResponse{Content: "tool", ResponseType: "tool"})
	m = next.(model)
	if m.textarea.Value() != "another turn" {
		t.Fatal("output erased draft")
	}
	next, _ = m.Update(turnDoneMsg{err: errors.New("offline")})
	m = next.(model)
	if m.busy || !strings.Contains(m.messages[len(m.messages)-1].content, "offline") {
		t.Fatal("failure not displayed")
	}
}

func TestExitSlashCommand(t *testing.T) {
	for _, busy := range []bool{false, true} {
		m := tuiTestModel()
		m.busy = busy
		m, cmd := submit(m, " /exit ")
		if cmd == nil {
			t.Fatal("/exit did not return a command")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("/exit did not quit")
		}
		if len(m.agent.Messages()) != 1 {
			t.Fatal("exit entered model history")
		}
	}
}

func TestExitValidationAndChat(t *testing.T) {
	m := tuiTestModel()
	m, cmd := submit(m, "/exit now")
	if cmd != nil || !strings.Contains(m.messages[len(m.messages)-1].content, "usage: /exit") {
		t.Fatal("exit arguments should produce a local usage error")
	}
	m, _ = submit(m, "/help")
	if !strings.Contains(m.messages[len(m.messages)-1].content, "/exit") {
		t.Fatal("exit missing from help")
	}
	for _, text := range []string{"exit", "//exit"} {
		m := tuiTestModel()
		m, cmd := submit(m, text)
		if cmd == nil || !m.busy || m.messages[len(m.messages)-1].content != strings.TrimPrefix(text, "/") {
			t.Fatalf("%q should be ordinary chat", text)
		}
		// Do not execute the chat command, which would contact the provider.
	}
}

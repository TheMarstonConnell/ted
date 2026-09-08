package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestWorkingSpinnerLifecycle(t *testing.T) {
	for _, turnErr := range []error{nil, errors.New("failed")} {
		m := tuiTestModel()
		idleStatus := m.statusView()
		m, cmd := submit(m, "hello")
		if cmd == nil || !strings.Contains(m.viewport.View(), "working...") {
			t.Fatal("turn did not start with a visible working indicator")
		}
		if m.statusView() != idleStatus {
			t.Fatal("working state changed bottom status bar")
		}
		before := m.workingSpinner.View()
		next, cmd := m.Update(m.workingSpinner.Tick())
		m = next.(model)
		if cmd == nil || m.workingSpinner.View() == before {
			t.Fatal("spinner did not animate")
		}
		for _, response := range []agent.AgentResponse{
			{ResponseType: "tool", Content: "Ran shell command"},
			{Content: "Here is the answer"},
		} {
			next, _ = m.Update(response)
			m = next.(model)
			view := m.viewport.View()
			if strings.Count(view, "working...") != 1 || strings.Index(view, "working...") < strings.Index(view, response.Content) {
				t.Fatal("working indicator should follow the latest response")
			}
		}
		staleTick := m.workingSpinner.Tick()
		next, _ = m.Update(turnDoneMsg{err: turnErr})
		m = next.(model)
		if strings.Contains(m.viewport.View(), "working...") {
			t.Fatal("indicator remained after completion")
		}
		next, cmd = m.Update(staleTick)
		m = next.(model)
		if cmd != nil {
			t.Fatal("idle spinner kept ticking")
		}
		for _, entry := range m.messages {
			if strings.Contains(entry.content, "working...") {
				t.Fatal("indicator recorded in history")
			}
		}
		m, _ = submit(m, "next turn")
		_, cmd = m.Update(staleTick)
		if cmd != nil {
			t.Fatal("previous turn tick restarted spinner")
		}
	}
}

func TestWorkingSpinnerPreservesScroll(t *testing.T) {
	m := tuiTestModel()
	m, _ = submit(m, "hello")
	m.appendMessage(agentMessage, strings.Repeat("A line of output\n\n", 80))
	m.viewport.GotoTop()
	offset := m.viewport.YOffset()
	next, _ := m.Update(m.workingSpinner.Tick())
	m = next.(model)
	if m.viewport.YOffset() != offset {
		t.Fatal("spinner tick moved scroll position")
	}
	m.viewport.GotoBottom()
	next, _ = m.Update(m.workingSpinner.Tick())
	m = next.(model)
	if !m.viewport.AtBottom() {
		t.Fatal("spinner tick lost tail following")
	}
}

package main

import (
	"charm.land/lipgloss/v2"
	"github.com/TheMarstonConnell/ted/agent"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
)

func TestContextMeter(t *testing.T) {
	for _, tt := range []struct {
		u    agent.ContextUsage
		want string
	}{
		{agent.ContextUsage{}, "ctx 100% left"},
		{agent.ContextUsage{ContextWindow: 1000}, "ctx 100% left"},
		{agent.ContextUsage{Known: true}, "ctx —"},
		{agent.ContextUsage{Known: true, ContextWindow: 1000}, "ctx 100% left"},
		{agent.ContextUsage{Known: true, ContextWindow: 1000, EstimatedTokens: 420}, "ctx 58% left"},
		{agent.ContextUsage{Known: true, ContextWindow: 1000, EstimatedTokens: 1100}, "ctx 0% left"},
	} {
		if got, _ := contextMeter(tt.u); got != tt.want {
			t.Fatalf("%q != %q", got, tt.want)
		}
	}
}

func TestUsageStatusAndEvent(t *testing.T) {
	m := tuiTestModel()
	m.directory = strings.Repeat("長/path/", 100)
	status := m.statusView()
	if !strings.Contains(ansi.Strip(status), "ctx 100% left") {
		t.Fatal(status)
	}
	if lipgloss.Width(status) > lipgloss.Width(m.inputView()) {
		t.Fatal("status overflows")
	}
	count := len(m.messages)
	next, _ := m.Update(agent.AgentResponse{ResponseType: "usage"})
	if len(next.(model).messages) != count {
		t.Fatal("usage event polluted transcript")
	}
}

package main

import (
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/charmbracelet/x/ansi"
)

type nudgeHistoryAgent struct {
	tuiAgent
	messages []agent.Message
}

func (a *nudgeHistoryAgent) Messages() []agent.Message { return a.messages }

func TestBotNotificationLiveAndHistory(t *testing.T) {
	m := tuiTestModel()
	live := "Bot notification from chat reviewer: checks passed"
	next, cmd := m.Update(agent.AgentResponse{ResponseType: "bot", Content: live})
	m = next.(model)
	if cmd != nil || len(m.messages) != 2 || m.messages[1] != (transcriptEntry{kind: botMessage, content: live}) {
		t.Fatalf("live bot notification: %+v", m.messages)
	}
	if !strings.Contains(ansi.Strip(m.renderEntry(m.messages[1], 80)), "checks passed") {
		t.Fatal("live notification is not visible")
	}

	history := &nudgeHistoryAgent{
		tuiAgent: m.agent,
		messages: []agent.Message{{
			Role:          "user",
			Kind:          "bot",
			SenderAgentID: "reviewer",
			Content:       agent.TextContent("checks passed"),
		}},
	}
	reloaded := initialModel(history)
	if len(reloaded.messages) != 2 || reloaded.messages[1] != m.messages[1] {
		t.Fatalf("history bot notification: %+v", reloaded.messages)
	}
}

func TestBotNotificationCollapsesLongContent(t *testing.T) {
	m := model{}
	content := "Bot notification: " + strings.Repeat("long update ", 50)
	entry := transcriptEntry{kind: botMessage, content: content}
	narrow := ansi.Strip(m.renderEntry(entry, 40))
	if strings.ContainsAny(narrow, "\n\r\t\x1b") || ansi.StringWidth(narrow) > 30 || !strings.HasSuffix(narrow, "…") {
		t.Fatalf("bot notification was not collapsed: %q", narrow)
	}
	wide := ansi.Strip(m.renderEntry(entry, 1000))
	if wide != strings.TrimSpace(content) || entry.content != content {
		t.Fatalf("full notification was not retained: %q", wide)
	}
}

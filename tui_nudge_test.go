package main

import (
	"context"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/remote"
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
	next, cmd := m.Update(remote.QueuedMessage{Text: "checks passed", SenderAgentID: "reviewer"})
	m = next.(model)
	if cmd != nil || len(m.messages) != 2 || m.messages[1] != (transcriptEntry{kind: toolCallMessage, content: live}) {
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

func TestBotNotificationEventReplayRows(t *testing.T) {
	snapshot := remote.Snapshot{
		ID: "target", Cursor: 5,
		Messages: []agent.Message{
			{Role: "user", Kind: "bot", SenderAgentID: "reviewer", Content: agent.TextContent("same report")},
			{Role: "user", Kind: "bot", SenderAgentID: "reviewer", Content: agent.TextContent("saved before shutdown")},
		},
	}
	snapshot.Messages = nil
	snapshot.Cursor = 0
	instance := remote.NewAgent(context.Background(), nil, snapshot, "", nil)
	m := initialModel(instance)
	if len(m.messages) != 1 {
		t.Fatalf("snapshot history rendered before event replay: %+v", m.messages)
	}
	replayed := []remote.QueuedMessage{
		{Text: "same report", SenderAgentID: "reviewer"},
		{Text: "same report", SenderAgentID: "reviewer"},
		{Text: "saved before shutdown", SenderAgentID: "reviewer"},
		{Text: "lost during crash", SenderAgentID: "reviewer"},
		{Text: "cancelled before start", SenderAgentID: "reviewer"},
	}
	for _, notification := range replayed {
		next, _ := m.Update(notification)
		m = next.(model)
	}
	var rows []string
	for _, entry := range m.messages {
		if entry.kind == toolCallMessage && strings.HasPrefix(entry.content, "Bot notification") {
			rows = append(rows, entry.content)
		}
	}
	if len(rows) != len(replayed) {
		t.Fatalf("bot rows = %q, want %d", rows, len(replayed))
	}
	for i, notification := range replayed {
		want := formatTUIBotNotification(notification)
		if rows[i] != want {
			t.Fatalf("bot rows = %q, want row %q", rows, want)
		}
	}
}

func TestBotNotificationCollapsesLongContent(t *testing.T) {
	m := model{}
	content := "Bot notification: " + strings.Repeat("long update ", 50)
	entry := transcriptEntry{kind: toolCallMessage, content: content}
	narrow := ansi.Strip(m.renderEntry(entry, 40))
	if strings.ContainsAny(narrow, "\n\r\t\x1b") || ansi.StringWidth(narrow) > 30 || !strings.HasSuffix(narrow, "…") {
		t.Fatalf("bot notification was not collapsed: %q", narrow)
	}
	wide := ansi.Strip(m.renderEntry(entry, 1000))
	if wide != strings.TrimSpace(content) || entry.content != content {
		t.Fatalf("full notification was not retained: %q", wide)
	}
}

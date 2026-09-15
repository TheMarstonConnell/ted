package main

import (
	"context"
	"encoding/json"
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
	queued := func(id, text, status string) remote.QueuedMessage {
		return remote.QueuedMessage{ID: id, Text: text, Kind: "bot", SenderAgentID: "reviewer", Status: status}
	}
	event := func(cursor uint64, eventType string, data any) remote.Event {
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return remote.Event{AgentID: "target", Cursor: cursor, Type: eventType, Data: encoded}
	}
	same := queued("completed", "same report", "pending")
	failed := queued("failed", "same report", "pending")
	graceful := queued("graceful", "saved before shutdown", "pending")
	crashed := queued("crashed", "lost during crash", "pending")
	cancelled := queued("cancelled", "cancelled before start", "pending")
	events := []remote.Event{
		event(1, "message.queued", same),
		event(2, "conversation", []agent.Message{{Role: "user", Kind: "bot", SenderAgentID: "reviewer", Content: agent.TextContent(same.Text)}}),
		event(3, "turn.completed", queued(same.ID, same.Text, "completed")),
		event(4, "message.queued", failed),
		event(5, "turn.failed", remote.QueuedMessage{ID: failed.ID, Text: failed.Text, Kind: "bot", SenderAgentID: "reviewer", Status: "failed", Error: "provider failed"}),
		event(6, "message.queued", graceful),
		event(7, "conversation", []agent.Message{{Role: "user", Kind: "bot", SenderAgentID: "reviewer", Content: agent.TextContent(graceful.Text)}}),
		event(8, "turn.interrupted", queued(graceful.ID, graceful.Text, "interrupted")),
		event(9, "message.queued", crashed),
		event(10, "turn.interrupted", queued(crashed.ID, crashed.Text, "interrupted")),
		event(11, "message.queued", cancelled),
		event(12, "message.cancelled", queued(cancelled.ID, cancelled.Text, "cancelled")),
	}
	instance, err := remote.NewAgentWithEvents(context.Background(), nil, remote.Snapshot{
		ID: "target", Cursor: 12,
		Messages: []agent.Message{
			{Role: "user", Kind: "bot", SenderAgentID: "reviewer", Content: agent.TextContent(same.Text)},
			{Role: "user", Kind: "bot", SenderAgentID: "reviewer", Content: agent.TextContent(graceful.Text)},
		},
	}, "", nil, events)
	if err != nil {
		t.Fatal(err)
	}
	model := initialModel(instance)
	var rows []string
	for _, entry := range model.messages {
		if entry.kind == toolCallMessage && strings.HasPrefix(entry.content, "Bot notification") {
			rows = append(rows, entry.content)
		}
	}
	want := []string{
		"Bot notification from chat reviewer: same report",
		"Bot notification from chat reviewer: same report",
		"Bot notification from chat reviewer: saved before shutdown",
		"Bot notification from chat reviewer: lost during crash",
		"Bot notification from chat reviewer: cancelled before start",
	}
	if len(rows) != len(want) {
		t.Fatalf("bot rows = %q, want %q", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("bot rows = %q, want %q", rows, want)
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

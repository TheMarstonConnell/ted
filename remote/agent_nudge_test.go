package remote

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestBotQueuedEventRendering(t *testing.T) {
	a := NewAgent(context.Background(), nil, Snapshot{ID: "target"}, "", nil)
	data, err := json.Marshal(QueuedMessage{
		ID:            "nudge",
		Text:          "checks passed",
		Kind:          "bot",
		SenderAgentID: "reviewer",
		Status:        "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	var updates []Update
	if err := a.consume(Event{
		AgentID: "target",
		Cursor:  1,
		Type:    "message.queued",
		Data:    data,
	}, func(update Update) { updates = append(updates, update) }); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Output == nil {
		t.Fatalf("updates: %+v", updates)
	}
	output := updates[0].Output
	if output.ResponseType != "bot" || output.Content != "Bot notification from chat reviewer: checks passed" {
		t.Fatalf("output: %+v", output)
	}

	// Conversation checkpoints retain history but do not produce a second row.
	history, err := json.Marshal([]agent.Message{{
		Role:          "user",
		Kind:          "bot",
		SenderAgentID: "reviewer",
		Content:       agent.TextContent("checks passed"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.consume(Event{
		AgentID: "target",
		Cursor:  2,
		Type:    "conversation",
		Data:    history,
	}, func(update Update) { updates = append(updates, update) }); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || len(a.Messages()) != 1 || a.Messages()[0].Kind != "bot" {
		t.Fatalf("notification duplicated or history lost: updates=%+v messages=%+v", updates, a.Messages())
	}
}

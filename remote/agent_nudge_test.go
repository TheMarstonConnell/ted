package remote

import (
	"context"
	"encoding/json"
	"reflect"
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
	if len(updates) != 1 || updates[0].Bot == nil || updates[0].Output != nil {
		t.Fatalf("updates: %+v", updates)
	}
	if *updates[0].Bot != (agent.BotNotification{Text: "checks passed", SenderAgentID: "reviewer"}) {
		t.Fatalf("bot notification: %+v", updates[0].Bot)
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

func TestInFlightBotNotifications(t *testing.T) {
	a := NewAgent(context.Background(), nil, Snapshot{
		ID: "target",
		Queue: []QueuedMessage{
			{ID: "pending", Text: "first", Kind: "bot", SenderAgentID: "one", Status: "pending"},
			{ID: "user", Text: "human", Status: "pending"},
			{ID: "running", Text: "second", Kind: "bot", SenderAgentID: "two", Status: "running"},
			{ID: "done", Text: "committed", Kind: "bot", Status: "completed"},
		},
	}, "", nil)
	want := []agent.BotNotification{
		{Text: "first", SenderAgentID: "one"},
		{Text: "second", SenderAgentID: "two"},
	}
	if got := a.InFlightBotNotifications(); !reflect.DeepEqual(got, want) {
		t.Fatalf("notifications = %+v, want %+v", got, want)
	}
}

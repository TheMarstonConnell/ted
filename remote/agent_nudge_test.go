package remote

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestBotQueuedAndUpdatedEventRendering(t *testing.T) {
	a := NewAgent(context.Background(), nil, Snapshot{ID: "target"}, "", nil)
	original := QueuedMessage{ID: "nudge", Text: "checks passed", Kind: "bot", SenderAgentID: "reviewer", Status: "pending"}
	merged := original
	merged.Text += "\n\ncoverage passed"
	var updates []Update
	consume := func(cursor uint64, eventType string, data any) {
		t.Helper()
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.consume(Event{AgentID: "target", Cursor: cursor, Type: eventType, Data: encoded}, func(update Update) {
			updates = append(updates, update)
		}); err != nil {
			t.Fatal(err)
		}
	}
	consume(1, "message.queued", original)
	consume(2, "message.updated", merged)
	consume(2, "message.updated", merged)
	consume(1, "message.queued", original)
	if len(updates) != 2 {
		t.Fatalf("updates = %+v, want acceptance and one merge", updates)
	}
	for i, want := range []QueuedMessage{original, merged} {
		if updates[i].Bot == nil || *updates[i].Bot != want || updates[i].Output != nil || updates[i].Busy != nil {
			t.Fatalf("update %d = %+v, want bot %+v", i, updates[i], want)
		}
	}
	if len(a.Messages()) != 0 {
		t.Fatalf("pending notification added to history: %+v", a.Messages())
	}
	consume(3, "turn.started", merged)
	consume(4, "conversation", []agent.Message{{Role: "user", Kind: "bot", SenderAgentID: "reviewer", Content: agent.TextContent(merged.Text)}})
	if len(updates) != 2 || len(a.Messages()) != 1 || a.Messages()[0].Kind != "bot" || a.Messages()[0].Content.Text() != merged.Text {
		t.Fatalf("merged history lost or duplicated: updates=%+v messages=%+v", updates, a.Messages())
	}
}

func TestFullEventReplayRendersEveryAcceptedBotOnce(t *testing.T) {
	queued := func(id, text, status string) QueuedMessage {
		return QueuedMessage{ID: id, Text: text, Kind: "bot", SenderAgentID: "reviewer", Status: status}
	}
	event := func(cursor uint64, eventType string, data any) Event {
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return Event{AgentID: "target", Cursor: cursor, Type: eventType, Data: encoded}
	}
	same := queued("completed", "same report", "pending")
	failed := queued("failed", "same report", "pending")
	graceful := queued("graceful", "saved before shutdown", "pending")
	crashed := queued("crashed", "lost during crash", "pending")
	cancelled := queued("cancelled", "cancelled before start", "pending")
	events := []Event{
		event(1, "message.queued", same),
		event(2, "conversation", []agent.Message{{Role: "user", Kind: "bot", SenderAgentID: "reviewer", Content: agent.TextContent(same.Text)}}),
		event(3, "turn.completed", queued(same.ID, same.Text, "completed")),
		event(4, "message.queued", failed),
		event(5, "turn.failed", QueuedMessage{ID: failed.ID, Text: failed.Text, Kind: "bot", SenderAgentID: "reviewer", Status: "failed", Error: "provider failed"}),
		event(6, "message.queued", graceful),
		event(7, "conversation", []agent.Message{{Role: "user", Kind: "bot", SenderAgentID: "reviewer", Content: agent.TextContent(graceful.Text)}}),
		event(8, "turn.interrupted", queued(graceful.ID, graceful.Text, "interrupted")),
		event(9, "message.queued", crashed),
		event(10, "turn.interrupted", queued(crashed.ID, crashed.Text, "interrupted")),
		event(11, "message.queued", cancelled),
		event(12, "message.cancelled", queued(cancelled.ID, cancelled.Text, "cancelled")),
	}
	a := NewAgent(context.Background(), nil, Snapshot{ID: "target"}, "", nil)
	var updates []Update
	for _, event := range events {
		if err := a.consume(event, func(update Update) { updates = append(updates, update) }); err != nil {
			t.Fatal(err)
		}
	}
	var notifications []string
	for _, update := range updates {
		if update.Bot != nil {
			notifications = append(notifications, update.Bot.Text)
		}
	}
	want := []string{"same report", "same report", "saved before shutdown", "lost during crash", "cancelled before start"}
	if len(notifications) != len(want) {
		t.Fatalf("notifications = %q, want %q", notifications, want)
	}
	for i := range want {
		if notifications[i] != want[i] {
			t.Fatalf("notifications = %q, want %q", notifications, want)
		}
	}
	if got := a.Messages(); len(got) != 2 {
		t.Fatalf("replayed history = %+v", got)
	}
}

package remote

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestQueuedAttachmentRendering(t *testing.T) {
	images := []agent.Attachment{{Name: "screen.png", URL: "data:image/png;base64,aW1hZ2U="}}
	for _, tt := range []struct {
		name    string
		message QueuedMessage
		want    string
	}{
		{"image only", QueuedMessage{Attachments: images}, "[Image attachment]"},
		{"text and image", QueuedMessage{Text: "inspect this", Attachments: images}, "inspect this"},
		{"text only", QueuedMessage{Text: "hello"}, "hello"},
		{"empty legacy message", QueuedMessage{}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAgent(context.Background(), nil, Snapshot{ID: "target"}, "", nil)
			var updates []Update
			consume := func(cursor uint64, kind string) {
				t.Helper()
				data, err := json.Marshal(tt.message)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.consume(Event{AgentID: "target", Cursor: cursor, Type: kind, Data: data}, func(update Update) {
					updates = append(updates, update)
				}); err != nil {
					t.Fatal(err)
				}
			}
			consume(1, "message.queued")
			consume(1, "message.queued")
			consume(2, "turn.started")
			if tt.want == "" {
				if len(updates) != 0 {
					t.Fatalf("empty message emitted updates: %+v", updates)
				}
				return
			}
			if len(updates) != 1 {
				t.Fatalf("got %d updates, want exactly one user row", len(updates))
			}
			got := updates[0]
			if got.Output == nil || got.Output.ResponseType != "user" || got.Output.Content != tt.want || got.Bot != nil || got.Busy != nil {
				t.Fatalf("update = %+v, want user content %q", got, tt.want)
			}
			if len(a.Messages()) != 0 {
				t.Fatal("pending image added to committed history")
			}
		})
	}
}

package controlplane

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestNarrowReadSnapshots(t *testing.T) {
	s := &Service{state: emptyState(), changed: make(chan struct{})}
	s.state.Agents["a"] = &storedAgent{Agent: Agent{
		ID: "a", Queue: []QueuedMessage{{ID: "q", Text: "pending"}},
		Messages: []agent.Message{
			{Role: "user", Content: agent.TextContent("first")},
			{Role: "assistant", Content: agent.TextContent("second"), ToolCalls: []agent.ToolCall{{Id: "tool"}}},
			{Role: "user", Content: agent.TextContent("third")},
		}, ActiveSettings: &Settings{Model: "original"},
	}}
	queue, err := s.QueuedMessages("a")
	if err != nil || len(queue) != 1 {
		t.Fatalf("queue: %v %v", queue, err)
	}
	queue[0].Text = "changed"
	messages, next, err := s.History("a", 1, 1)
	if err != nil || next != 2 || len(messages) != 1 || messages[0].Content.Text() != "second" {
		t.Fatalf("history: %v %d %v", messages, next, err)
	}
	messages[0].ToolCalls[0].Id = "changed"
	if err := json.Unmarshal([]byte(`"replacement"`), &messages[0].Content); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetAgent("a")
	if a.Queue[0].Text != "pending" || a.Messages[1].ToolCalls[0].Id != "tool" || a.Messages[1].Content.Text() != "second" {
		t.Fatal("narrow reads changed stored state")
	}
	for _, tc := range []struct {
		after                       uint64
		limit, next, length, status int
	}{
		{0, 100, 3, 3, 0}, {3, 100, 3, 0, 0}, {4, 100, 0, 0, 410}, {^uint64(0), 1, 0, 0, 410}, {0, 1001, 0, 0, 400}, {0, 0, 3, 3, 0},
	} {
		result, next, err := s.History("a", tc.after, tc.limit)
		if tc.status != 0 {
			var e *Error
			if !errors.As(err, &e) || e.Status != tc.status {
				t.Fatalf("%+v: %v", tc, err)
			}
		} else if err != nil || next != tc.next || len(result) != tc.length {
			t.Fatalf("%+v: %v %d %v", tc, result, next, err)
		}
	}
	if _, err := s.QueuedMessages("missing"); err == nil {
		t.Fatal("missing queue succeeded")
	}
	if _, _, err := s.History("missing", 0, 1); err == nil {
		t.Fatal("missing history succeeded")
	}
	full, _, _, err := s.SnapshotEvents(nil, true, nil)
	if err != nil || len(full) != 1 || len(full[0].Messages) != 3 {
		t.Fatalf("full inventory changed: %v", err)
	}
	summaries, _, _, err := s.snapshotEvents(nil, true, nil, true)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries: %v", err)
	}
	want := full[0]
	want.Queue, want.Messages = nil, nil
	want.DisplayTitle = "pending"
	if !reflect.DeepEqual(want, summaries[0]) {
		t.Fatal("summary changed metadata")
	}
	summaries[0].ActiveSettings.Model = "changed"
	if s.state.Agents["a"].Agent.ActiveSettings.Model != "original" {
		t.Fatal("summary aliases stored settings")
	}
}

func TestSnapshotEventFilterSkipsUnselectedLogsAndValidatesIDs(t *testing.T) {
	s := &Service{state: emptyState(), changed: make(chan struct{})}
	s.state.Agents["selected"] = &storedAgent{Agent: Agent{ID: "selected", Cursor: 1}, Events: []Event{{AgentID: "selected", Cursor: 1, Data: json.RawMessage(`{"ok":true}`)}}}
	// Unselected malformed JSON must never reach the cloning path.
	s.state.Agents["background"] = &storedAgent{Agent: Agent{ID: "background", Cursor: 1}, Events: []Event{{AgentID: "background", Cursor: 1, Data: json.RawMessage(`{`)}}}

	inventory, events, _, err := s.snapshotFilteredEvents(nil, false, []string{"background", "selected"}, map[string]bool{"selected": true}, true, true)
	if err != nil || len(inventory) != 2 || len(events) != 1 || events[0].AgentID != "selected" {
		t.Fatalf("filtered snapshot: inventory=%d events=%+v err=%v", len(inventory), events, err)
	}
	inventory, events, _, err = s.snapshotFilteredEvents(nil, false, []string{"background", "selected"}, map[string]bool{}, true, true)
	if err != nil || len(inventory) != 2 || len(events) != 0 {
		t.Fatalf("inventory-only snapshot: inventory=%d events=%+v err=%v", len(inventory), events, err)
	}
	_, events, _, err = s.snapshotFilteredEvents(nil, false, []string{"selected"}, nil, true, true)
	if err != nil || len(events) != 1 {
		t.Fatalf("omitted filter did not preserve replay: %+v %v", events, err)
	}

	// subscribe_all makes existing unsettled agents valid event selections.
	_, events, _, err = s.snapshotFilteredEvents(nil, true, nil, map[string]bool{"selected": true}, true, true)
	if err != nil || len(events) != 1 {
		t.Fatalf("all-mode event id: %+v %v", events, err)
	}

	_, _, _, err = s.snapshotFilteredEvents(nil, false, []string{"selected"}, map[string]bool{"background": true}, true, true)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Status != 400 || serviceErr.Code != "invalid" {
		t.Fatalf("unsubscribed event id: %v", err)
	}
	s.state.Agents["selected"].Agent.Settled = true
	_, _, _, err = s.snapshotFilteredEvents(nil, true, nil, map[string]bool{"selected": true}, true, true)
	if !errors.As(err, &serviceErr) || serviceErr.Status != 400 || serviceErr.Code != "invalid" {
		t.Fatalf("settled all-mode event id: %v", err)
	}
	_, _, _, err = s.snapshotFilteredEvents(nil, false, []string{"selected"}, map[string]bool{"missing": true}, true, true)
	if !errors.As(err, &serviceErr) || serviceErr.Status != 404 || serviceErr.Code != "not_found" {
		t.Fatalf("unknown event id: %v", err)
	}
	_, _, _, err = s.snapshotFilteredEvents(map[string]uint64{"missing": 0}, false, nil, map[string]bool{}, true, true)
	if !errors.As(err, &serviceErr) || serviceErr.Status != 410 || serviceErr.Code != "cursor_invalid" {
		t.Fatalf("inventory-only filter hid invalid cursor: %v", err)
	}
}

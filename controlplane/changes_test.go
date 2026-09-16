package controlplane

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestStateChangesRollbackTouchedRecords(t *testing.T) {
	state := emptyState()
	state.Projects["p"] = Project{ID: "p", Name: "original"}
	state.Agents["a"] = &storedAgent{Agent: Agent{ID: "a", ProjectID: "p", State: "idle", Queue: []QueuedMessage{{ID: "q", Status: "pending"}}, Messages: []agent.Message{{Role: "user", Content: agent.TextContent("old")}}}, Events: []Event{{AgentID: "a", Cursor: 1, Data: json.RawMessage(`{"old":true}`)}}}
	state.Receipts["key"] = receipt{AgentID: "a", MessageID: "q", Fingerprint: "old"}
	unrelated := &storedAgent{Agent: Agent{ID: "untouched"}}
	state.Agents["untouched"] = unrelated
	original, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	changes := newStateChanges()
	changes.project(state, "p")
	changes.project(state, "new-project")
	changes.queued(state, "a", 0)
	changes.receipt(state, "key")
	changes.receipt(state, "new-key")
	changes.agent(state, "new-agent")
	state.Projects["p"] = Project{ID: "p", Name: "changed"}
	state.Projects["new-project"] = Project{ID: "new-project"}
	a := state.Agents["a"]
	a.Agent.Queue[0].Status = "running"
	// A second capture must not replace the pre-transaction image.
	changes.queued(state, "a", 0)
	a.Agent.Queue = append(a.Agent.Queue, QueuedMessage{ID: "new", Status: "pending"})
	a.Agent.Messages = append(a.Agent.Messages, agent.Message{Role: "assistant", Content: agent.TextContent("new")})
	a.Events = append(a.Events, Event{AgentID: "a", Cursor: 2, Data: json.RawMessage(`{}`)})
	a.Agent.State = "running"
	a.Agent.Cursor = 2
	a.ManifestOffset = 123
	state.Agents["new-agent"] = &storedAgent{Agent: Agent{ID: "new-agent"}}
	delete(state.Receipts, "key")
	state.Receipts["new-key"] = receipt{AgentID: "new-agent"}
	changes.rollback(&state)
	restored, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("rollback changed durable state:\n%s\n%s", original, restored)
	}
	if state.Agents["untouched"] != unrelated {
		t.Fatal("rollback replaced untouched agent")
	}
}

func TestStateChangesRollbackDeletionAndAppendWithoutCopyingHistory(t *testing.T) {
	state := emptyState()
	state.Projects["p"] = Project{ID: "p"}
	// Capacity lets appends reuse backing arrays: rollback still restores the old lengths.
	record := &storedAgent{Agent: Agent{ID: "a", ProjectID: "p", Queue: make([]QueuedMessage, 1, 4), Messages: make([]agent.Message, 1, 4)}, Events: make([]Event, 1, 4)}
	record.Agent.Queue[0] = QueuedMessage{ID: "q", Status: "pending"}
	record.Agent.Messages[0] = agent.Message{Role: "user", Content: agent.TextContent("retained")}
	record.Events[0] = Event{AgentID: "a", Cursor: 1, Data: json.RawMessage(`{}`)}
	state.Agents["a"] = record
	c := newStateChanges()
	c.agent(state, "a")
	// Prefix storage stays immutable and shared until a caller edits a queue entry.
	if &c.agents["a"].Agent.Messages[0] != &record.Agent.Messages[0] || &c.agents["a"].Events[0] != &record.Events[0] {
		t.Fatal("capturing metadata copied retained history")
	}
	record.Agent.Queue = append(record.Agent.Queue, QueuedMessage{ID: "next"})
	record.Agent.Messages = append(record.Agent.Messages, agent.Message{Role: "user", Content: agent.TextContent("next")})
	record.Events = append(record.Events, Event{Cursor: 2})
	c.project(state, "p")
	delete(state.Agents, "a")
	delete(state.Projects, "p")
	c.rollback(&state)
	a := state.Agents["a"]
	if len(a.Agent.Queue) != 1 || len(a.Agent.Messages) != 1 || len(a.Events) != 1 || !reflect.DeepEqual(state.Projects["p"], Project{ID: "p"}) {
		t.Fatal("deletion/append rollback lost records")
	}
	if a.Agent.Messages[0].Content.Text() != "retained" {
		t.Fatal("rollback lost immutable prefix")
	}
}

package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestReadStatusTracksOnlyAgentOutputAndAcknowledgesExactCursor(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	toolResult := make(chan struct{})
	finish := make(chan struct{})
	p := &httpTestProvider{complete: func(r agent.CompletionRequest) (*agent.Response, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			return &agent.Response{Choices: []agent.Choice{{FinishReason: "tool_calls", Message: agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{ToolCallType: "function", Id: "tool-1", Function: agent.FunctionCall{Name: "bash", Arguments: `{"command":"printf tool"}`}}}}}}}, nil
		}
		if call == 2 {
			close(toolResult)
			select {
			case <-r.Context.Done():
				return nil, r.Context.Err()
			case <-finish:
			}
		}
		return &agent.Response{Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("done")}}}}, nil
	}}
	s, err := NewService(t.TempDir(), nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	project, err := s.CreateProject(CreateProjectRequest{Name: "test", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Prompt: "use a tool"}, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolResult:
	case <-time.After(3 * time.Second):
		t.Fatal("tool result was not emitted")
	}
	toolOnly, err := s.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if toolOnly.LastResponseCursor != 0 || toolOnly.ReadCursor != 0 {
		t.Fatalf("tool output changed read status: %+v", toolOnly)
	}
	close(finish)
	completed := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" && a.LastResponseCursor > 0 })
	responseCursor := completed.LastResponseCursor
	events, err := s.Events(a.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundResponse := false
	for _, event := range events {
		if event.Cursor != responseCursor {
			continue
		}
		foundResponse = true
		var output agent.AgentResponse
		if err = json.Unmarshal(event.Data, &output); err != nil || event.Type != "output" || output.ResponseType != "agent" {
			t.Fatalf("last response cursor does not identify agent output: %+v %v", event, err)
		}
	}
	if !foundResponse {
		t.Fatal("last response cursor was not retained")
	}
	beforeRead := completed.Cursor
	read, err := s.ReadAgent(a.ID, responseCursor)
	if err != nil {
		t.Fatal(err)
	}
	if read.ReadCursor != responseCursor || read.Cursor != beforeRead+1 || read.LastResponseCursor != responseCursor {
		t.Fatalf("read acknowledgement: %+v", read)
	}
	readEvents, err := s.Events(a.ID, read.Cursor-1, 1)
	if err != nil || len(readEvents) != 1 || readEvents[0].Type != "agent.updated" {
		t.Fatalf("missing read update event: %+v %v", readEvents, err)
	}
	var update struct {
		LastResponseCursor uint64 `json:"last_response_cursor"`
		ReadCursor         uint64 `json:"read_cursor"`
	}
	if err = json.Unmarshal(readEvents[0].Data, &update); err != nil || update.LastResponseCursor != responseCursor || update.ReadCursor != responseCursor {
		t.Fatalf("read update payload: %+v %v", update, err)
	}
	unchanged, err := s.ReadAgent(a.ID, responseCursor-1)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Cursor != read.Cursor || unchanged.ReadCursor != responseCursor {
		t.Fatalf("lower acknowledgement was not a no-op: %+v", unchanged)
	}
	_, err = s.ReadAgent(a.ID, read.Cursor+1)
	assertStatus(t, err, 410)

	if _, err = s.Submit(a.ID, "another response", ""); err != nil {
		t.Fatal(err)
	}
	traced := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" && a.LastResponseCursor > responseCursor })
	if traced.ReadCursor != responseCursor || traced.LastResponseCursor <= traced.ReadCursor {
		t.Fatalf("acknowledgement swallowed a racing response: %+v", traced)
	}
}

func TestReadStatusMutationRollsBackOnStorageFailure(t *testing.T) {
	s, _, _, a := serviceFixture(t)
	s.mu.Lock()
	s.save = func(string, diskState) error { return errors.New("disk full") }
	s.mu.Unlock()
	_, err := s.ReadAgent(a.ID, a.Cursor)
	assertStatus(t, err, 503)
	got, err := s.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadCursor != 0 || got.Cursor != a.Cursor {
		t.Fatalf("failed read acknowledgement mutated state: %+v", got)
	}
}

func TestPreReadStatusStateMigratesOnce(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	now := time.Now().UTC()
	project := Project{ID: "project", Name: "project", Root: root}
	events := []Event{
		{AgentID: "agent", Cursor: 1, Type: "agent.created", Data: json.RawMessage(`{}`), CreatedAt: now},
		{AgentID: "agent", Cursor: 2, Type: "output", Data: json.RawMessage(`{"ResponseType":"tool"}`), CreatedAt: now},
		{AgentID: "agent", Cursor: 3, Type: "output", Data: json.RawMessage(`{"ResponseType":"agent","Content":"old response"}`), CreatedAt: now},
		{AgentID: "agent", Cursor: 4, Type: "output", Data: json.RawMessage(`{"ResponseType":"tool_result"}`), CreatedAt: now},
	}
	state := emptyState()
	state.Version = legacyStoreVersion
	state.Projects[project.ID] = project
	state.Agents["agent"] = &storedAgent{Agent: Agent{ID: "agent", ProjectID: project.ID, State: "idle", Queue: []QueuedMessage{}, Messages: []agent.Message{}, Cursor: uint64(len(events)), CreatedAt: now, UpdatedAt: now}, Events: events}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err = json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	record := legacy["agents"].(map[string]any)["agent"].(map[string]any)
	stored := record["agent"].(map[string]any)
	delete(stored, "last_response_cursor")
	delete(stored, "read_cursor")
	data, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	s, err := NewService(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := s.GetAgent("agent")
	if err != nil {
		t.Fatal(err)
	}
	if migrated.LastResponseCursor != 3 || migrated.ReadCursor != 3 {
		t.Fatalf("migration did not begin old chat read: %+v", migrated)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	data, err = os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted diskState
	if err = json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Version != storeVersion {
		t.Fatalf("migration version was not persisted: %d", persisted.Version)
	}
	persisted.Agents["agent"].Agent.LastResponseCursor = 5
	persisted.Agents["agent"].Agent.ReadCursor = 3
	persisted.Agents["agent"].Agent.Cursor = 5
	persisted.Agents["agent"].Events = append(persisted.Agents["agent"].Events, Event{AgentID: "agent", Cursor: 5, Type: "output", Data: json.RawMessage(`{"ResponseType":"agent"}`), CreatedAt: now})
	if err = saveState(dir, persisted); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	unread, err := restarted.GetAgent("agent")
	if err != nil {
		t.Fatal(err)
	}
	if unread.LastResponseCursor != 5 || unread.ReadCursor != 3 {
		t.Fatalf("restart repeated migration over unread state: %+v", unread)
	}
}

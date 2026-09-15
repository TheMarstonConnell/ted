package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

func jsonCloneForTest[T any](value T) T {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var cloned T
	if err := json.Unmarshal(raw, &cloned); err != nil {
		panic(err)
	}
	return cloned
}

func rawContent(t testing.TB, raw string) agent.Content {
	t.Helper()
	var content agent.Content
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	return content
}

func cloningFixture(t testing.TB) diskState {
	t.Helper()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 600, time.UTC)
	message := agent.Message{
		SourceModel: "provider/model",
		Role:        "assistant",
		Content:     rawContent(t, ` [ { "type": "text", "text": "original" } ] `),
		Reasoning:   "chain",
		ReasoningDetails: agent.ReasoningDetails{
			json.RawMessage(` { "type": "reasoning", "data": "<opaque>" } `),
			nil,
		},
		ToolCalls: []agent.ToolCall{{
			ToolCallType: "function",
			Index:        2,
			Id:           "call",
			Function: agent.FunctionCall{
				Name:      "bash",
				Arguments: `{"command":"pwd"}`,
			},
		}},
		ToolCallId: "parent-call",
	}
	state := diskState{
		Version: storeVersion,
		Projects: map[string]Project{
			"project": {
				WorkspaceDefaults: WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"},
				GitBranch:         "main",
				ID:                "project",
				Name:              "Clone fixture",
				Root:              "/tmp/project",
				Defaults:          Settings{Model: "provider/model", Effort: "high"},
			},
		},
		Agents: map[string]*storedAgent{
			"agent": {
				Agent: Agent{
					ParentAgentID: "parent",
					Workspace: Workspace{
						Shared:             true,
						WorkspaceSelection: WorkspaceSelection{Mode: "current_checkout"},
						Locked:             true,
						Status:             "ready",
						Path:               "/tmp/project",
					},
					ID:             "agent",
					ProjectID:      "project",
					Title:          "Clone me",
					Settings:       Settings{Model: "provider/model", Effort: "high"},
					ActiveSettings: &Settings{Model: "active/model", Effort: "low"},
					State:          "running",
					Queue: []QueuedMessage{{
						ID: "queued", Text: "do work", Status: "running", CreatedAt: now,
					}},
					Messages: []agent.Message{message},
					// This field is intentionally populated to verify json:"-" behavior.
					Events:             []Event{{AgentID: "nested-must-disappear", Data: json.RawMessage(`true`)}},
					Cursor:             2,
					LastResponseCursor: 1,
					CreatedAt:          now,
					UpdatedAt:          now.Add(time.Second),
				},
				Events: []Event{
					{AgentID: "agent", Cursor: 1, Type: "output", Data: json.RawMessage(` { "html": "<tag>" } `), CreatedAt: now},
					{AgentID: "agent", Cursor: 2, Type: "empty", Data: nil, CreatedAt: now.Add(time.Second)},
				},
				ManifestOffset: 42,
			},
			"nil": nil,
		},
		Receipts: map[string]receipt{
			"request": {Fingerprint: "fingerprint", AgentID: "agent", MessageID: "queued"},
		},
	}
	return state
}

func assertJSONCloneEquivalent[T cloneable](t *testing.T, value T) {
	t.Helper()
	got := cloneSnapshot(value)
	want := jsonCloneForTest(value)
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("typed clone differs from JSON round trip\ngot: %s\nwant: %s", gotJSON, wantJSON)
	}
}

func TestCloneSnapshotPreservesJSONSemantics(t *testing.T) {
	state := cloningFixture(t)
	assertJSONCloneEquivalent(t, state)
	assertJSONCloneEquivalent(t, state.Agents["agent"].Agent)
	assertJSONCloneEquivalent(t, state.Agents["agent"].Agent.Messages)
	assertJSONCloneEquivalent(t, state.Agents["agent"].Events)

	if got := cloneSnapshot(state.Agents["agent"].Agent).Events; got != nil {
		t.Fatalf("Agent.Events survived json omission: %#v", got)
	}
	// Raw bytes may retain their internal whitespace; encoding/json still emits
	// exactly the same canonical wire representation (checked above).
	if got := cloneSnapshot(state.Agents["agent"].Events)[0].Data; !json.Valid(got) {
		t.Fatalf("cloned event data is invalid JSON: %q", got)
	}
	if got := string(cloneSnapshot(state.Agents["agent"].Events)[1].Data); got != "null" {
		t.Fatalf("nil event data = %q, want JSON null", got)
	}
}

func TestCloneSnapshotOmitsPrivateMessageProvenance(t *testing.T) {
	runtime := agent.NewAgent(nil, nil)
	if err := runtime.RestoreConversation([]agent.Message{{
		SourceModel: "provider/model",
		Role:        "system",
		Content:     agent.TextContent("system"),
	}}); err != nil {
		t.Fatal(err)
	}
	messages := runtime.Messages()
	// Messages exposes SourceModel while retaining private sourceModel for its
	// own runtime. Snapshot cloning must retain only the exported durable field,
	// exactly as the former JSON round trip did.
	got := cloneSnapshot(messages)
	want := jsonCloneForTest(messages)

	if !reflect.DeepEqual(got, want) {
		t.Fatal("snapshot retained private message provenance")
	}
}

func TestCloneSnapshotIsolation(t *testing.T) {
	original := cloningFixture(t)
	cloned := cloneSnapshot(original)

	cloned.Projects["project"] = Project{ID: "replacement"}
	cloned.Receipts["request"] = receipt{Fingerprint: "replacement"}
	delete(cloned.Agents, "nil")
	stored := cloned.Agents["agent"]
	stored.ManifestOffset = 99
	stored.Agent.ActiveSettings.Model = "changed-model"
	stored.Agent.Queue[0].Text = "changed queue"
	stored.Agent.Messages[0].ToolCalls[0].Function.Arguments = "changed arguments"
	stored.Agent.Messages[0].ReasoningDetails[0][0] = '['
	stored.Events[0].Data[0] = '['

	// Content's MarshalJSON exposes its opaque raw bytes. Mutating those bytes
	// is a strong regression check that the private backing array was detached.
	contentRaw, err := stored.Agent.Messages[0].Content.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	contentRaw[0] = '{'

	source := original.Agents["agent"]
	if original.Projects["project"].ID != "project" || original.Receipts["request"].Fingerprint != "fingerprint" {
		t.Fatal("clone aliases a top-level map")
	}
	if _, ok := original.Agents["nil"]; !ok {
		t.Fatal("clone aliases the agents map")
	}
	if source.ManifestOffset != 42 || source.Agent.ActiveSettings.Model != "active/model" || source.Agent.Queue[0].Text != "do work" {
		t.Fatal("clone aliases stored agent fields")
	}
	if source.Agent.Messages[0].ToolCalls[0].Function.Arguments != `{"command":"pwd"}` {
		t.Fatal("clone aliases tool calls")
	}
	if source.Agent.Messages[0].ReasoningDetails[0][0] != ' ' {
		t.Fatal("clone aliases reasoning raw JSON")
	}
	if source.Events[0].Data[0] != ' ' {
		t.Fatal("clone aliases event raw JSON")
	}
	sourceContent, err := source.Agent.Messages[0].Content.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if sourceContent[0] != '[' {
		t.Fatalf("clone aliases content raw JSON: %q", sourceContent)
	}

	// Independently cloning repeated pointers is also JSON round-trip behavior.
	aliased := original.Agents["agent"]
	original.Agents["alias"] = aliased
	separate := cloneSnapshot(original)
	separate.Agents["agent"].Agent.Title = "only one"
	if separate.Agents["alias"].Agent.Title == "only one" {
		t.Fatal("repeated storedAgent pointers remain aliased")
	}
}

func TestCloneSnapshotNilAndEmptySlices(t *testing.T) {
	assertJSONCloneEquivalent(t, diskState{})
	assertJSONCloneEquivalent(t, Agent{})
	assertJSONCloneEquivalent(t, []agent.Message(nil))
	assertJSONCloneEquivalent(t, []Event(nil))

	emptyState := diskState{
		Projects: map[string]Project{},
		Agents:   map[string]*storedAgent{},
		Receipts: map[string]receipt{},
	}
	emptyAgent := Agent{
		Queue:    []QueuedMessage{},
		Messages: []agent.Message{},
		Events:   []Event{},
	}
	emptyMessageFields := []agent.Message{{
		Content:          agent.TextContent(""),
		ReasoningDetails: agent.ReasoningDetails{},
		ToolCalls:        []agent.ToolCall{},
	}}
	assertJSONCloneEquivalent(t, emptyState)
	assertJSONCloneEquivalent(t, emptyAgent)
	assertJSONCloneEquivalent(t, []agent.Message{})
	assertJSONCloneEquivalent(t, []Event{})
	assertJSONCloneEquivalent(t, emptyMessageFields)

	clonedState := cloneSnapshot(emptyState)
	if clonedState.Projects == nil || clonedState.Agents == nil || clonedState.Receipts == nil {
		t.Fatal("non-nil empty maps became nil")
	}
	clonedAgent := cloneSnapshot(emptyAgent)
	if clonedAgent.Queue == nil || clonedAgent.Messages == nil {
		t.Fatal("non-nil, non-omitempty agent slices became nil")
	}
	if clonedAgent.Events != nil {
		t.Fatal("omitted Agent.Events did not become nil")
	}
	clonedMessages := cloneSnapshot(emptyMessageFields)
	if clonedMessages[0].ReasoningDetails != nil || clonedMessages[0].ToolCalls != nil {
		t.Fatal("empty omitempty message slices did not become nil")
	}
}

func TestCloneSnapshotRejectsMalformedRawJSON(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("malformed raw JSON did not panic like json.Marshal")
		}
	}()
	cloneSnapshot([]Event{{Data: json.RawMessage(`{"unterminated":`)}})
}

func benchmarkState() diskState {
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	state := diskState{
		Version:  storeVersion,
		Projects: make(map[string]Project, 8),
		Agents:   make(map[string]*storedAgent, 8),
		Receipts: make(map[string]receipt, 32),
	}
	for i := 0; i < 8; i++ {
		projectID := fmt.Sprintf("project-%02d", i)
		agentID := fmt.Sprintf("agent-%02d", i)
		state.Projects[projectID] = Project{ID: projectID, Name: "A representative durable project", Root: "/tmp/project", Defaults: Settings{Model: "provider/model", Effort: "high"}}
		stored := &storedAgent{Agent: Agent{
			ID: agentID, ProjectID: projectID, Title: "A representative agent",
			Settings:       Settings{Model: "provider/model", Effort: "high"},
			ActiveSettings: &Settings{Model: "provider/model", Effort: "high"},
			State:          "running", Queue: make([]QueuedMessage, 12), Messages: make([]agent.Message, 48),
			CreatedAt: now, UpdatedAt: now,
		}, Events: make([]Event, 128), ManifestOffset: int64(i * 100)}
		for j := range stored.Agent.Queue {
			stored.Agent.Queue[j] = QueuedMessage{ID: fmt.Sprintf("queue-%d", j), Text: "inspect and update the implementation", Status: "completed", CreatedAt: now}
		}
		for j := range stored.Agent.Messages {
			stored.Agent.Messages[j] = agent.Message{
				SourceModel: "provider/model", Role: "assistant", Content: agent.TextContent("A moderately sized response used to represent conversation history."),
				Reasoning: "brief reasoning", ReasoningDetails: agent.ReasoningDetails{json.RawMessage(`{"type":"reasoning.encrypted","data":"opaque-provider-payload"}`)},
				ToolCalls: []agent.ToolCall{{ToolCallType: "function", Id: fmt.Sprintf("call-%d", j), Function: agent.FunctionCall{Name: "bash", Arguments: `{"command":"go test ./controlplane"}`}}},
			}
		}
		for j := range stored.Events {
			stored.Events[j] = Event{AgentID: agentID, Cursor: uint64(j + 1), Type: "output", Data: json.RawMessage(`{"ResponseType":"agent","Message":"representative durable event payload"}`), CreatedAt: now}
		}
		stored.Agent.Cursor = uint64(len(stored.Events))
		state.Agents[agentID] = stored
		for j := 0; j < 4; j++ {
			key := fmt.Sprintf("receipt-%d-%d", i, j)
			state.Receipts[key] = receipt{Fingerprint: "abcdef0123456789", AgentID: agentID, MessageID: fmt.Sprintf("queue-%d", j)}
		}
	}
	return state
}

var benchmarkDiskStateSink diskState
var benchmarkAgentSink Agent
var benchmarkMessagesSink []agent.Message
var benchmarkEventsSink []Event

func BenchmarkCloneDiskState(b *testing.B) {
	state := benchmarkState()
	b.Run("typed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkDiskStateSink = cloneSnapshot(state)
		}
	})
	b.Run("json", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkDiskStateSink = jsonCloneForTest(state)
		}
	})
}

func BenchmarkCloneAgent(b *testing.B) {
	value := benchmarkState().Agents["agent-00"].Agent
	b.Run("typed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkAgentSink = cloneSnapshot(value)
		}
	})
	b.Run("json", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkAgentSink = jsonCloneForTest(value)
		}
	})
}

func BenchmarkCloneMessages(b *testing.B) {
	value := benchmarkState().Agents["agent-00"].Agent.Messages
	b.Run("typed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkMessagesSink = cloneSnapshot(value)
		}
	})
	b.Run("json", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkMessagesSink = jsonCloneForTest(value)
		}
	})
}

func BenchmarkCloneEvents(b *testing.B) {
	value := benchmarkState().Agents["agent-00"].Events
	b.Run("typed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkEventsSink = cloneSnapshot(value)
		}
	})
	b.Run("json", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchmarkEventsSink = jsonCloneForTest(value)
		}
	})
}

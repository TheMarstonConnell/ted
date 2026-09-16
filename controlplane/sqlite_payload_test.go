package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestSQLiteRejectsInvalidLegacyIdentityBeforeChangingJSON(t *testing.T) {
	for _, kind := range []string{"project identity", "parent cycle"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			state := emptyState()
			state.Projects["p"] = Project{ID: "p", Root: t.TempDir()}
			if kind == "project identity" {
				p := state.Projects["p"]
				p.ID = "different"
				state.Projects["p"] = p
			} else {
				state.Agents["a"] = &storedAgent{Agent: Agent{ID: "a", ProjectID: "p", ParentAgentID: "b"}}
				state.Agents["b"] = &storedAgent{Agent: Agent{ID: "b", ProjectID: "p", ParentAgentID: "a"}}
			}
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "state.json")
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if s, err := NewService(dir, nil, nil); err == nil {
				s.Close(context.Background())
				t.Fatal("invalid legacy state migrated")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(data) {
				t.Fatalf("failed migration replaced source JSON: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, sqliteBackupName)); !os.IsNotExist(err) {
				t.Fatalf("invalid source was archived/activated: %v", err)
			}
		})
	}
}

func TestSQLiteOpaquePayloadMigrationAndIncrementalPersistence(t *testing.T) {
	dir := t.TempDir()
	state := emptyState()
	state.Projects["p"] = Project{ID: "p", Root: t.TempDir()}
	now := time.Now().UTC()
	messages := []agent.Message{
		{Role: "system", Content: agent.TextContent("system")},
		{Role: "user", Content: rawContent(t, `null`)},
		{Role: "assistant", SourceModel: "provider/model", Content: rawContent(t, `[{"type":"text","text":"<opaque>"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA==","detail":"auto"}}]`),
			ReasoningDetails: agent.ReasoningDetails{json.RawMessage(`{"type":"reasoning.encrypted","data":"ciphertext","signature":"opaque-signature","future_field":{"number":9007199254740993}}`)},
			ToolCalls:        []agent.ToolCall{{Id: "tool", ToolCallType: "function", Function: agent.FunctionCall{Name: "bash", Arguments: `{"command":"echo \"unchanged\""}`}}}},
		{Role: "tool", ToolCallId: "tool", Content: agent.TextContent("result")},
	}
	record := &storedAgent{Agent: Agent{ID: "a", ProjectID: "p", State: "idle", Held: true, Messages: messages, Queue: []QueuedMessage{}, Cursor: 1, CreatedAt: now, UpdatedAt: now}, ManifestOffset: 712,
		Events: []Event{{AgentID: "a", Cursor: 1, Type: "output", Data: json.RawMessage(`{"number":9007199254740993,"nested":[null,{"text":"<value>"}]}`), CreatedAt: now}}}
	state.Agents["a"] = record
	if err := saveState(dir, state); err != nil {
		t.Fatal(err)
	}
	s, err := NewService(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	if _, err := s.ReadAgent("a", 1); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored := readClosedSQLiteState(t, dir).Agents["a"]
	wantJSON, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(restored.Agent.Messages)
	if err != nil {
		t.Fatal(err)
	}
	wantEvent, err := json.Marshal(record.Events[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) || restored.ManifestOffset != record.ManifestOffset || string(restored.Events[0].Data) != string(wantEvent) {
		t.Fatal("migration or metadata mutation changed opaque history")
	}
	if restored.Agent.ReadCursor != 1 || restored.Agent.Cursor != 2 {
		t.Fatal("incremental update was not durable")
	}
}

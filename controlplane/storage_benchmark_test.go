package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

// This benchmark file also builds on the JSON baseline; NewService selects the backend.
func storageBenchmarkService(b *testing.B, agents, history int) (*Service, int) {
	b.Helper()
	state := emptyState()
	root := b.TempDir()
	state.Projects["project"] = Project{ID: "project", Name: "benchmark", Root: root, Defaults: Settings{Model: "benchmark/model", Effort: "low"}, WorkspaceDefaults: WorkspaceSelection{Mode: "current_checkout"}}
	text := strings.Repeat("payload ", 64)
	for i := range agents {
		id := fmt.Sprintf("agent-%06d", i)
		now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second)
		record := &storedAgent{Agent: Agent{ID: id, ProjectID: "project", Title: id, Settings: Settings{Model: "benchmark/model", Effort: "low"}, State: "idle", Held: true, Settled: i%2 == 1, Workspace: Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "current_checkout"}, Status: "ready", Locked: true, Path: root}, Queue: []QueuedMessage{}, Messages: []agent.Message{}, CreatedAt: now, UpdatedAt: now}}
		for j := range history {
			role := "assistant"
			if j == 0 {
				role = "system"
			} else if j%2 == 1 {
				role = "user"
			}
			record.Agent.Messages = append(record.Agent.Messages, agent.Message{Role: role, Content: agent.TextContent(text), SourceModel: "benchmark/model"})
			data, err := json.Marshal(map[string]any{"ResponseType": "agent", "Content": text})
			if err != nil {
				b.Fatal(err)
			}
			record.Events = append(record.Events, Event{AgentID: id, Cursor: uint64(j + 1), Type: "output", Data: data, CreatedAt: now})
			record.Agent.Queue = append(record.Agent.Queue, QueuedMessage{ID: fmt.Sprintf("queue-%d", j), Status: "completed", Text: "completed work", CreatedAt: now})
		}
		record.Agent.Cursor = uint64(len(record.Events))
		record.Agent.LastResponseCursor = record.Agent.Cursor
		state.Agents[id] = record
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	if err := saveState(dir, state); err != nil {
		b.Fatal(err)
	}
	s, err := NewService(dir, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			b.Error(err)
		}
	})
	return s, len(encoded)
}

func BenchmarkStorageReadCursor(b *testing.B) {
	for _, size := range []struct {
		name            string
		agents, history int
	}{
		{"10_agents_20_history", 10, 20},
		{"1000_agents_20_history", 1000, 20},
		{"1_agent_2000_history", 1, 2000},
	} {
		b.Run(size.name, func(b *testing.B) {
			s, seedBytes := storageBenchmarkService(b, size.agents, size.history)
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(seedBytes), "seed_B")
			for range b.N {
				a := s.state.Agents["agent-000000"]
				if _, err := s.ReadAgent(a.Agent.ID, a.Agent.Cursor); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkStorageAgentPage(b *testing.B) {
	for _, count := range []int{10, 1000} {
		b.Run(fmt.Sprintf("%d_agents", count), func(b *testing.B) {
			s, seedBytes := storageBenchmarkService(b, count, 20)
			h := NewHandler(s)
			req := httptest.NewRequest("GET", "/v1/agents?project_id=project&page=1&page_size=10", nil)
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(seedBytes), "seed_B")
			for range b.N {
				recorder := httptest.NewRecorder()
				h.ServeHTTP(recorder, req)
				if recorder.Code != 200 {
					b.Fatalf("%d: %s", recorder.Code, recorder.Body.String())
				}
			}
		})
	}
}

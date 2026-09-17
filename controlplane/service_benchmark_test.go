package controlplane

import (
	"fmt"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func BenchmarkParallelAgentRead(b *testing.B) {
	s := &Service{state: emptyState()}
	a := Agent{ID: "a", Messages: make([]agent.Message, 100)}
	for i := range a.Messages {
		a.Messages[i] = agent.Message{Role: "user", Content: agent.TextContent("a representative transcript message")}
	}
	s.state.Agents[a.ID] = &storedAgent{Agent: a}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := s.GetAgent("a"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkSettleTree(b *testing.B) {
	s := &Service{state: emptyState(), changed: make(chan struct{}), save: func(string, diskState) error { return nil }}
	for i := range 1000 {
		id := fmt.Sprint(i)
		parent := ""
		if i > 0 {
			parent = fmt.Sprint((i - 1) / 2)
		}
		s.state.Agents[id] = &storedAgent{Agent: Agent{ID: id, ParentAgentID: parent, State: "idle"}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		for _, a := range s.state.Agents {
			a.Agent.Settled = false
			a.Agent.Held = false
			a.Agent.Cursor = 0
			a.Events = nil
		}
		b.StartTimer()
		if _, err := s.SetSettled("0", true); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWebSocketInventorySnapshot(b *testing.B) {
	s := &Service{state: emptyState(), changed: make(chan struct{})}
	for i := range 10 {
		id := fmt.Sprint(i)
		a := Agent{ID: id, Messages: make([]agent.Message, 100)}
		for j := range a.Messages {
			a.Messages[j] = agent.Message{Role: "user", Content: agent.TextContent("a representative transcript message")}
		}
		s.state.Agents[id] = &storedAgent{Agent: a}
	}
	h := &httpAPI{service: s}
	sub := &wsSubscription{all: true}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, _, _, err := h.snapshotWS(sub); err != nil {
			b.Fatal(err)
		}
	}
}

package controlplane

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

type delayedCancellationProvider struct{ started, release chan struct{} }

func (*delayedCancellationProvider) Name() string { return "delayed" }
func (*delayedCancellationProvider) ListModels() []agent.ModelInfo {
	return []agent.ModelInfo{{ID: "one"}}
}
func (p *delayedCancellationProvider) Complete(_ *zap.Logger, r agent.CompletionRequest) (*agent.Response, error) {
	close(p.started)
	<-r.RequestContext().Done()
	<-p.release
	return nil, r.RequestContext().Err()
}
func TestAllSubscriptionDeliversFinalSettledCancellation(t *testing.T) {
	p := &delayedCancellationProvider{make(chan struct{}), make(chan struct{})}
	s, err := NewService(t.TempDir(), nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	srv := httptest.NewServer(NewHandler(s))
	defer srv.Close()
	project, err := s.CreateProject(CreateProjectRequest{Name: "test", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err = ws.WriteJSON(map[string]any{"type": "subscribe", "request_id": "all", "subscribe_all": true}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Submit(a.ID, "work", ""); err != nil {
		t.Fatal(err)
	}
	<-p.started
	if _, err = s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			close(p.release)
		}
	}()
	for {
		ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		var frame struct {
			Type  string `json:"type"`
			Agent Agent  `json:"agent"`
			Event Event  `json:"event"`
		}
		if err = ws.ReadJSON(&frame); err != nil {
			t.Fatalf("missing final cancellation event: %v", err)
		}
		if !released && frame.Type == "inventory" && frame.Agent.Settled && frame.Agent.State == "stopping" {
			// Force completion after the settled/stopping inventory was delivered.
			time.Sleep(20 * time.Millisecond)
			close(p.release)
			released = true
		}
		if frame.Type == "event" && frame.Event.Type == "turn.cancelled" {
			var m QueuedMessage
			if err = json.Unmarshal(frame.Event.Data, &m); err != nil {
				t.Fatal(err)
			}
			if m.Status != "cancelled" {
				t.Fatal(m)
			}
			return
		}
	}
}

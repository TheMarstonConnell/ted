package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/gorilla/websocket"
)

func TestSettingsAndQueuedSubmission(t *testing.T) {
	var submissions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`[{"id":"test/model","name":"Model","provider":"test","context_window":1234,"efforts":["low","high"],"default_effort":"low"}]`))
		case "/v1/agents/a/settings":
			if r.Method != "PATCH" {
				t.Error(r.Method)
			}
			var patch map[string]string
			_ = json.NewDecoder(r.Body).Decode(&patch)
			if patch["effort"] != "high" {
				t.Error(patch)
			}
			_ = json.NewEncoder(w).Encode(Snapshot{ID: "a", State: "running", Settings: Settings{Model: "test/model", Effort: "high"}, Cursor: 2})
		case "/v1/agents/a/messages":
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("missing deduplication key")
			}
			submissions.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"q","text":"hello","status":"pending"}`))
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	c := New(server.URL)
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ContextWindow != 1234 || models[0].DefaultEffort != "low" {
		t.Fatal(models, err)
	}
	a := NewAgent(context.Background(), c, Snapshot{ID: "a", State: "running", Settings: Settings{Model: "test/model", Effort: "low"}}, "/remote/root", models)
	change, err := a.SetEffort(agent.EffortHigh)
	if err != nil || change.After.Effort != agent.EffortHigh || a.Ready() {
		t.Fatal(change, err)
	}
	if err := a.Turn("hello"); err != nil {
		t.Fatal(err)
	}
	if submissions.Load() != 1 {
		t.Fatal("submission duplicated")
	}
	if a.WorkingDir() != "/remote/root" || !a.AcceptsQueuedInput() {
		t.Fatal("wrong client semantics")
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"settled","message":"restore first"}}`))
	}))
	defer server.Close()
	_, err := New(server.URL).GetAgent(context.Background(), "a")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "settled" {
		t.Fatal(err)
	}
}

func TestWebSocketReplayInventoryDoesNotSkipEventsAndFullOutput(t *testing.T) {
	var connections atomic.Int32
	full := strings.Repeat("full tool output\n", 10000)
	output, _ := json.Marshal(agent.AgentResponse{ResponseType: "tool_result", Content: "capped", FullToolOutput: full})
	events := []Event{{AgentID: "a", Cursor: 2, Type: "output", Data: json.RawMessage(`{"ResponseType":"assistant","Content":"first"}`)}, {AgentID: "a", Cursor: 3, Type: "output", Data: output}}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/agents/a/events/3" {
			_ = json.NewEncoder(w).Encode(events[1])
			return
		}
		if r.URL.Path != "/v1/ws" {
			t.Errorf("unexpected %s", r.URL)
			w.WriteHeader(404)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var sub struct {
			Type    string            `json:"type"`
			IDs     []string          `json:"agent_ids"`
			Cursors map[string]uint64 `json:"cursors"`
		}
		if err := conn.ReadJSON(&sub); err != nil {
			t.Error(err)
			return
		}
		if sub.Type != "subscribe" || len(sub.IDs) != 1 || sub.IDs[0] != "a" {
			t.Error(sub)
		}
		n := connections.Add(1)
		_ = conn.WriteJSON(map[string]any{"type": "subscribed", "request_id": "r"})
		_ = conn.WriteJSON(map[string]any{"type": "inventory", "agent": Snapshot{ID: "a", Cursor: 99, State: "idle"}})
		if n == 1 {
			if sub.Cursors["a"] != 1 {
				t.Errorf("initial cursor %d", sub.Cursors["a"])
			}
			_ = conn.WriteJSON(map[string]any{"type": "event", "event": events[0]})
			return // reconnect must ask after consumed 2, NOT inventory's 99
		}
		if sub.Cursors["a"] != 2 {
			t.Errorf("reconnect cursor %d", sub.Cursors["a"])
		}
		_ = conn.WriteJSON(map[string]any{"type": "event", "event": events[0]}) // duplicate replay ignored
		_ = conn.WriteJSON(map[string]any{"type": "event_ref", "agent_id": "a", "cursor": 3, "url": "https://untrusted.invalid/output"})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewAgent(ctx, New(server.URL), Snapshot{ID: "a", Cursor: 1, State: "idle"}, "/server", nil)
	updates := make(chan Update, 20)
	if err := a.Subscribe(ctx, func(u Update) { updates <- u }); err != nil {
		t.Fatal(err)
	}
	var outputs []agent.AgentResponse
	timeout := time.After(5 * time.Second)
	for len(outputs) < 2 {
		select {
		case u := <-updates:
			if u.Output != nil {
				outputs = append(outputs, *u.Output)
			}
		case <-timeout:
			t.Fatal("timed out waiting for replay", outputs)
		}
	}
	if outputs[0].Content != "first" || outputs[1].FullToolOutput != full {
		t.Fatal("lost or duplicated output")
	}
	cancel()
}

func TestGitBranchComesFromServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/p" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"p","git_branch":"server-branch"}`))
	}))
	defer server.Close()
	a := NewAgent(context.Background(), New(server.URL), Snapshot{ProjectID: "p"}, "/nonexistent/remote/root", nil)
	if branch, err := a.GitBranch(); err != nil || branch != "server-branch" {
		t.Fatal(branch, err)
	}
}

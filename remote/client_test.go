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

func TestDeletedAgentStopsSubscription(t *testing.T) {
	for _, mode := range []string{"live", "reconnect", "initial"} {
		t.Run(mode, func(t *testing.T) {
			var connections atomic.Int32
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/agents/a" {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"agent deleted"}}`))
					return
				}
				if r.URL.Path != "/v1/ws" {
					t.Errorf("unexpected request: %s", r.URL)
					w.WriteHeader(404)
					return
				}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				var sub any
				if err := conn.ReadJSON(&sub); err != nil {
					return
				}
				n := connections.Add(1)
				if mode == "initial" || (mode == "reconnect" && n > 1) {
					_ = conn.WriteJSON(map[string]any{"type": "error", "code": "cursor_invalid", "message": "unknown agent"})
					return
				}
				_ = conn.WriteJSON(map[string]any{"type": "subscribed"})
				if mode == "live" {
					_ = conn.WriteJSON(map[string]any{"type": "error", "code": "cursor_invalid", "message": "agent deleted"})
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := NewAgent(ctx, New(server.URL), Snapshot{ID: "a", State: "idle", Cursor: 5}, "/server", nil)
			updates := make(chan Update, 10)
			err := a.Subscribe(ctx, func(u Update) { updates <- u })
			if mode == "initial" {
				if !errors.Is(err, ErrAgentDeleted) {
					t.Fatalf("initial subscription: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.After(5 * time.Second)
			wait:
				for {
					select {
					case u := <-updates:
						if errors.Is(u.Err, ErrAgentDeleted) {
							break wait
						}
					case <-deadline:
						t.Fatal("no terminal deletion notification")
					}
				}
			}
			count := connections.Load()
			time.Sleep(1200 * time.Millisecond)
			if connections.Load() != count {
				t.Fatal("continued reconnecting after deletion")
			}
		})
	}
}

func TestAgentsPageRequestMetadataAndValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents" || r.URL.Query().Get("include_settled") != "true" ||
			r.URL.Query().Get("page") != "2" || r.URL.Query().Get("page_size") != "3" ||
			r.URL.Query().Get("project_id") != "project with spaces" {
			t.Errorf("unexpected paged request: %s", r.URL.String())
		}
		w.Header().Set("X-Total-Count", "7")
		_ = json.NewEncoder(w).Encode([]Snapshot{{ID: "a"}, {ID: "b"}, {ID: "c"}})
	}))
	defer server.Close()

	result, total, err := New(server.URL).AgentsPage(context.Background(), "project with spaces", 2, 3)
	if err != nil || total != 7 || len(result) != 3 || result[0].ID != "a" {
		t.Fatalf("paged result: %+v total=%d err=%v", result, total, err)
	}
}

func TestAgentsPageRejectsMissingOrInvalidMetadata(t *testing.T) {
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Snapshot{{ID: "must not be accepted"}})
	}))
	defer missing.Close()
	result, total, err := New(missing.URL).AgentsPage(context.Background(), "", 1, 1)
	if err == nil || !strings.Contains(err.Error(), "X-Total-Count") || result != nil || total != 0 {
		t.Fatalf("missing metadata: result=%+v total=%d err=%v", result, total, err)
	}

	invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "not-a-count")
		_, _ = w.Write([]byte("[]"))
	}))
	defer invalid.Close()
	result, total, err = New(invalid.URL).AgentsPage(context.Background(), "", 1, 1)
	if err == nil || !strings.Contains(err.Error(), "invalid X-Total-Count") || result != nil || total != 0 {
		t.Fatalf("invalid metadata: result=%+v total=%d err=%v", result, total, err)
	}

	var requests atomic.Int32
	validation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	defer validation.Close()
	c := New(validation.URL)
	for _, tc := range [][2]int{{0, 1}, {1, 0}, {-1, 1}} {
		if _, _, err := c.AgentsPage(context.Background(), "", tc[0], tc[1]); err == nil {
			t.Errorf("accepted invalid pagination %#v", tc)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("validation made %d requests", requests.Load())
	}
}

func TestAgentsPageLegacyRejectionGuidance(t *testing.T) {
	for _, tc := range []struct {
		name, code, message string
		status              int
		guidance            bool
	}{
		{"legacy page", "invalid", "unknown or repeated query parameter: page", 400, true},
		{"legacy page size", "invalid", "unknown or repeated query parameter: page_size", 400, true},
		{"other parameter", "invalid", "unknown or repeated query parameter: project_id", 400, false},
		{"invalid project", "invalid_project", "invalid project", 400, false},
		{"unavailable", "shutting_down", "server shutting down", 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !r.URL.Query().Has("page") {
					_ = json.NewEncoder(w).Encode([]Snapshot{{ID: "legacy"}})
					return
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": APIError{Code: tc.code, Message: tc.message}})
			}))
			defer server.Close()
			client := New(server.URL)
			rows, total, err := client.AgentsPage(context.Background(), "", 1, 25)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.Code != tc.code || apiErr.Message != tc.message || rows != nil || total != 0 {
				t.Fatalf("rows=%v total=%d error=%v", rows, total, err)
			}
			for _, guidance := range []string{"upgrade the server", "sessions --all"} {
				if strings.Contains(err.Error(), guidance) != tc.guidance {
					t.Fatalf("guidance %q: %v", guidance, err)
				}
			}
			if tc.guidance {
				rows, err = client.Agents(context.Background(), "")
				if err != nil || len(rows) != 1 || rows[0].ID != "legacy" {
					t.Fatalf("legacy unpaged request: %v, %v", rows, err)
				}
			}
		})
	}
}

package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/api"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

type wsTestFrame struct {
	Type      string           `json:"type"`
	RequestID string           `json:"request_id"`
	Agent     api.AgentSummary `json:"agent"`
	Event     Event            `json:"event"`
	AgentID   string           `json:"agent_id"`
	Cursor    uint64           `json:"cursor"`
	URL       string           `json:"url"`
	MessageID string           `json:"message_id"`
	Status    string           `json:"status"`
	Code      string           `json:"code"`
}

func dialHTTPWS(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	c, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, res)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func sendHTTPWS(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	if err := c.WriteJSON(v); err != nil {
		t.Fatal(err)
	}
}
func readHTTPWS(t *testing.T, c *websocket.Conn) wsTestFrame {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, data, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.TextMessage || len(data) > maxWSFrame {
		t.Fatalf("invalid frame kind/size: %d %d", kind, len(data))
	}
	f := decodeHTTP[wsTestFrame](t, data)
	names := map[string]string{"subscribed": "WSSubscribed", "inventory": "WSInventory", "event": "WSEvent", "event_ref": "WSEventReference", "ack": "WSAck", "error": "WSError"}
	name, ok := names[f.Type]
	if !ok {
		t.Fatalf("unknown frame: %s", data)
	}
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	var raw any
	if err = json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if err = spec.Components.Schemas[name].Value.VisitJSON(raw); err != nil {
		t.Fatalf("WS frame violates schema: %v\n%s", err, data)
	}
	if f.Type == "inventory" {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(data, &raw)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw["agent"], &fields)
		if _, ok := fields["messages"]; ok {
			t.Fatal("inventory repeats conversation history")
		}
		if _, ok := fields["queue"]; ok {
			t.Fatal("inventory repeats queue history")
		}
	}
	return f
}
func subscribeHTTPWS(t *testing.T, c *websocket.Conn, all bool, ids []string, cursors map[string]uint64) {
	t.Helper()
	command := map[string]any{"type": "subscribe", "request_id": "sub", "subscribe_all": all}
	if ids != nil {
		command["agent_ids"] = ids
	}
	if cursors != nil {
		command["cursors"] = cursors
	}
	sendHTTPWS(t, c, command)
	f := readHTTPWS(t, c)
	if f.Type != "subscribed" || f.RequestID != "sub" {
		t.Fatalf("subscribe: %+v", f)
	}
}
func drainHTTPWS(t *testing.T, c *websocket.Conn, cursors map[string]uint64, targets map[string]uint64) []wsTestFrame {
	t.Helper()
	var frames []wsTestFrame
	for {
		complete := true
		for id, want := range targets {
			if cursors[id] < want {
				complete = false
				break
			}
		}
		if complete {
			return frames
		}
		f := readHTTPWS(t, c)
		frames = append(frames, f)
		var id string
		var cursor uint64
		switch f.Type {
		case "inventory":
			continue
		case "event":
			id, cursor = f.Event.AgentID, f.Event.Cursor
		case "event_ref":
			id, cursor = f.AgentID, f.Cursor
		default:
			t.Fatalf("unexpected replay frame: %+v", f)
		}
		if cursor != cursors[id]+1 {
			t.Fatalf("gap or duplicate: %s got %d after %d", id, cursor, cursors[id])
		}
		cursors[id] = cursor
	}
}

func TestHTTPWebSocketSubscribeAllFutureAndIncrementalReplay(t *testing.T) {
	f := newHTTPFixture(t, nil)
	p := f.project()
	a := f.agent(p)
	settled := f.agent(p)
	if _, err := f.s.SetSettled(settled.ID, true); err != nil {
		t.Fatal(err)
	}
	c := dialHTTPWS(t, f.server)
	subscribeHTTPWS(t, c, true, nil, nil)
	cursors := map[string]uint64{}
	frames := drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: a.Cursor})
	for _, frame := range frames {
		if frame.Agent.Id == settled.ID || frame.Event.AgentID == settled.ID {
			t.Fatal("all included initially settled agent")
		}
	}
	// New project and agent created after the atomic initial subscription.
	future := f.agent(f.project())
	drainHTTPWS(t, c, cursors, map[string]uint64{future.ID: future.Cursor})
	// Produce many updates while the subscriber is replaying; compare the exact
	// per-agent sequence with HTTP's lifetime log. No history reset per wake.
	for i := 0; i < 12; i++ {
		effort := "low"
		if i%2 == 0 {
			effort = "high"
		}
		if _, err := f.s.UpdateSettings(a.ID, SettingsPatch{Effort: &effort}); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := f.s.GetAgent(a.ID)
	drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: current.Cursor})
	final, err := f.s.SetSettled(a.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: final.Cursor})
	restored, err := f.s.SetSettled(a.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: restored.Cursor})
	c.Close()
	// Resume has no replay at or below saved cursors. Send a new change then
	// require exactly cursor+1 (inventory high-water is not consumed).
	c2 := dialHTTPWS(t, f.server)
	subscribeHTTPWS(t, c2, true, nil, cursors)
	effort := "high"
	updated, err := f.s.UpdateSettings(a.ID, SettingsPatch{Effort: &effort})
	if err != nil {
		t.Fatal(err)
	}
	drainHTTPWS(t, c2, cursors, map[string]uint64{a.ID: updated.Cursor})
}

func TestHTTPWebSocketExplicitUnionAndInvalidCursors(t *testing.T) {
	f := newHTTPFixture(t, nil)
	p := f.project()
	a := f.agent(p)
	b := f.agent(p)
	b, _ = f.s.SetSettled(b.ID, true)
	c := dialHTTPWS(t, f.server)
	subscribeHTTPWS(t, c, true, []string{b.ID}, nil)
	cursors := map[string]uint64{}
	drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: a.Cursor, b.ID: b.Cursor})
	cases := []struct {
		command any
		code    string
	}{
		{map[string]any{"type": "subscribe", "request_id": "bad", "agent_ids": []string{a.ID}, "cursors": map[string]uint64{a.ID: a.Cursor + 100}}, "cursor_invalid"},
		{map[string]any{"type": "subscribe", "request_id": "bad", "subscribe_all": true, "cursors": map[string]uint64{"missing": 0}}, "cursor_invalid"},
		{map[string]any{"type": "subscribe", "request_id": "bad", "agent_ids": []string{"missing"}}, "not_found"},
		{map[string]any{"type": "subscribe", "request_id": "bad", "cursors": map[string]uint64{a.ID: 0}}, "invalid"},
		{map[string]any{"type": "subscribe", "request_id": "bad", "agent_ids": []string{a.ID}, "cursors": map[string]int{a.ID: -1}}, "invalid"},
		{map[string]any{"type": "subscribe", "request_id": "bad", "agent_ids": []string{a.ID}, "cursors": map[string]float64{a.ID: 0.5}}, "invalid"},
		{map[string]any{"type": "subscribe", "request_id": "bad", "extra": true}, "invalid"},
		{map[string]any{"type": "subscribe"}, "invalid"},
		{map[string]any{"type": "unknown", "request_id": "bad"}, "invalid"},
		{map[string]any{"type": "submit", "request_id": "bad", "agent_id": a.ID, "text": "missing key"}, "invalid"},
		{map[string]any{"type": "submit", "request_id": "bad", "agent_id": a.ID, "idempotency_key": "x", "text": ""}, "invalid"},
	}
	for _, tc := range cases {
		sendHTTPWS(t, c, tc.command)
		got := readHTTPWS(t, c)
		if got.Type != "error" || got.Code != tc.code {
			t.Fatalf("%v: %+v", tc.command, got)
		}
	}
	// Every rejected command must leave the previous subscription intact.
	effort := "high"
	a, _ = f.s.UpdateSettings(a.ID, SettingsPatch{Effort: &effort})
	drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: a.Cursor})
}

func TestHTTPWebSocketSubmissionSharesHTTPIdempotency(t *testing.T) {
	started := make(chan struct{}, 1)
	p := &httpTestProvider{complete: func(r agent.CompletionRequest) (*agent.Response, error) {
		started <- struct{}{}
		<-r.RequestContext().Done()
		return nil, r.RequestContext().Err()
	}}
	f := newHTTPFixture(t, p)
	a := f.agent(f.project())
	c := dialHTTPWS(t, f.server)
	command := map[string]any{"type": "submit", "request_id": "send-1", "agent_id": a.ID, "idempotency_key": "shared-key", "text": "hello"}
	sendHTTPWS(t, c, command)
	ack := readHTTPWS(t, c)
	if ack.Type != "ack" || ack.RequestID != "send-1" || ack.MessageID == "" {
		t.Fatal(ack)
	}
	m := decodeHTTP[QueuedMessage](t, f.request("POST", "/v1/agents/"+a.ID+"/messages", `{"text":"hello"}`, "shared-key", 202))
	if m.ID != ack.MessageID {
		t.Fatal("HTTP and WS have separate idempotency namespaces")
	}
	command["request_id"] = "send-2"
	sendHTTPWS(t, c, command)
	again := readHTTPWS(t, c)
	if again.Type != "ack" || again.MessageID != ack.MessageID || again.RequestID != "send-2" {
		t.Fatal(again)
	}
	command["text"] = "changed"
	sendHTTPWS(t, c, command)
	conflict := readHTTPWS(t, c)
	if conflict.Type != "error" || conflict.Code != "idempotency_conflict" {
		t.Fatal(conflict)
	}
	a, _ = f.s.GetAgent(a.ID)
	if len(a.Queue) != 1 {
		t.Fatal(a.Queue)
	}
}

func TestHTTPWebSocketOversizedToolOutputReference(t *testing.T) {
	var calls atomic.Int32
	p := &httpTestProvider{complete: func(r agent.CompletionRequest) (*agent.Response, error) {
		if calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]string{"command": "printf '%090000d' 0"})
			return &agent.Response{Choices: []agent.Choice{{FinishReason: "tool_calls", Message: agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{Id: "large", ToolCallType: "function", Function: agent.FunctionCall{Name: "bash", Arguments: string(args)}}}}}}}, nil
		}
		return &agent.Response{Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("done")}}}}, nil
	}}
	f := newHTTPFixture(t, p)
	a := f.agent(f.project())
	if _, err := f.s.Submit(a.ID, "large output", "large"); err != nil {
		t.Fatal(err)
	}
	a = waitHTTPAgent(t, f.s, a.ID, func(a Agent) bool { return len(a.Queue) > 0 && a.Queue[0].Status == "completed" })
	c := dialHTTPWS(t, f.server)
	subscribeHTTPWS(t, c, false, []string{a.ID}, nil)
	frames := drainHTTPWS(t, c, map[string]uint64{}, map[string]uint64{a.ID: a.Cursor})
	found := false
	for _, frame := range frames {
		if frame.Type != "event_ref" {
			continue
		}
		event := decodeHTTP[Event](t, f.request("GET", frame.URL, "", "", 200))
		if event.Cursor != frame.Cursor || event.AgentID != a.ID {
			t.Fatal(event)
		}
		if event.Type == "output" {
			var out agent.AgentResponse
			if err := json.Unmarshal(event.Data, &out); err != nil {
				t.Fatal(err)
			}
			if out.ResponseType == "tool_result" {
				if len(out.FullToolOutput) < 90000 {
					t.Fatalf("truncated full output: %d", len(out.FullToolOutput))
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("large retained tool output was not delivered as an HTTP reference")
	}
}

func TestHTTPWebSocketLifetimeReplayAfterRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewService(dir, zap.NewNop(), []agent.Provider{&httpTestProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(CreateProjectRequest{Name: "restart", Root: t.TempDir(), Defaults: Settings{Model: "http-test/one", Effort: "low"}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewServer(NewHandler(s))
	c := dialHTTPWS(t, first)
	subscribeHTTPWS(t, c, false, []string{a.ID}, nil)
	cursors := map[string]uint64{}
	drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: a.Cursor})
	c.Close()
	first.Close()
	if _, err = s.Submit(a.ID, "persist", "persist"); err != nil {
		t.Fatal(err)
	}
	a = waitHTTPAgent(t, s, a.ID, func(a Agent) bool { return len(a.Queue) > 0 && a.Queue[0].Status == "completed" })
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err = NewService(dir, zap.NewNop(), []agent.Provider{&httpTestProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	second := httptest.NewServer(NewHandler(s))
	defer second.Close()
	c2 := dialHTTPWS(t, second)
	defer c2.Close()
	subscribeHTTPWS(t, c2, false, []string{a.ID}, cursors)
	drainHTTPWS(t, c2, cursors, map[string]uint64{a.ID: a.Cursor})
}

func TestHTTPWebSocketOriginAndFrameLimit(t *testing.T) {
	f := newHTTPFixture(t, nil)
	endpoint := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/v1/ws"
	for _, origin := range []string{"https://attacker.invalid", f.server.URL + ".evil", "null"} {
		headers := http.Header{"Origin": []string{origin}}
		c, res, err := websocket.DefaultDialer.Dial(endpoint, headers)
		if c != nil {
			c.Close()
		}
		if err == nil || res == nil || res.StatusCode != 403 {
			t.Fatalf("origin %q: %v %v", origin, res, err)
		}
		res.Body.Close()
	}
	c, res, err := websocket.DefaultDialer.Dial(endpoint, http.Header{"Origin": []string{f.server.URL}})
	if err != nil {
		t.Fatalf("same-origin: %v %v", err, res)
	}
	c.Close()
	c = dialHTTPWS(t, f.server)
	if err = c.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", maxWSFrame+1))); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = c.ReadMessage()
	var closed *websocket.CloseError
	if !errors.As(err, &closed) || closed.Code != websocket.CloseMessageTooBig {
		t.Fatalf("oversize: %v", err)
	}
}

func TestHTTPWebSocketSlowWriterDeadline(t *testing.T) {
	// Isolate network backpressure from storage speed. This exercises exactly the
	// production bounded writer on a real TCP socket whose peer stops reading.
	result := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := websocket.Upgrader{}
		c, err := u.Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer c.Close()
		frame := []byte(`{"padding":"` + strings.Repeat("x", 60000) + `"}`)
		for i := 0; i < 10000; i++ {
			if err = writeWSBytes(c, frame); err != nil {
				result <- err
				return
			}
		}
		result <- fmt.Errorf("all writes unexpectedly completed")
	}))
	defer server.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if tcp, ok := c.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(1024)
	}
	select {
	case err := <-result:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("expected write deadline: %v", err)
		}
	case <-time.After(wsWriteTimeout + 4*time.Second):
		t.Fatal("slow writer was not disconnected")
	}
}

func TestHTTPWebSocketAllResumeFlushesOfflineSettlement(t *testing.T) {
	f := newHTTPFixture(t, nil)
	a := f.agent(f.project())
	saved := a.Cursor
	a, _ = f.s.SetSettled(a.ID, true)
	c := dialHTTPWS(t, f.server)
	subscribeHTTPWS(t, c, true, nil, map[string]uint64{a.ID: saved})
	cursors := map[string]uint64{a.ID: saved}
	frames := drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: a.Cursor})
	found := false
	for _, frame := range frames {
		if frame.Type == "inventory" && frame.Agent.Id == a.ID && frame.Agent.Settled {
			found = true
		}
	}
	if !found {
		t.Fatal("settled offline agent was silently omitted")
	}
}

func TestHTTPWebSocketSettledStoppingFlushesFinalEvents(t *testing.T) {
	started, cancelling, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p := &httpTestProvider{complete: func(r agent.CompletionRequest) (*agent.Response, error) {
		close(started)
		<-r.RequestContext().Done()
		close(cancelling)
		<-release
		return nil, r.RequestContext().Err()
	}}
	f := newHTTPFixture(t, p)
	project := f.project()
	// Device one subscribes before device two creates the agent and starts work.
	c := dialHTTPWS(t, f.server)
	subscribeHTTPWS(t, c, true, nil, nil)
	a := f.agent(project)
	base := "/v1/agents/" + a.ID
	f.request("POST", base+"/messages", `{"text":"cancel me"}`, "cancel-me", 202)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	a, _ = f.s.GetAgent(a.ID)
	cursors := map[string]uint64{}
	drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: a.Cursor})
	stopping := decodeHTTP[Agent](t, f.request("PATCH", base, `{"settled":true}`, "", 200))
	select {
	case <-cancelling:
	case <-time.After(5 * time.Second):
		t.Fatal("settle did not cancel")
	}
	frames := drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: stopping.Cursor})
	sawStopping := false
	for _, frame := range frames {
		if frame.Type == "inventory" && frame.Agent.Settled && frame.Agent.State == "stopping" {
			sawStopping = true
		}
	}
	if !sawStopping {
		t.Fatal("did not observe settled stopping state")
	}
	close(release)
	final := waitHTTPAgent(t, f.s, a.ID, func(a Agent) bool { return a.State == "idle" && a.Settled })
	frames = drainHTTPWS(t, c, cursors, map[string]uint64{a.ID: final.Cursor})
	sawTerminal, sawIdle := false, false
	for _, frame := range frames {
		if frame.Type == "event" && frame.Event.Type == "turn.cancelled" {
			sawTerminal = true
		}
		if frame.Type == "inventory" && frame.Agent.Settled && frame.Agent.State == "idle" {
			sawIdle = true
		}
	}
	if !sawTerminal || !sawIdle {
		t.Fatalf("settled subscription dropped terminal frames: terminal=%v idle=%v", sawTerminal, sawIdle)
	}
}

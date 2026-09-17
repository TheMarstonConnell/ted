package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/api"
	"github.com/TheMarstonConnell/ted/browser"
	"github.com/gorilla/websocket"
)

type fakeBrowserEvent struct {
	event browser.LiveEvent
	err   error
}
type fakeBrowserLive struct {
	sent      chan browser.LiveCommand
	events    chan fakeBrowserEvent
	closed    chan struct{}
	once      sync.Once
	sendErr   error
	blockSend bool
}

func newFakeBrowserLive() *fakeBrowserLive {
	return &fakeBrowserLive{sent: make(chan browser.LiveCommand, 512), events: make(chan fakeBrowserEvent, 128), closed: make(chan struct{})}
}
func (f *fakeBrowserLive) Send(c browser.LiveCommand) error {
	if f.blockSend {
		<-f.closed
		return io.EOF
	}
	if f.sendErr != nil {
		return f.sendErr
	}
	select {
	case f.sent <- c:
		return nil
	case <-f.closed:
		return io.EOF
	}
}
func (f *fakeBrowserLive) Receive() (browser.LiveEvent, error) {
	select {
	case e := <-f.events:
		return e.event, e.err
	case <-f.closed:
		return browser.LiveEvent{}, io.EOF
	}
}
func (f *fakeBrowserLive) Close() error { f.once.Do(func() { close(f.closed) }); return nil }

func browserFixture(t *testing.T, connect browserLiveConnector) *httpFixture {
	t.Helper()
	f := newHTTPFixture(t, nil)
	f.server.Close()
	f.server = httptest.NewServer(newHandler(f.s, connect))
	t.Cleanup(f.server.Close)
	return f
}
func browserWSURL(f *httpFixture, id string) string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http") + "/v1/agents/" + id + "/browser"
}
func dialBrowser(t *testing.T, f *httpFixture, id string) *websocket.Conn {
	t.Helper()
	c, r, err := websocket.DefaultDialer.Dial(browserWSURL(f, id), http.Header{"Origin": {f.server.URL}})
	if err != nil {
		t.Fatalf("dial browser: %v (%v)", err, r)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func readBrowser(t *testing.T, c *websocket.Conn) browser.LiveEvent {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Components.Schemas["BrowserLiveEvent"].Value.VisitJSON(raw); err != nil {
		t.Fatalf("event schema: %v (%s)", err, data)
	}
	var event browser.LiveEvent
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	return event
}
func awaitBrowserClosed(t *testing.T, live *fakeBrowserLive) {
	t.Helper()
	select {
	case <-live.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon subscription not closed")
	}
}

func TestBrowserGeneratedRouteAndHandshakeSecurity(t *testing.T) {
	var calls atomic.Int32
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) {
		calls.Add(1)
		return nil, errors.New("private daemon path")
	})
	a := f.agent(f.project())
	path := "/v1/agents/" + a.ID + "/browser"
	f.request("GET", path, "", "", 400)
	f.request("POST", path, "", "", 405)
	f.request("GET", path+"?project=/tmp/evil", "", "", 400)
	f.request("GET", path+"?thread=other", "", "", 400)
	f.request("GET", path, "{}", "", 400)
	for _, origin := range []string{"https://evil.test", "null", f.server.URL + "/", f.server.URL + "?x=1"} {
		_, r, err := websocket.DefaultDialer.Dial(browserWSURL(f, a.ID), http.Header{"Origin": {origin}})
		if err == nil || r == nil || r.StatusCode != 403 {
			t.Fatalf("origin %q: %v %+v", origin, err, r)
		}
		r.Body.Close()
	}
	_, r, err := websocket.DefaultDialer.Dial(browserWSURL(f, "missing"), nil)
	if err == nil || r.StatusCode != 404 {
		t.Fatalf("missing agent: %v %+v", err, r)
	}
	r.Body.Close()
	if calls.Load() != 0 {
		t.Fatal("rejected handshake connected daemon")
	}
	_, r, err = websocket.DefaultDialer.Dial(browserWSURL(f, a.ID), nil)
	if err == nil || r.StatusCode != 503 {
		t.Fatalf("unavailable: %v %+v", err, r)
	}
	data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if strings.Contains(string(data), "private daemon") || !strings.Contains(string(data), "browser_unavailable") {
		t.Fatalf("unsafe error: %s", data)
	}
	if calls.Load() != 1 {
		t.Fatal("generated route did not invoke connector")
	}
}

func TestBrowserTrustedCanonicalIdentityAndNoInitialCommand(t *testing.T) {
	fakes := make(chan *fakeBrowserLive, 4)
	requests := make(chan browser.Request, 4)
	f := browserFixture(t, func(_ context.Context, req browser.Request) (browserLiveConnection, error) {
		live := newFakeBrowserLive()
		requests <- req
		fakes <- live
		return live, nil
	})
	root := t.TempDir()
	runGit(t, root, "init")
	sub := filepath.Join(root, "nested")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CreateProject(CreateProjectRequest{Name: "nested project", Root: sub})
	if err != nil {
		t.Fatal(err)
	}
	a := f.agent(p)
	c := dialBrowser(t, f, a.ID)
	req, live := <-requests, <-fakes
	if req.Project != sub || req.Thread != a.ID || req.Home != filepath.Join(f.s.dir, "runtime") || req.Action != "" || req.Params != nil {
		t.Fatalf("untrusted identity: %+v", req)
	}
	select {
	case cmd := <-live.sent:
		t.Fatalf("subscription sent command: %+v", cmd)
	default:
	}
	sendHTTPWS(t, c, map[string]any{"type": "watch"})
	select {
	case cmd := <-live.sent:
		if cmd.Type != "watch" || cmd.TabID != "" {
			t.Fatal(cmd)
		}
	case <-time.After(time.Second):
		t.Fatal("watch not forwarded")
	}
	c.Close()
	awaitBrowserClosed(t, live)
}

func TestBrowserManagedWorktreeUsesRuntimeProjectNotWorkingDirectory(t *testing.T) {
	git := newLocalRemote(t)
	s, err := NewService(t.TempDir(), nil, []agent.Provider{workspaceProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	p, err := s.CreateProject(CreateProjectRequest{Name: "worktree", Root: git.checkout, WorkspaceDefaults: &WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, Prompt: "identity"}, "")
	if err != nil {
		t.Fatal(err)
	}
	a = awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" })
	if a.Workspace.Status != "ready" || a.Workspace.Path == p.Root {
		t.Fatalf("workspace not prepared: %+v", a.Workspace)
	}
	s.mu.Lock()
	instance := s.instances[a.ID]
	s.mu.Unlock()
	requests := make(chan browser.Request, 4)
	server := httptest.NewServer(newHandler(s, func(_ context.Context, req browser.Request) (browserLiveConnection, error) {
		requests <- req
		return newFakeBrowserLive(), nil
	}))
	t.Cleanup(server.Close)
	f := &httpFixture{s: s, server: server, t: t}
	c := dialBrowser(t, f, a.ID)
	req := <-requests
	if instance == nil || req.Project != instance.ProjectRoot() || req.Thread != instance.ThreadID() || req.Project == a.Workspace.Path || req.Home != filepath.Join(s.dir, "runtime") {
		t.Fatalf("browser/runtime mismatch: %+v", req)
	}
	c.Close()
	child, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, ParentAgentID: a.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !child.Workspace.Shared || child.Workspace.Path != a.Workspace.Path {
		t.Fatal(child.Workspace)
	}
	c = dialBrowser(t, f, child.ID)
	req = <-requests
	if req.Project != git.checkout || req.Thread != child.ID {
		t.Fatalf("shared draft identity: %+v", req)
	}
	c.Close()
}

func TestBrowserCommandsEventsAndRecoverableValidation(t *testing.T) {
	live := newFakeBrowserLive()
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) { return live, nil })
	a := f.agent(f.project())
	c := dialBrowser(t, f, a.ID)
	for _, event := range []browser.LiveEvent{
		{Type: "state", Tabs: []browser.LiveTab{{ID: "tab-a", URL: "https://example.test", Title: "Example"}}, Selected: "tab-a", TabID: "tab-a"},
		{Type: "frame", TabID: "tab-a", Data: "aGVsbG8=", Width: 1024, Height: 768},
		{Type: "activity", TabID: "tab-a", Kind: "click", X: 12, Y: 20},
		{Type: "error", Code: "tab_not_found", Message: "Tab was closed"},
	} {
		live.events <- fakeBrowserEvent{event: event}
		got := readBrowser(t, c)
		wantJSON, _ := json.Marshal(event)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Fatalf("event changed: %s -> %s", wantJSON, gotJSON)
		}
	}
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"cdp","method":"Runtime.evaluate"}`)); err != nil {
		t.Fatal(err)
	}
	if event := readBrowser(t, c); event.Type != "error" || event.Code != "invalid" {
		t.Fatal(event)
	}
	commands := []string{
		`{"type":"watch","tab_id":"tab-a"}`,
		`{"type":"new","url":"about:blank"}`,
		`{"type":"navigate","url":"https://example.test"}`,
		`{"type":"navigate","tab_id":"tab-a","url":"https://example.test/next"}`,
		`{"type":"back","tab_id":"tab-a"}`,
		`{"type":"forward","tab_id":"tab-a"}`,
		`{"type":"reload","tab_id":"tab-a"}`,
		`{"type":"mouse","tab_id":"tab-a","event":"mouseMoved","x":0,"y":0}`,
		`{"type":"mouse","tab_id":"tab-a","event":"mousePressed","x":12.5,"y":42,"button":"left","buttons":1,"click_count":2,"modifiers":8}`,
		`{"type":"mouse","tab_id":"tab-a","event":"mouseWheel","x":12.5,"y":42,"delta_x":-10,"delta_y":25}`,
		`{"type":"key","tab_id":"tab-a","event":"keyDown","key":"A","code":"KeyA","text":"A","key_code":65,"modifiers":8}`,
		`{"type":"key","tab_id":"tab-a","event":"keyUp","code":"KeyA"}`,
		`{"type":"text","tab_id":"tab-a","text":"hello 👋"}`,
		`{"type":"release","tab_id":"tab-a"}`,
		`{"type":"close","tab_id":"tab-a"}`,
	}
	for _, raw := range commands {
		if err := c.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		var want browser.LiveCommand
		_ = json.Unmarshal([]byte(raw), &want)
		select {
		case got := <-live.sent:
			if got != want {
				t.Fatalf("command changed: %+v != %+v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("not forwarded: %s", raw)
		}
	}
	c.Close()
	awaitBrowserClosed(t, live)
}

func TestBrowserStrictCommandValidation(t *testing.T) {
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	h := &httpAPI{spec: spec}
	invalid := []string{
		`null`, `[]`, `true`, `{}`, `{"type":"watch"} {}`, `{"type":"watch","type":"new"}`,
		`{"type":null}`, `{"type":"watch","project":"/tmp"}`, `{"type":"watch","thread":"other"}`, `{"type":"watch","home":"/tmp"}`,
		`{"type":"cdp"}`, `{"type":"watch","tab_id":null}`, `{"type":"watch","url":"https://example.test"}`,
		`{"type":"new","tab_id":"tab"}`, `{"type":"navigate"}`, `{"type":"navigate","url":"javascript:alert(1)"}`,
		`{"type":"navigate","url":"relative"}`, `{"type":"navigate","url":"https://[invalid"}`,
		`{"type":"back"}`, `{"type":"reload","tab_id":""}`, `{"type":"close","tab_id":12}`, `{"type":"release"}`,
		`{"type":"text","tab_id":"tab","text":null}`, `{"type":"text","tab_id":"tab","text":42}`,
		`{"type":"mouse","tab_id":"tab","event":"keyDown","x":1,"y":2}`,
		`{"type":"mouse","tab_id":"tab","event":"mousePressed","x":1,"y":2}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":-1,"y":2}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":1e300,"y":2}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":1,"y":2,"buttons":1.5}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":1,"y":2,"buttons":8}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":1,"y":2,"button":"back"}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":1,"y":2,"click_count":4}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":1,"y":2,"modifiers":16}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseWheel","x":1,"y":2,"delta_y":100001}`,
		`{"type":"key","tab_id":"tab","event":"mouseMoved","key":"a"}`,
		`{"type":"key","tab_id":"tab","event":"keyDown"}`, `{"type":"key","tab_id":"tab","event":"keyDown","code":""}`,
		`{"type":"key","tab_id":"tab","event":"keyDown","key":"a","key_code":65536}`,
		`{"type":"key","tab_id":"tab","event":"keyDown","key":"a","x":1}`,
		`{"type":"watch","tab_id":"` + strings.Repeat("a", 257) + `"}`,
		`{"type":"new","url":"https://example.test/` + strings.Repeat("a", 8192) + `"}`,
		`{"type":"text","tab_id":"tab","text":"` + strings.Repeat("a", 16385) + `"}`,
	}
	for _, raw := range invalid {
		if _, err := h.validateBrowserCommand([]byte(raw)); err == nil {
			t.Errorf("accepted invalid command: %.200s", raw)
		}
	}
}

func TestBrowserDisconnectAndShutdownCleanup(t *testing.T) {
	for _, action := range []string{"client", "daemon", "send", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			live := newFakeBrowserLive()
			if action == "send" {
				live.sendErr = errors.New("private socket path")
			}
			f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) { return live, nil })
			a := f.agent(f.project())
			c := dialBrowser(t, f, a.ID)
			switch action {
			case "client":
				c.Close()
			case "daemon":
				live.events <- fakeBrowserEvent{err: io.EOF}
			case "send":
				sendHTTPWS(t, c, map[string]string{"type": "watch"})
			case "shutdown":
				if err := f.s.BeginShutdown(); err != nil {
					t.Fatal(err)
				}
			}
			if action != "client" {
				event := readBrowser(t, c)
				if event.Type != "error" || strings.Contains(event.Message, "private") {
					t.Fatal(event)
				}
				if action == "shutdown" && event.Code != "shutting_down" {
					t.Fatal(event)
				}
				if action != "shutdown" && event.Code != "browser_unavailable" {
					t.Fatal(event)
				}
			}
			awaitBrowserClosed(t, live)
		})
	}
}

func TestBrowserShutdownCancelsOpeningConnector(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	f := browserFixture(t, func(ctx context.Context, _ browser.Request) (browserLiveConnection, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	})
	a := f.agent(f.project())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, r, _ := websocket.DefaultDialer.Dial(browserWSURL(f, a.ID), nil)
		if c != nil {
			c.Close()
		}
		if r != nil {
			r.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("connector not called")
	}
	if err := f.s.BeginShutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("connector not cancelled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handshake did not return")
	}
}

func TestBrowserViewerLimitAndReuse(t *testing.T) {
	fakes := make(chan *fakeBrowserLive, 8)
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) {
		live := newFakeBrowserLive()
		fakes <- live
		return live, nil
	})
	p := f.project()
	a, b := f.agent(p), f.agent(p)
	connections := make([]*websocket.Conn, 0, maxAgentBrowserViewers)
	lives := make([]*fakeBrowserLive, 0, maxAgentBrowserViewers)
	for range maxAgentBrowserViewers {
		connections = append(connections, dialBrowser(t, f, a.ID))
		lives = append(lives, <-fakes)
	}
	_, r, err := websocket.DefaultDialer.Dial(browserWSURL(f, a.ID), nil)
	if err == nil || r.StatusCode != 503 {
		t.Fatalf("viewer limit: %v %+v", err, r)
	}
	r.Body.Close()
	other := dialBrowser(t, f, b.ID)
	otherLive := <-fakes
	other.Close()
	awaitBrowserClosed(t, otherLive)
	connections[0].Close()
	awaitBrowserClosed(t, lives[0])
	deadline := time.Now().Add(time.Second)
	for {
		c, r, err := websocket.DefaultDialer.Dial(browserWSURL(f, a.ID), nil)
		if err == nil {
			c.Close()
			awaitBrowserClosed(t, <-fakes)
			break
		}
		if r != nil {
			r.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not released: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBrowserInputBoundsAndBackpressure(t *testing.T) {
	for _, kind := range []string{"binary", "oversized", "invalid-flood", "queue"} {
		t.Run(kind, func(t *testing.T) {
			live := newFakeBrowserLive()
			live.blockSend = kind == "queue"
			f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) { return live, nil })
			a := f.agent(f.project())
			c := dialBrowser(t, f, a.ID)
			switch kind {
			case "binary":
				_ = c.WriteMessage(websocket.BinaryMessage, []byte(`{"type":"watch"}`))
			case "oversized":
				_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"text","text":"`+strings.Repeat("x", maxBrowserInput)+`"}`))
			case "invalid-flood":
				for range 8 {
					_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"bad"}`))
				}
			case "queue":
				for range browserQueueSize + 4 {
					_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"watch"}`))
				}
			}
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			for {
				_, _, err := c.ReadMessage()
				if err != nil {
					var closed *websocket.CloseError
					if !errors.As(err, &closed) {
						t.Fatalf("not gracefully closed: %v", err)
					}
					break
				}
			}
			awaitBrowserClosed(t, live)
		})
	}
}

func TestBrowserFrameCoalescingAndSlowReader(t *testing.T) {
	live := newFakeBrowserLive()
	f := newHTTPFixture(t, nil)
	f.server.Close()
	f.server = httptest.NewUnstartedServer(newHandler(f.s, func(context.Context, browser.Request) (browserLiveConnection, error) { return live, nil }))
	f.server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetWriteBuffer(1024)
			}
		}
	}
	f.server.Start()
	t.Cleanup(f.server.Close)
	a := f.agent(f.project())
	c := dialBrowser(t, f, a.ID)
	if tcp, ok := c.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(1024)
	}
	published := make(chan struct{})
	go func() {
		defer close(published)
		for i := range 400 {
			event := fakeBrowserEvent{event: browser.LiveEvent{Type: "frame", TabID: "tab", Data: strings.Repeat("x", 256<<10), Width: 800, Height: 600}}
			select {
			case live.events <- event:
			case <-live.closed:
				return
			}
			if i == 399 {
				return
			}
		}
	}()
	select {
	case <-published:
	case <-time.After(3 * time.Second):
		t.Fatal("frame reception blocked behind viewer")
	}
	select {
	case <-live.closed:
	case <-time.After(2*wsWriteTimeout + 3*time.Second):
		t.Fatal("slow viewer not disconnected")
	}
}

func TestBrowserOversizedDaemonEvent(t *testing.T) {
	live := newFakeBrowserLive()
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) { return live, nil })
	a := f.agent(f.project())
	c := dialBrowser(t, f, a.ID)
	live.events <- fakeBrowserEvent{event: browser.LiveEvent{Type: "frame", Data: strings.Repeat("x", maxBrowserOutput)}}
	event := readBrowser(t, c)
	if event.Type != "error" || event.Code != "too_large" {
		t.Fatal(event)
	}
	awaitBrowserClosed(t, live)
}

func TestBrowserGlobalViewerLimit(t *testing.T) {
	s, _, project, first := serviceFixture(t)
	var releaseFirst func()
	for i := range maxBrowserViewers {
		a := first
		if i != 0 {
			var err error
			a, err = s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
			if err != nil {
				t.Fatal(err)
			}
		}
		_, release, err := s.beginBrowserViewer(a.ID, func() {})
		if err != nil {
			t.Fatalf("viewer %d rejected: %v", i, err)
		}
		if i == 0 {
			releaseFirst = release
		} else {
			t.Cleanup(release)
		}
	}
	_, _, err := s.beginBrowserViewer(first.ID, func() {})
	assertStatus(t, err, 503)
	releaseFirst()
	_, release, err := s.beginBrowserViewer(first.ID, func() {})
	if err != nil {
		t.Fatalf("released global slot not reusable: %v", err)
	}
	release()
}

func TestBrowserPingWhileDaemonSendBlocked(t *testing.T) {
	live := newFakeBrowserLive()
	live.blockSend = true
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) { return live, nil })
	a := f.agent(f.project())
	c := dialBrowser(t, f, a.ID)
	pong := make(chan string, 1)
	c.SetPongHandler(func(data string) error { pong <- data; return nil })
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
	sendHTTPWS(t, c, map[string]string{"type": "watch"})
	if err := c.WriteControl(websocket.PingMessage, []byte("alive"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-pong:
		if data != "alive" {
			t.Fatal(data)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon send blocked WebSocket control frames")
	}
	c.Close()
	awaitBrowserClosed(t, live)
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("client reader leaked")
	}
}

func TestBrowserProjectDeletionEndsSubscription(t *testing.T) {
	live := newFakeBrowserLive()
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) { return live, nil })
	p := f.project()
	a := f.agent(p)
	c := dialBrowser(t, f, a.ID)
	f.request("PATCH", "/v1/agents/"+a.ID, `{"settled":true}`, "", 200)
	f.request("DELETE", "/v1/projects/"+p.ID, "", "", 204)
	event := readBrowser(t, c)
	if event.Type != "error" || event.Code != "shutting_down" {
		t.Fatal(event)
	}
	awaitBrowserClosed(t, live)
}

func TestBrowserIndependentViewersAndThreadRouting(t *testing.T) {
	type opened struct {
		req  browser.Request
		live *fakeBrowserLive
	}
	openedCh := make(chan opened, 4)
	f := browserFixture(t, func(_ context.Context, req browser.Request) (browserLiveConnection, error) {
		live := newFakeBrowserLive()
		openedCh <- opened{req, live}
		return live, nil
	})
	p := f.project()
	a, b := f.agent(p), f.agent(p)
	first := dialBrowser(t, f, a.ID)
	one := <-openedCh
	second := dialBrowser(t, f, a.ID)
	two := <-openedCh
	third := dialBrowser(t, f, b.ID)
	three := <-openedCh
	if one.req.Thread != a.ID || two.req.Thread != a.ID || three.req.Thread != b.ID {
		t.Fatal("connector thread mismatch")
	}
	first.Close()
	awaitBrowserClosed(t, one.live)
	select {
	case <-two.live.closed:
		t.Fatal("closing viewer closed peer")
	case <-three.live.closed:
		t.Fatal("closing viewer closed other agent")
	default:
	}
	sendHTTPWS(t, second, map[string]string{"type": "watch", "tab_id": "pinned"})
	select {
	case command := <-two.live.sent:
		if command.TabID != "pinned" {
			t.Fatal(command)
		}
	case <-time.After(time.Second):
		t.Fatal("viewer command lost")
	}
	select {
	case command := <-three.live.sent:
		t.Fatalf("command leaked to another thread: %+v", command)
	default:
	}
	two.live.events <- fakeBrowserEvent{event: browser.LiveEvent{Type: "state", TabID: "pinned", Selected: "agent-selected"}}
	if event := readBrowser(t, second); event.TabID != "pinned" || event.Selected != "agent-selected" {
		t.Fatal(event)
	}
	three.live.events <- fakeBrowserEvent{event: browser.LiveEvent{Type: "state", TabID: "third"}}
	if event := readBrowser(t, third); event.TabID != "third" {
		t.Fatal(event)
	}
}

func TestBrowserRejectsMalformedUpgradeBeforeDaemonStartup(t *testing.T) {
	var calls atomic.Int32
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) {
		calls.Add(1)
		return nil, io.EOF
	})
	a := f.agent(f.project())
	for _, headers := range []http.Header{
		{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}},
		{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Version": {"13"}, "Sec-Websocket-Key": {"bad"}},
		{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Version": {"12"}, "Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}},
	} {
		req, err := http.NewRequest("GET", f.server.URL+"/v1/agents/"+a.ID+"/browser", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = headers
		response, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("malformed upgrade status %d", response.StatusCode)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("malformed upgrade started daemon")
	}
}

func TestBrowserMissingProjectDoesNotFallBackToServerDirectory(t *testing.T) {
	var calls atomic.Int32
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) {
		calls.Add(1)
		return nil, io.EOF
	})
	p := f.project()
	a := f.agent(p)
	f.s.mu.Lock()
	delete(f.s.state.Projects, p.ID)
	f.s.mu.Unlock()
	_, response, err := websocket.DefaultDialer.Dial(browserWSURL(f, a.ID), nil)
	if err == nil || response == nil || response.StatusCode != 409 {
		t.Fatalf("missing project: %v %+v", err, response)
	}
	response.Body.Close()
	if calls.Load() != 0 {
		t.Fatal("missing project connected unrelated daemon")
	}
}

func TestBrowserUnavailableDirectoryDoesNotUseParentDirectory(t *testing.T) {
	for _, replacement := range []string{"missing", "file"} {
		t.Run(replacement, func(t *testing.T) {
			var calls atomic.Int32
			f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) {
				calls.Add(1)
				return nil, io.EOF
			})
			p := f.project()
			a := f.agent(p)
			if err := os.Remove(p.Root); err != nil {
				t.Fatal(err)
			}
			if replacement == "file" {
				if err := os.WriteFile(p.Root, []byte("not a project directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, response, err := websocket.DefaultDialer.Dial(browserWSURL(f, a.ID), nil)
			if err == nil || response == nil || response.StatusCode != 409 {
				t.Fatalf("unavailable directory: %v %+v", err, response)
			}
			response.Body.Close()
			if calls.Load() != 0 {
				t.Fatal("unavailable project connected unrelated daemon")
			}
		})
	}
}

func TestBrowserDefaultConnectorUsesRuntimeSocketNotProcessHome(t *testing.T) {
	home, err := os.MkdirTemp("", "ted-live-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("TED_HOME", t.TempDir())
	s, err := NewService(home, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	socketDir := filepath.Join(home, "runtime", "browser")
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(socketDir, "daemon.sock"))
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	requests := make(chan browser.Request, 1)
	commands := make(chan browser.LiveCommand, 1)
	daemonErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			daemonErr <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		decoder, encoder := json.NewDecoder(conn), json.NewEncoder(conn)
		var req browser.Request
		if err := decoder.Decode(&req); err != nil {
			daemonErr <- err
			return
		}
		requests <- req
		if err := encoder.Encode(browser.Response{OK: true}); err != nil {
			daemonErr <- err
			return
		}
		if err := encoder.Encode(browser.LiveEvent{Type: "state"}); err != nil {
			daemonErr <- err
			return
		}
		var command browser.LiveCommand
		if err := decoder.Decode(&command); err != nil {
			daemonErr <- err
			return
		}
		commands <- command
	}()
	server := httptest.NewServer(NewHandler(s))
	t.Cleanup(server.Close)
	f := &httpFixture{s: s, server: server, t: t}
	p, err := s.CreateProject(CreateProjectRequest{Name: "connector", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	c := dialBrowser(t, f, a.ID)
	select {
	case req := <-requests:
		if req.Project != p.Root || req.Thread != a.ID || req.Action != "live" || req.Home != "" {
			t.Fatalf("incorrect daemon request: %+v", req)
		}
	case err := <-daemonErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("default connector did not reach runtime socket")
	}
	if event := readBrowser(t, c); event.Type != "state" {
		t.Fatal(event)
	}
	sendHTTPWS(t, c, map[string]string{"type": "watch"})
	select {
	case command := <-commands:
		if command.Type != "watch" {
			t.Fatal(command)
		}
	case err := <-daemonErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("default connector did not forward command")
	}
}

type browserCloseCall struct {
	home, project, thread string
}

func TestBrowserViewerSessionLifecycle(t *testing.T) {
	lives := make(chan *fakeBrowserLive, 4)
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) {
		live := newFakeBrowserLive()
		lives <- live
		return live, nil
	})
	project := f.project()
	a := f.agent(project)
	calls := make(chan browserCloseCall, 4)
	f.s.mu.Lock()
	f.s.browserClose = func(_ context.Context, home, project, thread string) error {
		calls <- browserCloseCall{home, project, thread}
		return nil
	}
	f.s.mu.Unlock()

	first := dialBrowser(t, f, a.ID)
	firstLive := <-lives
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	awaitBrowserClosed(t, firstLive)
	select {
	case call := <-calls:
		t.Fatalf("ordinary disconnect closed session: %+v", call)
	case <-time.After(50 * time.Millisecond):
	}

	second := dialBrowser(t, f, a.ID)
	secondLive := <-lives
	if _, err := f.s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	want := browserCloseCall{filepath.Join(f.s.dir, "runtime"), project.Root, a.ID}
	select {
	case call := <-calls:
		if call != want {
			t.Fatalf("settle close = %+v, want %+v", call, want)
		}
	case <-time.After(time.Second):
		t.Fatal("settle did not close viewer-created session")
	}
	awaitBrowserClosed(t, secondLive)
	_ = second.Close()

	third := dialBrowser(t, f, a.ID)
	thirdLive := <-lives
	if err := f.s.DeleteProject(project.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-calls:
		if call != want {
			t.Fatalf("delete close = %+v, want %+v", call, want)
		}
	case <-time.After(time.Second):
		t.Fatal("delete did not close reconnected settled viewer session")
	}
	awaitBrowserClosed(t, thirdLive)
	_ = third.Close()
}

func TestBrowserSettleCancelsOpeningViewerBeforeCleanup(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	f := browserFixture(t, func(ctx context.Context, _ browser.Request) (browserLiveConnection, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	})
	project := f.project()
	a := f.agent(project)
	calls := make(chan browserCloseCall, 1)
	f.s.mu.Lock()
	f.s.browserClose = func(_ context.Context, home, project, thread string) error {
		calls <- browserCloseCall{home, project, thread}
		return nil
	}
	f.s.mu.Unlock()

	dialed := make(chan struct{})
	go func() {
		defer close(dialed)
		c, response, _ := websocket.DefaultDialer.Dial(browserWSURL(f, a.ID), nil)
		if c != nil {
			_ = c.Close()
		}
		if response != nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("connector not entered")
	}
	if _, err := f.s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("opening viewer was not cancelled")
	}
	select {
	case call := <-calls:
		want := browserCloseCall{filepath.Join(f.s.dir, "runtime"), project.Root, a.ID}
		if call != want {
			t.Fatalf("settle close = %+v, want %+v", call, want)
		}
	case <-time.After(time.Second):
		t.Fatal("opening viewer session was not cleaned up")
	}
	select {
	case <-dialed:
	case <-time.After(time.Second):
		t.Fatal("cancelled handshake did not return")
	}
}

func TestBrowserShutdownClosesViewerSessionWithoutRuntimeInstance(t *testing.T) {
	s, err := NewService(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = s.Close(context.Background())
		}
	})
	server := httptest.NewServer(newHandler(s, func(context.Context, browser.Request) (browserLiveConnection, error) {
		return newFakeBrowserLive(), nil
	}))
	t.Cleanup(server.Close)
	f := &httpFixture{s: s, server: server, t: t}
	project, err := s.CreateProject(CreateProjectRequest{Name: "shutdown", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan browserCloseCall, 1)
	s.mu.Lock()
	s.browserClose = func(_ context.Context, home, project, thread string) error {
		calls <- browserCloseCall{home, project, thread}
		return nil
	}
	if s.instances[a.ID] != nil {
		t.Fatal("viewer-only agent unexpectedly has a runtime instance")
	}
	s.mu.Unlock()
	c := dialBrowser(t, f, a.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	closed = true
	select {
	case call := <-calls:
		want := browserCloseCall{filepath.Join(s.dir, "runtime"), project.Root, a.ID}
		if call != want {
			t.Fatalf("shutdown close = %+v, want %+v", call, want)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not close viewer-only session")
	}
	_ = c.Close()
}

func TestBrowserSettleCleanupDoesNotCreateRuntimeHome(t *testing.T) {
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) {
		return newFakeBrowserLive(), nil
	})
	project := f.project()
	a := f.agent(project)
	c := dialBrowser(t, f, a.ID)
	if _, err := f.s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if _, err := os.Stat(filepath.Join(f.s.dir, "runtime")); !os.IsNotExist(err) {
		t.Fatalf("settle cleanup created runtime home: %v", err)
	}
}

func TestBrowserProjectDeletionRetainsIdentityAfterCleanupFailure(t *testing.T) {
	lives := make(chan *fakeBrowserLive, 2)
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) {
		live := newFakeBrowserLive()
		lives <- live
		return live, nil
	})
	project := f.project()
	a := f.agent(project)
	f.s.mu.Lock()
	f.s.browserClose = func(context.Context, string, string, string) error { return nil }
	f.s.mu.Unlock()
	first := dialBrowser(t, f, a.ID)
	firstLive := <-lives
	if _, err := f.s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	awaitBrowserClosed(t, firstLive)
	_ = first.Close()

	second := dialBrowser(t, f, a.ID)
	secondLive := <-lives
	f.s.mu.Lock()
	f.s.browserClose = func(context.Context, string, string, string) error {
		return errors.New("close unavailable")
	}
	f.s.mu.Unlock()
	assertStatus(t, f.s.DeleteProject(project.ID), 503)
	awaitBrowserClosed(t, secondLive)
	_ = second.Close()
	if _, err := f.s.GetAgent(a.ID); err != nil {
		t.Fatalf("failed cleanup discarded agent identity: %v", err)
	}
	if _, err := f.s.GetProject(project.ID); err != nil {
		t.Fatalf("failed cleanup discarded project identity: %v", err)
	}

	calls := make(chan browserCloseCall, 1)
	f.s.mu.Lock()
	f.s.browserClose = func(_ context.Context, home, project, thread string) error {
		calls <- browserCloseCall{home, project, thread}
		return nil
	}
	f.s.mu.Unlock()
	if err := f.s.DeleteProject(project.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-calls:
		want := browserCloseCall{filepath.Join(f.s.dir, "runtime"), project.Root, a.ID}
		if call != want {
			t.Fatalf("retried delete close = %+v, want %+v", call, want)
		}
	case <-time.After(time.Second):
		t.Fatal("project deletion did not retry retained browser identity")
	}
}

func TestBrowserShutdownRetainsAgentCloseForUntrackedRuntimeInstance(t *testing.T) {
	dir, err := os.MkdirTemp("", "ted-close-instance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s, err := NewService(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtimeHome := filepath.Join(s.dir, "runtime")
	socketDir := filepath.Join(runtimeHome, "browser")
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(socketDir, "daemon.sock"))
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	root := t.TempDir()
	instance, err := agent.NewAgentIn(nil, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.SetIdentity("misc-runtime", root, root, runtimeHome); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.instances["misc-runtime"] = instance
	s.mu.Unlock()
	request := make(chan browser.Request, 1)
	serveErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		var req browser.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			serveErr <- err
			return
		}
		request <- req
		serveErr <- json.NewEncoder(conn).Encode(browser.Response{OK: true})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-request:
		if req.Project != root || req.Thread != "misc-runtime" || req.Action != "session-close" {
			t.Fatalf("runtime instance close request = %+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown skipped untracked runtime instance")
	}
}

func TestBrowserLifecycleAfterServiceRestart(t *testing.T) {
	for _, action := range []string{"settle", "settle-again", "delete", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			original, err := NewService(dir, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			project, err := original.CreateProject(CreateProjectRequest{Name: "persisted", Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			a, err := original.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
			if err != nil {
				t.Fatal(err)
			}
			if action == "delete" || action == "settle-again" {
				if _, err := original.SetSettled(a.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			// Release durable storage as a crashed process would, without session cleanup.
			if err := original.store.close(); err != nil {
				t.Fatal(err)
			}
			if err := original.releaseLock(); err != nil {
				t.Fatal(err)
			}
			restored, err := NewService(dir, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			calls := make(chan browserCloseCall, 4)
			restored.browserClose = func(_ context.Context, home, project, thread string) error {
				calls <- browserCloseCall{home, project, thread}
				return nil
			}
			t.Cleanup(func() { _ = restored.Close(context.Background()) })
			switch action {
			case "settle", "settle-again":
				_, err = restored.SetSettled(a.ID, true)
			case "delete":
				err = restored.DeleteProject(project.ID)
			case "shutdown":
				err = restored.Close(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-calls:
				want := browserCloseCall{filepath.Join(dir, "runtime"), project.Root, a.ID}
				if got != want {
					t.Fatalf("cleanup target %+v, want %+v", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("persisted session identity was not cleaned up after restart")
			}
		})
	}
}

func TestConfiguredPublicOriginBehindTLSProxy(t *testing.T) {
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) {
		return newFakeBrowserLive(), nil
	})
	a := f.agent(f.project())
	f.server.Close()
	handler, err := WithPublicOrigin(newHandler(f.s, func(context.Context, browser.Request) (browserLiveConnection, error) {
		return newFakeBrowserLive(), nil
	}), "https://ted.example.com")
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(handler)
	t.Cleanup(f.server.Close)
	for _, path := range []string{"/v1/ws", "/v1/agents/" + a.ID + "/browser"} {
		for _, origin := range []string{"https://ted.example.com", "https://TED.EXAMPLE.COM", "https://evil.example.com", "http://ted.example.com", "https://ted.example.com/path", "https://ted.example.com?", "https://ted.example.com#", "null"} {
			headers := http.Header{"Origin": {origin}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"evil.example.com"}}
			c, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+path, headers)
			allowed := strings.EqualFold(origin, "https://ted.example.com")
			if allowed {
				if err != nil {
					t.Fatalf("TLS-terminated %s rejected: %v", path, err)
				}
				_ = c.Close()
			} else {
				if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
					if c != nil {
						_ = c.Close()
					}
					t.Fatalf("untrusted origin %q was not rejected: %v %+v", origin, err, response)
				}
				response.Body.Close()
			}
		}
	}
	for _, origin := range []string{"https://", "ftp://ted.example.com", "https://user@ted.example.com", "https://ted.example.com/", "https://ted.example.com?x=1"} {
		if _, err := WithPublicOrigin(http.NotFoundHandler(), origin); err == nil {
			t.Fatalf("invalid configured origin accepted: %q", origin)
		}
	}
}

func TestBrowserStorageFailureCancelsViewer(t *testing.T) {
	s, err := NewService(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	live := newFakeBrowserLive()
	server := httptest.NewServer(newHandler(s, func(context.Context, browser.Request) (browserLiveConnection, error) { return live, nil }))
	t.Cleanup(server.Close)
	project, err := s.CreateProject(CreateProjectRequest{Name: "storage", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	f := &httpFixture{s: s, server: server, t: t}
	c := dialBrowser(t, f, a.ID)
	defer c.Close()
	s.mu.Lock()
	err = s.failStorageLocked(errors.New("disk full"))
	s.mu.Unlock()
	assertStatus(t, err, 503)
	awaitBrowserClosed(t, live)
}

func TestSettleReportsBrowserCleanupFailureAndRetries(t *testing.T) {
	s, _, project, a := serviceFixture(t)
	calls := 0
	s.browserClose = func(_ context.Context, home, root, id string) error {
		calls++
		if home != filepath.Join(s.dir, "runtime") || root != project.Root || id != a.ID {
			t.Fatalf("wrong cleanup identity: %s %s %s", home, root, id)
		}
		if calls == 1 {
			return errors.New("daemon unavailable")
		}
		return nil
	}
	_, err := s.SetSettled(a.ID, true)
	assertStatus(t, err, 503)
	settled, err := s.GetAgent(a.ID)
	if err != nil || !settled.Settled || !settled.Held {
		t.Fatalf("failed cleanup lost settled/held state: %+v %v", settled, err)
	}
	retried, err := s.SetSettled(a.ID, true)
	if err != nil || calls != 2 || retried.Cursor != settled.Cursor {
		t.Fatalf("retry did not clean up without new events: calls=%d agent=%+v err=%v", calls, retried, err)
	}
}

func TestDeleteProjectWithMissingRootAndRunningDaemon(t *testing.T) {
	dir, err := os.MkdirTemp("", "ted-delete-missing-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s, err := NewService(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	runtimeHome := filepath.Join(dir, "runtime")
	t.Setenv("TED_HOME", runtimeHome)
	ctx, cancel := context.WithCancel(context.Background())
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- browser.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-daemonDone; err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.Dial("unix", filepath.Join(runtimeHome, "browser", "daemon.sock"))
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	root := filepath.Join(dir, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(CreateProjectRequest{Name: "missing", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(project.ID); err != nil {
		t.Fatalf("missing directory blocked metadata deletion: %v", err)
	}
	if _, err := s.GetProject(project.ID); err == nil {
		t.Fatal("project survived deletion")
	}
	if _, err := os.Stat(filepath.Join(runtimeHome, "browser", "projects")); !os.IsNotExist(err) {
		t.Fatalf("cleanup started Chrome or created project storage: %v", err)
	}
}

package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if req.Project != root || req.Thread != a.ID || req.Home != filepath.Join(f.s.dir, "runtime") || req.Action != "" || req.Params != nil {
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
	f := browserFixture(t, func(context.Context, browser.Request) (browserLiveConnection, error) { return live, nil })
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
	var viewers browserViewers
	for i := range maxBrowserViewers {
		if !viewers.acquire(fmt.Sprint(i)) {
			t.Fatal("premature viewer limit")
		}
	}
	if viewers.acquire("overflow") {
		t.Fatal("global limit not enforced")
	}
	viewers.release("0")
	if !viewers.acquire("reused") {
		t.Fatal("global slot not reusable")
	}
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

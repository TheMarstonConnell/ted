//go:build !windows

package browser

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
)

func TestLiveValidation(t *testing.T) {
	valid := []LiveCommand{
		{Type: "watch"}, {Type: "watch", TabID: "a"}, {Type: "new"}, {Type: "navigate", URL: "https://example.com"},
		{Type: "mouse", TabID: "a", Event: "mousePressed", Button: "left", X: 1, Y: 2, Buttons: 1, ClickCount: 2},
		{Type: "mouse", TabID: "a", Event: "mouseWheel", DeltaY: -100},
		{Type: "key", TabID: "a", Event: "keyDown", Key: "Shift", Code: "ShiftLeft", Modifiers: 8},
		{Type: "text", TabID: "a", Text: "hello"}, {Type: "release", TabID: "a"},
	}
	for _, c := range valid {
		if err := validateLiveCommand(c); err != nil {
			t.Errorf("valid %+v: %v", c, err)
		}
	}
	invalid := []LiveCommand{
		{Type: "cdp"}, {Type: "mouse", TabID: "a", Event: "click"}, {Type: "mouse", TabID: "a", Event: "mousePressed"},
		{Type: "mouse", Event: "mouseMoved"}, {Type: "mouse", TabID: "a", Event: "mouseMoved", X: math.NaN()},
		{Type: "mouse", TabID: "a", Event: "mouseMoved", DeltaY: math.Inf(1)},
		{Type: "mouse", TabID: "a", Event: "mouseMoved", X: -1}, {Type: "mouse", TabID: "a", Event: "mouseMoved", Buttons: 8},
		{Type: "mouse", TabID: "a", Event: "mouseMoved", Button: "back"}, {Type: "key", TabID: "a", Event: "char", Key: "a"},
		{Type: "key", TabID: "a", Event: "keyDown", Modifiers: 16}, {Type: "release"}, {Type: "close"}, {Type: "reload"},
		{Type: "navigate", URL: "javascript:alert(1)"}, {Type: "new", TabID: "a"}, {Type: "text", TabID: "a", Text: strings.Repeat("x", 65537)},
	}
	for _, c := range invalid {
		if err := validateLiveCommand(c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestLiveSubscriptionReadOnlyPeriodicAndCancellation(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ted")
	t.Setenv("TED_HOME", home)
	cancel, done := startTestServer(t)
	defer func() { cancel(); <-done }()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	client, err := OpenLive(ctx, Request{Project: t.TempDir(), Thread: "live-read-only"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for range 2 {
		event, err := client.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if event.Type != "state" || len(event.Tabs) != 0 || event.Selected != "" {
			t.Fatalf("unexpected empty state: %+v", event)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "browser", "projects")); !os.IsNotExist(err) {
		t.Fatalf("subscription created project/browser: %v", err)
	}
	if err := client.Send(LiveCommand{Type: "watch"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(LiveCommand{Type: "close", TabID: "foreign"}); err != nil {
		t.Fatal(err)
	}
	for {
		event, err := client.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			if event.Code != "not_found" {
				t.Fatalf("error: %+v", event)
			}
			break
		}
	}
	blocked := make(chan error, 1)
	go func() {
		for {
			_, err := client.Receive()
			if err != nil {
				blocked <- err
				return
			}
		}
	}()
	stop()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock Receive")
	}
}

func TestLivePersistentCommandDecoderAndIsolation(t *testing.T) {
	mgr := newManager(context.Background(), t.TempDir())
	defer mgr.close()
	project := t.TempDir()
	root, err := ProjectRoot(project)
	if err != nil {
		t.Fatal(err)
	}
	p := mgr.project(root, projectKey(root))
	foreign := &browserTab{id: "foreign", cancel: func() {}}
	p.sessions["other"] = &session{tabs: map[target.ID]*browserTab{"foreign": foreign}, order: []target.ID{"foreign"}, selected: "foreign"}
	server, client := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { handleConnection(ctx, mgr, server); close(done) }()
	// Pipeline request and commands to exercise decoder read-ahead at upgrade.
	go func() {
		_, _ = client.Write([]byte(`{"project":` + quote(project) + `,"thread":"mine","action":"live"}` + "\n" + `{"type":"watch","tab_id":"foreign"}` + "\n" + `{"type":"mouse","tab_id":"foreign","event":"mouseMoved","method":"Runtime.evaluate"}` + "\n"))
	}()
	dec := json.NewDecoder(client)
	var handshake Response
	if err := dec.Decode(&handshake); err != nil || !handshake.OK {
		t.Fatalf("handshake: %+v %v", handshake, err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	errors := 0
	for errors < 2 {
		var event LiveEvent
		if err := dec.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			errors++
		}
	}
	if p.sessions["mine"] != nil {
		t.Fatal("read-only commands created session")
	}
	if _, ok := p.sessions["other"].tabs["foreign"]; !ok {
		t.Fatal("modified other thread")
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection did not stop")
	}
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestLiveTabLookupDoesNotWaitForAgentOperation(t *testing.T) {
	mgr := newManager(context.Background(), t.TempDir())
	p := mgr.project("root", "key")
	tab := &browserTab{id: "tab"}
	s := &session{tabs: map[target.ID]*browserTab{"tab": tab}, selected: "tab", order: []target.ID{"tab"}}
	p.sessions["thread"] = s
	live := &liveSubscription{manager: mgr, key: "key", thread: "thread"}
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, found, err := live.tab("tab")
		if err == nil && found != tab {
			err = fail("test", "wrong tab")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("human operation blocked behind agent lock")
	}
	if _, _, err := live.tab("other"); err == nil {
		t.Fatal("accepted unowned tab")
	}
}

func TestInputTrackingReleaseRemovesOnlyOwnHeldState(t *testing.T) {
	sub := &liveSubscription{inputs: make(map[*browserTab]*liveInputState)}
	tab := &browserTab{}
	sub.track(tab, LiveCommand{Type: "mouse", Event: "mousePressed", Button: "left", X: 10, Y: 20})
	sub.track(tab, LiveCommand{Type: "mouse", Event: "mouseMoved", X: 30, Y: 40})
	sub.track(tab, LiveCommand{Type: "key", Event: "keyDown", Key: "Shift", Code: "ShiftLeft"})
	state := sub.inputs[tab]
	if len(state.buttons) != 1 || len(state.keys) != 1 || state.buttons["left"].X != 30 {
		t.Fatalf("held state: %+v", state)
	}
	sub.track(tab, LiveCommand{Type: "mouse", Event: "mouseReleased", Button: "left"})
	sub.track(tab, LiveCommand{Type: "key", Event: "keyUp", Key: "Shift", Code: "ShiftLeft"})
	if len(sub.inputs) != 0 {
		t.Fatal("released state leaked")
	}
}

func TestLiveStreamLimitIsPerCommandNotConnection(t *testing.T) {
	mgr := newManager(context.Background(), t.TempDir())
	defer mgr.close()
	server, conn := net.Pipe()
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go handleConnection(ctx, mgr, server)
	req := Request{Project: t.TempDir(), Thread: "long-stream", Action: "live"}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(conn)
	var response Response
	if err := dec.Decode(&response); err != nil || !response.OK {
		t.Fatalf("handshake %v %+v", err, response)
	}
	sent := make(chan error, 1)
	go func() {
		line := strings.Repeat(" ", 128<<10) + `{"type":"watch"}` + "\n"
		for range 70 {
			if _, err := conn.Write([]byte(line)); err != nil {
				sent <- err
				return
			}
		}
		_, err := conn.Write([]byte("{\"type\":\"close\",\"tab_id\":\"missing\"}\n"))
		sent <- err
	}()
	for {
		var event LiveEvent
		if err := dec.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			if event.Code != "not_found" {
				t.Fatalf("unexpected error: %+v", event)
			}
			break
		}
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
}

func TestLiveConcurrentSendIsFramed(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	cancel, done := startTestServer(t)
	defer func() { cancel(); <-done }()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	c, err := OpenLive(ctx, Request{Project: t.TempDir(), Thread: "concurrent-send"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sent := make(chan error, 32)
	for range 32 {
		go func() { sent <- c.Send(LiveCommand{Type: "close", TabID: "missing"}) }()
	}
	errors := 0
	for errors < 32 {
		event, err := c.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			if event.Code != "not_found" {
				t.Fatalf("corrupted command: %+v", event)
			}
			errors++
		}
	}
	for range 32 {
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
	}
}

func TestLiveExplicitHomeConnectsToTrustedRuntime(t *testing.T) {
	runtimeHome := filepath.Join(t.TempDir(), "runtime")
	t.Setenv("TED_HOME", runtimeHome)
	cancel, done := startTestServer(t)
	defer func() { cancel(); <-done }()
	processHome := filepath.Join(t.TempDir(), "different-home")
	t.Setenv("TED_HOME", processHome)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	req := Request{Home: runtimeHome, Project: t.TempDir(), Thread: "runtime-home"}
	c, err := OpenLive(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if event, err := c.Receive(); err != nil || event.Type != "state" {
		t.Fatalf("state %+v %v", event, err)
	}
	req.Action = "status"
	if response, err := Call(ctx, req); err != nil || !response.OK {
		t.Fatalf("status %+v %v", response, err)
	}
	if os.Getenv("TED_HOME") != processHome {
		t.Fatal("client changed process environment")
	}
	if _, err := os.Stat(processHome); !os.IsNotExist(err) {
		t.Fatalf("client created state in unrelated process home: %v", err)
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), runtimeHome) {
		t.Fatalf("client home leaked into daemon request: %s", encoded)
	}
}

func TestLiveDaemonStartupUsesChildHome(t *testing.T) {
	runtimeHome := filepath.Join(t.TempDir(), "runtime")
	processHome := filepath.Join(t.TempDir(), "process")
	t.Setenv("TED_HOME", processHome)
	script := filepath.Join(t.TempDir(), "daemon-fixture")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$TED_HOME\" > \"$TED_HOME/started-home\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := executablePath
	executablePath = func() (string, error) { return script, nil }
	defer func() { executablePath = previous }()
	path, err := socketPathForHome(runtimeHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := startDaemonInHome(path, runtimeHome); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(runtimeHome, "started-home"))
		if err == nil && string(data) == runtimeHome {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child used wrong home: %q %v", data, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if os.Getenv("TED_HOME") != processHome {
		t.Fatal("startup mutated process environment")
	}
}

func TestLiveReleaseMissingAndClosedTabsIsIdempotent(t *testing.T) {
	closed, cancel := context.WithCancel(context.Background())
	cancel()
	old := &browserTab{id: "closed", ctx: closed}
	current := &browserTab{id: "current", ctx: context.Background()}
	sub := &liveSubscription{inputs: make(map[*browserTab]*liveInputState)}
	sub.track(old, LiveCommand{Type: "key", Event: "keyDown", Key: "Alt", Code: "AltLeft"})
	sub.track(current, LiveCommand{Type: "key", Event: "keyDown", Key: "Shift", Code: "ShiftLeft"})
	for _, id := range []string{"closed", "closed", "missing", "foreign"} {
		if err := sub.execute(context.Background(), LiveCommand{Type: "release", TabID: id}); err != nil {
			t.Fatalf("release %q: %v", id, err)
		}
	}
	if _, ok := sub.inputs[old]; ok {
		t.Fatal("closed tab kept held input")
	}
	if state := sub.inputs[current]; state == nil || len(state.keys) != 1 {
		t.Fatal("release retargeted current tab")
	}
}

func TestLiveStateReportsAuthoritativePin(t *testing.T) {
	mgr := newManager(context.Background(), t.TempDir())
	p := mgr.project("root", "key")
	first := &browserTab{id: "first", ctx: context.Background(), streamUsers: 1}
	second := &browserTab{id: "second", ctx: context.Background(), streamUsers: 1}
	p.sessions["thread"] = &session{tabs: map[target.ID]*browserTab{"first": first, "second": second}, order: []target.ID{"first", "second"}, selected: "second"}
	sub := &liveSubscription{manager: mgr, key: "key", thread: "thread", watch: "first"}
	states, frames, events := make(chan LiveEvent, 1), make(chan LiveEvent, 1), make(chan LiveEvent, 16)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); sub.observe(ctx, states, frames, events) }()
	defer func() { cancel(); <-done }()
	select {
	case state := <-states:
		if state.Pinned != "first" || state.TabID != "first" || state.Selected != "second" {
			t.Fatalf("pin confused viewer and agent: %+v", state)
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"pinned":"first"`) {
			t.Fatalf("pin absent from wire state: %s", data)
		}
	case <-ctx.Done():
		t.Fatal("missing pinned state")
	}
	sub.mu.Lock()
	sub.watch = ""
	sub.mu.Unlock()
	select {
	case state := <-states:
		if state.Pinned != "" || state.TabID != "second" || state.Selected != "second" {
			t.Fatalf("follow retained stale pin: %+v", state)
		}
	case <-ctx.Done():
		t.Fatal("missing follow state")
	}
}

func TestSessionCloseWaitsForInflightLiveSessionCreation(t *testing.T) {
	mgr := newManager(context.Background(), t.TempDir())
	defer mgr.close()
	root, err := ProjectRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, thread := projectKey(root), "closing-live"
	project := mgr.project(root, key)
	lifecycle, release := mgr.retainSessionLifecycle(key, thread)
	defer release()
	lifecycle.gate.RLock()

	closed := make(chan error, 1)
	go func() {
		_, err := mgr.dispatch(context.Background(), Request{Project: root, Thread: thread, Action: "session-close"})
		closed <- err
	}()
	select {
	case err := <-closed:
		t.Fatalf("session close passed in-flight live command: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	s := &session{project: project, thread: thread, dir: t.TempDir(), tabs: make(map[target.ID]*browserTab)}
	project.mu.Lock()
	project.sessions[thread] = s
	project.mu.Unlock()
	lifecycle.gate.RUnlock()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("session close did not resume")
	}
	project.mu.Lock()
	remaining := project.sessions[thread]
	project.mu.Unlock()
	if remaining != nil || !s.closed {
		t.Fatal("session created by in-flight live command survived cleanup")
	}
}

func TestSessionCloseCancelsQueuedLiveNewBeforeCleanup(t *testing.T) {
	mgr := newManager(context.Background(), t.TempDir())
	defer mgr.close()
	root, err := ProjectRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	thread := "cancelled-live"
	key := projectKey(root)
	viewerCtx, cancelViewer := context.WithCancel(context.Background())
	defer cancelViewer()
	viewer := liveTestClient(t, viewerCtx, mgr, Request{Project: root, Thread: thread})
	receiveLive(t, viewer, func(event LiveEvent) bool { return event.Type == "state" })

	lifecycle, release := mgr.retainSessionLifecycle(key, thread)
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	lifecycle.gate.Lock()
	if err := viewer.Send(LiveCommand{Type: "new"}); err != nil {
		lifecycle.gate.Unlock()
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		_, err := mgr.dispatch(context.Background(), Request{Project: root, Thread: thread, Action: "session-close"})
		closed <- err
	}()
	disconnected := make(chan error, 1)
	go func() {
		for {
			if _, err := viewer.Receive(); err != nil {
				disconnected <- err
				return
			}
		}
	}()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		lifecycle.gate.Unlock()
		t.Fatal("session close did not cancel live subscription before cleanup")
	}
	lifecycle.gate.Unlock()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("session close remained blocked")
	}
	mgr.mu.Lock()
	projects := len(mgr.projects)
	mgr.mu.Unlock()
	if projects != 0 {
		t.Fatal("canceled queued new recreated a browser project")
	}
	release()
	released = true
	deadline := time.Now().Add(time.Second)
	for {
		mgr.mu.Lock()
		lifecycles := len(mgr.lifecycles)
		mgr.mu.Unlock()
		if lifecycles == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed thread lifecycle gate was retained")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSessionCloseWaitsForOrdinaryAgentRequest(t *testing.T) {
	mgr := newManager(context.Background(), t.TempDir())
	defer mgr.close()
	root, err := ProjectRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, thread := projectKey(root), "agent-close-race"
	p := mgr.project(root, key)
	p.started = true
	s := &session{project: p, thread: thread, dir: t.TempDir(), tabs: make(map[target.ID]*browserTab)}
	p.sessions[thread] = s
	// Delay an ordinary request between lifecycle admission and session lookup.
	p.mu.Lock()
	operation := make(chan error, 1)
	go func() {
		_, err := mgr.dispatch(context.Background(), Request{Project: root, Thread: thread, Action: "tabs"})
		operation <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		mgr.mu.Lock()
		lifecycle := mgr.lifecycles[sessionIdentity{key, thread}]
		mgr.mu.Unlock()
		if lifecycle != nil {
			if !lifecycle.gate.TryLock() {
				break
			}
			lifecycle.gate.Unlock()
		}
		if time.Now().After(deadline) {
			p.mu.Unlock()
			t.Fatal("ordinary request did not enter the shared lifecycle barrier")
		}
		time.Sleep(time.Millisecond)
	}
	closed := make(chan error, 1)
	go func() {
		_, err := mgr.dispatch(context.Background(), Request{Project: root, Thread: thread, Action: "session-close"})
		closed <- err
	}()
	p.mu.Unlock()
	for _, result := range []<-chan error{operation, closed} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("ordinary request/session cleanup did not finish")
		}
	}
	p.mu.Lock()
	remaining := p.sessions[thread]
	p.mu.Unlock()
	if remaining != nil || !s.closed {
		t.Fatal("ordinary request left a replacement session after cleanup")
	}
}

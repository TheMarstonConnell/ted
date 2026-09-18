//go:build !windows

package browser

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func liveTestClient(t *testing.T, ctx context.Context, mgr *manager, req Request) *LiveClient {
	t.Helper()
	server, conn := net.Pipe()
	go handleConnection(ctx, mgr, server)
	req.Action = "live"
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(conn)
	var response Response
	if err := dec.Decode(&response); err != nil || !response.OK {
		t.Fatalf("live handshake %+v %v", response, err)
	}
	client := &LiveClient{conn: conn, dec: dec}
	client.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func receiveLive(t *testing.T, c *LiveClient, predicate func(LiveEvent) bool) LiveEvent {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(12 * time.Second))
	defer c.conn.SetReadDeadline(time.Time{})
	for {
		event, err := c.Receive()
		if err != nil {
			t.Fatalf("receive live event: %v", err)
		}
		if event.Type == "error" {
			t.Fatalf("live error: %+v", event)
		}
		if predicate(event) {
			return event
		}
	}
}

func TestLiveSharedChromeIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	mgr := newManager(ctx, t.TempDir())
	defer mgr.close()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<title>Live ` + r.URL.Path + `</title><style>input{position:absolute;left:20px;top:20px;width:200px;height:30px}button{position:absolute;left:20px;top:90px;width:150px;height:40px}</style><input id="field"><button id="go" onclick="document.body.dataset.clicked='yes'">Go</button><script>window.events=[];for(const type of ['keydown','keyup','mousedown','mouseup'])document.addEventListener(type,e=>events.push(type+':'+(e.key||e.button)))</script>`))
	}))
	defer fixture.Close()
	req := Request{Project: t.TempDir(), Thread: "live-shared", Timeout: 30 * time.Second}
	call := func(action string, params map[string]any) any {
		t.Helper()
		r := req
		r.Action = action
		r.Params = params
		data, err := mgr.dispatch(ctx, r)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return data
	}
	viewer := liveTestClient(t, ctx, mgr, req)
	receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && len(e.Tabs) == 0 })
	address := strings.TrimPrefix(fixture.URL, "http://")
	opened := call("open", map[string]any{"url": " " + address + "/first "}).(map[string]any)
	if opened["url"] != fixture.URL+"/first" {
		t.Fatalf("normalized open URL = %v", opened["url"])
	}
	state := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && len(e.Tabs) == 1 })
	first := state.Selected
	frame := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "frame" && e.TabID == first })
	if frame.Data == "" || frame.Width != 1440 || frame.Height != 900 {
		t.Fatalf("invalid frame: dimensions %v x %v, bytes %d", frame.Width, frame.Height, len(frame.Data))
	}
	for _, action := range []string{"open", "tab-new"} {
		r := req
		r.Action, r.Params = action, map[string]any{"url": "javascript:document.title='unsafe'"}
		if _, err := mgr.dispatch(ctx, r); err == nil || errorResponse(err).Error.Code != "invalid_params" {
			t.Fatalf("%s unsafe URL error = %v", action, err)
		}
	}

	send := func(c LiveCommand) {
		t.Helper()
		if err := viewer.Send(c); err != nil {
			t.Fatal(err)
		}
	}
	// A long agent wait must not serialize human input behind the agent lock.
	waitCtx, stopWait := context.WithCancel(ctx)
	defer stopWait()
	waitDone := make(chan error, 1)
	go func() {
		r := req
		r.Action = "wait"
		r.Params = map[string]any{"selector": "#human-finished"}
		_, err := mgr.dispatch(waitCtx, r)
		waitDone <- err
	}()
	time.Sleep(150 * time.Millisecond)
	send(LiveCommand{Type: "mouse", TabID: first, Event: "mousePressed", Button: "left", Buttons: 1, X: 50, Y: 35})
	send(LiveCommand{Type: "mouse", TabID: first, Event: "mouseReleased", Button: "left", X: 50, Y: 35})
	send(LiveCommand{Type: "text", TabID: first, Text: "human during wait"})
	s := mgr.project(req.Project, projectKey(req.Project))
	s.mu.Lock()
	session := s.sessions[req.Thread]
	s.mu.Unlock()
	tab, err := session.selectedTab()
	if err != nil {
		t.Fatal(err)
	}
	var screen struct{ Width, Height int }
	if err := liveRun(ctx, tab, chromedp.Evaluate(`({width:screen.width,height:screen.height})`, &screen)); err != nil {
		t.Fatal(err)
	}
	if screen.Width != 1440 || screen.Height != 900 {
		t.Fatalf("default screen does not match desktop viewport: %+v", screen)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var value string
		if err := liveRun(ctx, tab, chromedp.Evaluate(`document.querySelector('#field').value`, &value)); err != nil {
			t.Fatal(err)
		}
		if value == "human during wait" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("human input blocked by agent wait; value=%q", value)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-waitDone:
		t.Fatalf("wait completed before human finished: %v", err)
	default:
	}
	if err := liveRun(ctx, tab, chromedp.Evaluate(`document.querySelector('#field').id='human-finished'`, nil)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent wait did not observe human page change")
	}
	// Viewer pinning must survive agent selection changes.
	send(LiveCommand{Type: "watch", TabID: first})
	created := call("tab-new", map[string]any{"url": "//" + address + "/second"}).(map[string]any)
	if created["url"] != fixture.URL+"/second" {
		t.Fatalf("normalized tab-new URL = %v", created["url"])
	}
	second := created["id"].(string)
	receiveLive(t, viewer, func(e LiveEvent) bool {
		return e.Type == "state" && e.Selected == second && e.TabID == first && e.Pinned == first && len(e.Tabs) == 2
	})
	send(LiveCommand{Type: "navigate", TabID: first, URL: address + "/navigated"})
	receiveLive(t, viewer, func(e LiveEvent) bool {
		if e.Type != "state" {
			return false
		}
		for _, tab := range e.Tabs {
			if tab.ID == first && tab.URL == fixture.URL+"/navigated" {
				return true
			}
		}
		return false
	})
	if selected, _ := session.selectedTab(); string(selected.id) != second {
		t.Fatal("viewer navigation changed agent selection")
	}
	send(LiveCommand{Type: "back", TabID: first})
	receiveLive(t, viewer, func(e LiveEvent) bool {
		if e.Type != "state" {
			return false
		}
		for _, tab := range e.Tabs {
			if tab.ID == first && tab.URL == fixture.URL+"/first" {
				return true
			}
		}
		return false
	})
	send(LiveCommand{Type: "watch"})
	receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && e.TabID == second && e.Pinned == "" })
	// Recording and the viewer consume the same screencast across stop/start.
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		call("record-start", map[string]any{"fps": 5})
		call("fill", map[string]any{"selector": "#field", "value": "recorded"})
		receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "activity" && e.Kind == "fill" && e.TabID == second })
		time.Sleep(400 * time.Millisecond)
		result := call("record-stop", nil).(map[string]any)
		if result["frames"].(int) < 1 {
			t.Fatalf("recording has no frames: %+v", result)
		}
		call("fill", map[string]any{"selector": "#field", "value": "viewer after recording"})
		receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "frame" && e.TabID == second })
	}
	call("click", map[string]any{"selector": "#go"})
	receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "activity" && e.Kind == "click" && e.TabID == second })
	// Human actions do not synthesize agent activity.
	send(LiveCommand{Type: "key", TabID: second, Event: "keyDown", Key: "Shift", Code: "ShiftLeft", Modifiers: 8})
	send(LiveCommand{Type: "release", TabID: second})
	selected, _ := session.selectedTab()
	deadline = time.Now().Add(3 * time.Second)
	for {
		var released bool
		err := liveRun(ctx, selected, chromedp.Evaluate(`events.includes('keyup:Shift')`, &released))
		if err != nil {
			t.Fatal(err)
		}
		if released {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("release did not send keyUp")
		}
		time.Sleep(20 * time.Millisecond)
	}
	send(LiveCommand{Type: "close", TabID: first})
	receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && len(e.Tabs) == 1 && e.Selected == second })
	send(LiveCommand{Type: "release", TabID: first})
	send(LiveCommand{Type: "release", TabID: first})
	receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && len(e.Tabs) == 1 && e.Selected == second })

	otherReq := req
	otherReq.Thread = "other-thread"
	otherReq.Action = "open"
	otherReq.Params = map[string]any{"url": fixture.URL + "/other", "isolated": true}
	otherData, err := mgr.dispatch(ctx, otherReq)
	if err != nil {
		t.Fatal(err)
	}
	otherID := otherData.(map[string]any)["id"].(string)
	send(LiveCommand{Type: "navigate", TabID: otherID, URL: fixture.URL + "/forbidden"})
	_ = viewer.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		event, err := viewer.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			if event.Code != "not_found" {
				t.Fatalf("cross-thread input: %+v", event)
			}
			break
		}
	}
	_ = viewer.conn.SetReadDeadline(time.Time{})
	receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && len(e.Tabs) == 1 && e.Tabs[0].ID == second })
	send(LiveCommand{Type: "new", URL: address + "/viewer-new"})
	createdState := receiveLive(t, viewer, func(e LiveEvent) bool {
		return e.Type == "state" && len(e.Tabs) == 2 && e.Tabs[1].URL == fixture.URL+"/viewer-new"
	})
	send(LiveCommand{Type: "close", TabID: createdState.Tabs[1].ID})
	send(LiveCommand{Type: "watch"})
	receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && len(e.Tabs) == 1 && e.TabID == second })
	send(LiveCommand{Type: "key", TabID: second, Event: "keyDown", Key: "Alt", Code: "AltLeft", Modifiers: 1})
	deadline = time.Now().Add(3 * time.Second)
	for {
		var pressed bool
		if err := liveRun(ctx, selected, chromedp.Evaluate(`events.includes('keydown:Alt')`, &pressed)); err != nil {
			t.Fatal(err)
		}
		if pressed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("keyDown was not delivered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = viewer.Close()
	deadline = time.Now().Add(3 * time.Second)
	for {
		var released bool
		if err := liveRun(ctx, selected, chromedp.Evaluate(`events.includes('keyup:Alt')`, &released)); err != nil {
			t.Fatal(err)
		}
		if released {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnect did not release held key")
		}
		time.Sleep(20 * time.Millisecond)
	}

}

func TestLiveConcurrentViewerAndAgentSessionCreationIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mgr := newManager(ctx, t.TempDir())
	defer mgr.close()
	root, err := ProjectRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := projectKey(root)
	project := mgr.project(root, key)
	if err := project.ensureStarted(ctx); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		command   LiveCommand
		wantTabs  int
		wantError string
	}{
		{name: "new", command: LiveCommand{Type: "new", URL: "data:text/html,<title>viewer</title>"}, wantTabs: 2},
		{name: "initial navigate", command: LiveCommand{Type: "navigate", URL: "data:text/html,<title>viewer</title>"}, wantTabs: 1, wantError: "invalid_params"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thread := "creation-race-" + strings.ReplaceAll(tc.name, " ", "-")
			sub := &liveSubscription{manager: mgr, root: root, key: key, thread: thread}
			observed := sub.session()
			if observed != nil {
				t.Fatal("test thread already has a session")
			}

			project.mu.Lock()
			type sessionResult struct {
				s       *session
				created bool
				err     error
			}
			agentResult := make(chan sessionResult, 1)
			agentStarted := make(chan struct{})
			go func() {
				close(agentStarted)
				s, created, err := project.getSession(ctx, thread, false)
				agentResult <- sessionResult{s: s, created: created, err: err}
			}()
			<-agentStarted
			time.Sleep(5 * time.Millisecond)
			viewerResult := make(chan error, 1)
			viewerStarted := make(chan struct{})
			go func() {
				close(viewerStarted)
				viewerResult <- sub.open(ctx, tc.command, observed)
			}()
			<-viewerStarted
			time.Sleep(5 * time.Millisecond)
			project.mu.Unlock()

			agent := <-agentResult
			if agent.err != nil || !agent.created {
				t.Fatalf("agent session creation: created=%v, err=%v", agent.created, agent.err)
			}
			viewerErr := <-viewerResult
			if response := errorResponse(viewerErr); tc.wantError == "" {
				if viewerErr != nil {
					t.Fatalf("viewer command: %v", viewerErr)
				}
			} else if response.Error == nil || response.Error.Code != tc.wantError {
				t.Fatalf("viewer error = %+v, want %s", response.Error, tc.wantError)
			}
			tabs, selected := agent.s.tabSnapshot()
			if len(tabs) != tc.wantTabs {
				t.Fatalf("tabs after concurrent creation = %d, want %d", len(tabs), tc.wantTabs)
			}
			if tc.command.Type == "new" {
				sub.mu.Lock()
				watch := sub.watch
				sub.mu.Unlock()
				if watch == "" || watch == string(selected) {
					t.Fatalf("new did not create and pin a distinct viewer tab: watch=%q selected=%q", watch, selected)
				}
			}
		})
	}
}

func TestLivePageCreatedTabsIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mgr := newManager(ctx, t.TempDir())
	defer mgr.close()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			_, _ = w.Write([]byte(`<title>` + r.URL.Path + `</title>`))
			return
		}
		_, _ = w.Write([]byte(`<title>opener</title>
			<a id="link" href="/link-popup" target="_blank">link popup</a>
			<button id="script" onclick="window.open('/script-popup','_blank')">script popup</button>`))
	}))
	defer fixture.Close()
	project := t.TempDir()
	ownerReq := Request{Project: project, Thread: "popup-owner", Timeout: 20 * time.Second}
	otherReq := Request{Project: project, Thread: "popup-other", Timeout: 20 * time.Second}
	call := func(req Request, action string, params map[string]any) any {
		t.Helper()
		req.Action, req.Params = action, params
		data, err := mgr.dispatch(ctx, req)
		if err != nil {
			t.Fatalf("%s %s: %v", req.Thread, action, err)
		}
		return data
	}
	call(ownerReq, "open", map[string]any{"url": fixture.URL})
	other := call(otherReq, "open", map[string]any{"url": fixture.URL + "/other"}).(map[string]any)
	viewer := liveTestClient(t, ctx, mgr, ownerReq)
	initial := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && len(e.Tabs) == 1 })
	rootID := initial.Tabs[0].ID

	call(ownerReq, "click", map[string]any{"selector": "#link"})
	linkState := receiveLive(t, viewer, func(e LiveEvent) bool {
		if e.Type != "state" || len(e.Tabs) != 2 {
			return false
		}
		for _, tab := range e.Tabs {
			if tab.URL == fixture.URL+"/link-popup" {
				return true
			}
		}
		return false
	})
	var linkID string
	for _, tab := range linkState.Tabs {
		if tab.URL == fixture.URL+"/link-popup" {
			linkID = tab.ID
		}
	}

	call(ownerReq, "click", map[string]any{"selector": "#script"})
	scriptState := receiveLive(t, viewer, func(e LiveEvent) bool {
		if e.Type != "state" || len(e.Tabs) != 3 {
			return false
		}
		for _, tab := range e.Tabs {
			if tab.URL == fixture.URL+"/script-popup" {
				return true
			}
		}
		return false
	})
	var scriptID string
	for _, tab := range scriptState.Tabs {
		if tab.URL == fixture.URL+"/script-popup" {
			scriptID = tab.ID
		}
	}
	if linkID == "" || scriptID == "" {
		t.Fatalf("popup state did not identify both tabs: %+v", scriptState)
	}
	if err := viewer.Send(LiveCommand{Type: "watch", TabID: scriptID}); err != nil {
		t.Fatal(err)
	}
	receiveLive(t, viewer, func(e LiveEvent) bool {
		return e.Type == "state" && e.TabID == scriptID && e.Pinned == scriptID
	})
	frame := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "frame" && e.TabID == scriptID })
	if frame.Data == "" || frame.Width != 1440 || frame.Height != 900 {
		t.Fatalf("popup frame lacks the desktop viewport: %gx%g", frame.Width, frame.Height)
	}

	otherTabs := call(otherReq, "tabs", nil).(map[string]any)["tabs"].([]map[string]any)
	if len(otherTabs) != 1 || otherTabs[0]["id"] != other["id"] {
		t.Fatalf("other thread acquired popup targets: %+v", otherTabs)
	}

	projectBrowser := mgr.project(project, projectKey(project))
	projectBrowser.mu.Lock()
	ownerSession := projectBrowser.sessions[ownerReq.Thread]
	projectBrowser.mu.Unlock()
	root, err := ownerSession.selectedTab()
	if err != nil {
		t.Fatal(err)
	}
	browser := chromedp.FromContext(root.ctx).Browser
	closeCtx, stopClose := context.WithTimeout(ctx, 3*time.Second)
	err = target.CloseTarget(target.ID(linkID)).Do(cdp.WithExecutor(closeCtx, browser))
	stopClose()
	if err != nil {
		t.Fatal(err)
	}
	receiveLive(t, viewer, func(e LiveEvent) bool {
		if e.Type != "state" || len(e.Tabs) != 2 {
			return false
		}
		for _, tab := range e.Tabs {
			if tab.ID == linkID {
				return false
			}
		}
		return true
	})

	closed := call(ownerReq, "session-close", nil).(map[string]any)
	if closed["closed"] != true {
		t.Fatalf("session close: %+v", closed)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		queryCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		infos, queryErr := target.GetTargets().Do(cdp.WithExecutor(queryCtx, browser))
		stop()
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		remaining := make(map[string]bool)
		for _, info := range infos {
			remaining[string(info.TargetID)] = true
		}
		if !remaining[rootID] && !remaining[linkID] && !remaining[scriptID] {
			if !remaining[other["id"].(string)] {
				t.Fatal("closing popup owner also closed the other thread")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owner targets survived session close: root=%v link=%v script=%v", remaining[rootID], remaining[linkID], remaining[scriptID])
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestSessionCleanupAfterProjectDirectoryMoveIntegration(t *testing.T) {
	ctx, s, tab := liveTestSession(t)
	root := s.project.root
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := s.project.manager.dispatch(ctx, Request{Project: root, Thread: s.thread, Action: "session-close"}); err != nil {
		t.Fatalf("moved root prevented browser cleanup: %v", err)
	}
	infos, err := target.GetTargets().Do(cdp.WithExecutor(ctx, chromedp.FromContext(s.project.browserCtx).Browser))
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.TargetID == tab.id {
			t.Fatal("Chrome target survived cleanup after directory move")
		}
	}
}

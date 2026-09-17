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
	"testing"
	"time"

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
	call("open", map[string]any{"url": fixture.URL + "/first"})
	state := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && len(e.Tabs) == 1 })
	first := state.Selected
	frame := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "frame" && e.TabID == first })
	if frame.Data == "" || frame.Width <= 0 || frame.Height <= 0 {
		t.Fatalf("invalid frame: dimensions %v x %v, bytes %d", frame.Width, frame.Height, len(frame.Data))
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
	second := call("tab-new", map[string]any{"url": fixture.URL + "/second"}).(map[string]any)["id"].(string)
	receiveLive(t, viewer, func(e LiveEvent) bool {
		return e.Type == "state" && e.Selected == second && e.TabID == first && e.Pinned == first && len(e.Tabs) == 2
	})
	send(LiveCommand{Type: "navigate", TabID: first, URL: fixture.URL + "/navigated"})
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

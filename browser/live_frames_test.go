package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func TestLiveFramesReplaceLatestAndFeedRecording(t *testing.T) {
	tab := &browserTab{id: "tab-a", ctx: context.Background()}
	r := &recording{}
	tab.rec = r
	for _, data := range []string{"first", "second", "newest"} {
		tab.handleRecordingEvent(&page.EventScreencastFrame{Data: data, Metadata: &page.ScreencastFrameMetadata{DeviceWidth: 1000, DeviceHeight: 700}, SessionID: 1})
	}
	if frame := tab.liveFrame; frame.Type != "frame" || frame.TabID != "tab-a" || frame.Data != "newest" || frame.Width != 1000 || frame.Height != 700 {
		t.Fatalf("latest frame: %+v", frame)
	}
	if r.latestFrame != "newest" {
		t.Fatalf("recording frame = %q", r.latestFrame)
	}
	tab.rec = nil
	tab.handleRecordingEvent(&page.EventScreencastFrame{Data: "viewer-only", SessionID: 2})
	if tab.liveFrame.Data != "viewer-only" || r.latestFrame != "newest" {
		t.Fatal("viewer delivery must not depend on an active recording")
	}
}

func TestLiveActivityIsTabScopedAndNavigationClears(t *testing.T) {
	tab := &browserTab{id: "tab-a"}
	other := &browserTab{id: "tab-b"}
	tab.activity("click", 12.5, 98)
	other.activity("fill", 55, 25)
	tab.handleRecordingEvent(&page.EventFrameNavigated{Frame: &cdp.Frame{ParentID: "parent"}})
	if tab.liveActivity.Kind != "click" {
		t.Fatal("subframe navigation cleared top-level activity")
	}
	tab.handleRecordingEvent(&page.EventFrameNavigated{Frame: &cdp.Frame{}})
	if event := tab.liveActivity; event.Kind != "clear" || event.TabID != "tab-a" || event.X != 0 || event.Y != 0 || tab.liveActivitySeq != 2 {
		t.Fatalf("navigation activity: %+v, sequence %d", event, tab.liveActivitySeq)
	}
	if other.liveActivity.Kind != "fill" || other.liveActivitySeq != 1 {
		t.Fatal("navigation modified another tab")
	}
	tab.activity("move", 4, 5)
	tab.handleRecordingEvent(&page.EventNavigatedWithinDocument{})
	if tab.liveActivity.Kind != "clear" || tab.liveActivitySeq != 4 {
		t.Fatal("same-document navigation did not clear activity")
	}
	for _, kind := range []string{"text", "password", "key"} {
		tab.activity(kind, 1, 2)
	}
	tab.activity("click", math.NaN(), 0)
	tab.activity("fill", 0, math.Inf(1))
	if tab.liveActivitySeq != 4 {
		t.Fatal("invalid activity was retained")
	}
}

func TestLiveActivityConcurrentLatest(t *testing.T) {
	tab := &browserTab{id: "tab-a"}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				tab.activity("move", float64(j), 1)
				tab.liveMu.Lock()
				if tab.liveActivity.TabID != "tab-a" || tab.liveActivity.Type != "activity" {
					t.Error("incomplete activity event")
				}
				tab.liveMu.Unlock()
			}
		})
	}
	wg.Wait()
	if tab.liveActivitySeq != 1000 {
		t.Fatalf("lost updates: %d", tab.liveActivitySeq)
	}
}

func TestLiveStreamOwnershipFailures(t *testing.T) {
	ctx := context.Background()
	tab := &browserTab{ctx: ctx}
	if err := tab.acquireStream(ctx); err == nil || tab.streamUsers != 0 {
		t.Fatalf("failed start acquired ownership: %v, %d", err, tab.streamUsers)
	}
	// An existing stream does not need another CDP StartScreencast.
	tab.streamUsers = 1
	if err := tab.acquireStream(ctx); err != nil || tab.streamUsers != 2 {
		t.Fatalf("shared acquire: %v, %d", err, tab.streamUsers)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := tab.acquireStream(canceled); err == nil || tab.streamUsers != 2 {
		t.Fatalf("canceled acquire: %v, %d", err, tab.streamUsers)
	}
	if err := tab.releaseStream(ctx); err != nil || tab.streamUsers != 1 {
		t.Fatalf("intermediate release stopped Chrome: %v, %d", err, tab.streamUsers)
	}
	if err := tab.releaseStream(ctx); err == nil || tab.streamUsers != 0 {
		t.Fatalf("failed stop retained phantom ownership: %v, %d", err, tab.streamUsers)
	}
	if err := tab.releaseStream(ctx); err != nil || tab.streamUsers != 0 {
		t.Fatalf("unbalanced release: %v, %d", err, tab.streamUsers)
	}
}

func liveTestSession(t *testing.T) (context.Context, *session, *browserTab) {
	t.Helper()
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 with Chrome available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	m := newManager(ctx, t.TempDir())
	t.Cleanup(m.close)
	root, err := ProjectRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.dispatch(ctx, Request{Project: root, Thread: "live-frames", Action: "open", Params: map[string]any{"url": `data:text/html,<style>body{margin:0}button,input{position:absolute;left:100px;top:100px;width:120px;height:40px}input{top:180px}</style><button id="button" onclick="window.actualClick=[event.clientX,event.clientY];this.remove()">Click</button><input id="field" type="password"><div id="plain" style="position:absolute;top:250px">Not editable</div>`}})
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := m.project(root, projectKey(root)).getSession(ctx, "live-frames", false)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := s.selectedTab()
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s, tab
}

func waitLiveFrame(t *testing.T, tab *browserTab, previous string) LiveEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tab.liveMu.Lock()
		frame := tab.liveFrame
		tab.liveMu.Unlock()
		if frame.Data != "" && frame.Data != previous {
			return frame
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no fresh screencast frame")
	return LiveEvent{}
}

func TestLiveRecordingSharedStreamIntegration(t *testing.T) {
	ctx, s, tab := liveTestSession(t)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	users := func(want int) {
		t.Helper()
		tab.streamMu.Lock()
		defer tab.streamMu.Unlock()
		if tab.streamUsers != want {
			t.Fatalf("stream users = %d, want %d", tab.streamUsers, want)
		}
	}
	acquire := func() {
		t.Helper()
		if err := tab.acquireStream(ctx); err != nil {
			t.Fatal(err)
		}
	}
	release := func() {
		t.Helper()
		if err := tab.releaseStream(ctx); err != nil {
			t.Fatal(err)
		}
	}
	startRecording := func() {
		t.Helper()
		s.recordMu.Lock()
		defer s.recordMu.Unlock()
		if _, err := s.startRecordingLocked(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	stopRecording := func() {
		t.Helper()
		s.recordMu.Lock()
		defer s.recordMu.Unlock()
		result, err := s.stopRecordingLocked(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if result.(map[string]any)["frames"].(int) == 0 {
			t.Fatal("recording did not receive shared frames")
		}
	}
	acquire()
	first := waitLiveFrame(t, tab, "")
	if first.Width <= 0 || first.Height <= 0 || first.TabID != string(tab.id) {
		t.Fatalf("invalid frame metadata: %+v", first)
	}
	acquire()
	startRecording()
	users(3)
	s.recording.mu.Lock()
	seed := s.recording.latestFrame
	s.recording.mu.Unlock()
	if seed == "" {
		t.Fatal("idle stream did not seed recording")
	}
	release()
	users(2)
	stopRecording()
	users(1)
	if err := s.runTab(ctx, tab, chromedp.Evaluate(`document.body.style.background='red'`, nil)); err != nil {
		t.Fatal(err)
	}
	waitLiveFrame(t, tab, first.Data)
	release()
	users(0)

	startRecording()
	waitLiveFrame(t, tab, "")
	acquire()
	users(2)
	release()
	users(1)
	stopRecording()
	users(0)
}

func TestLiveTargetActivityIntegration(t *testing.T) {
	ctx, s, tab := liveTestSession(t)
	activity := func() (LiveEvent, uint64) {
		tab.liveMu.Lock()
		defer tab.liveMu.Unlock()
		return tab.liveActivity, tab.liveActivitySeq
	}
	if _, err := s.click(ctx, map[string]any{"selector": "#button"}); err != nil {
		t.Fatal(err)
	}
	var actual []float64
	if err := s.runTab(ctx, tab, chromedp.Evaluate(`window.actualClick`, &actual)); err != nil {
		t.Fatal(err)
	}
	click, _ := activity()
	if len(actual) != 2 || click.Kind != "click" || math.Abs(click.X-actual[0]) > 1 || math.Abs(click.Y-actual[1]) > 1 {
		t.Fatalf("activity %+v differs from actual dispatched click %v", click, actual)
	}
	if _, err := s.fill(ctx, map[string]any{"selector": "#field", "value": "never-telemetry-password"}); err != nil {
		t.Fatal(err)
	}
	fill, seq := activity()
	if fill.Kind != "fill" || fill.X < 100 || fill.X > 230 || fill.Y < 180 || fill.Y > 230 {
		t.Fatalf("fill coordinates: %+v", fill)
	}
	encoded, err := json.Marshal(fill)
	if err != nil || strings.Contains(string(encoded), "never-telemetry") {
		t.Fatalf("unsafe activity: %s, %v", encoded, err)
	}
	if _, err := s.fill(ctx, map[string]any{"selector": "#plain", "value": "fail"}); err == nil {
		t.Fatal("fill unexpectedly succeeded")
	}
	if _, current := activity(); current != seq {
		t.Fatal("failed action produced activity")
	}
	if err := s.runTab(ctx, tab, chromedp.Navigate("data:text/html,new-document")); err != nil {
		t.Fatal(err)
	}
	if event, _ := activity(); event.Kind != "clear" {
		t.Fatalf("navigation retained activity: %+v", event)
	}
}

func TestLiveReleaseRetryIntegration(t *testing.T) {
	ctx, s, tab := liveTestSession(t)
	if err := s.runTab(ctx, tab, chromedp.Evaluate(`window.releaseEvents=[];for(const type of ['mouseup','keyup'])document.addEventListener(type,event=>releaseEvents.push(type+':'+(event.key||event.button)))`, nil)); err != nil {
		t.Fatal(err)
	}
	sub := &liveSubscription{inputs: make(map[*browserTab]*liveInputState)}
	defer sub.release(ctx, tab)
	mouse := LiveCommand{Type: "mouse", TabID: string(tab.id), Event: "mousePressed", Button: "left", Buttons: 1, X: 10, Y: 10}
	key := LiveCommand{Type: "key", TabID: string(tab.id), Event: "keyDown", Key: "Shift", Code: "ShiftLeft", Modifiers: 8}
	for _, command := range []LiveCommand{mouse, key} {
		if err := dispatchLiveInput(ctx, tab, command); err != nil {
			t.Fatal(err)
		}
		sub.track(tab, command)
	}

	failed, cancel := context.WithCancel(ctx)
	cancel()
	if err := sub.release(failed, tab); err == nil {
		t.Fatal("canceled release unexpectedly succeeded")
	}
	state := sub.inputs[tab]
	if state == nil || len(state.buttons) != 1 || len(state.keys) != 1 {
		t.Fatalf("failed releases were discarded: %+v", state)
	}
	if err := sub.release(ctx, tab); err != nil {
		t.Fatal(err)
	}
	if _, ok := sub.inputs[tab]; ok {
		t.Fatal("successful retry retained released inputs")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		var events []string
		if err := s.runTab(ctx, tab, chromedp.Evaluate(`window.releaseEvents`, &events)); err != nil {
			t.Fatal(err)
		}
		mouseUp, keyUp := false, false
		for _, event := range events {
			mouseUp = mouseUp || event == "mouseup:0"
			keyUp = keyUp || event == "keyup:Shift"
		}
		if mouseUp && keyUp {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("retry did not deliver both releases: %v", events)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLiveFramePreservesResolutionAndCSSViewportIntegration(t *testing.T) {
	ctx, s, tab := liveTestSession(t)
	if err := s.runTab(ctx, tab, emulation.SetDeviceMetricsOverride(2400, 1400, 2, false)); err != nil {
		t.Fatal(err)
	}
	if err := tab.acquireStream(ctx); err != nil {
		t.Fatal(err)
	}
	defer tab.releaseStream(ctx)
	frame := waitLiveFrame(t, tab, "")
	if frame.Width != 2400 || frame.Height != 1400 {
		t.Fatalf("frame dimensions must be CSS viewport, got %gx%g", frame.Width, frame.Height)
	}
	data, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil {
		t.Fatal(err)
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width < 2400 || config.Height < 1400 {
		t.Fatalf("shared stream downsampled recording: %dx%d", config.Width, config.Height)
	}
}

func TestScreencastAcksSerializeAndReplaceObsoleteSessions(t *testing.T) {
	var a screencastAcks
	calls := make(chan int64, 110)
	unblock := make(chan struct{})
	defer close(unblock)
	acknowledge := func(id int64) {
		calls <- id
		<-unblock
	}
	receive := func(want int64) {
		t.Helper()
		select {
		case id := <-calls:
			if id != want {
				t.Fatalf("ACK session = %d, want %d", id, want)
			}
		case <-time.After(time.Second):
			t.Fatal("ACK worker did not run")
		}
	}
	a.offer(10, acknowledge)
	receive(10)
	for i := 0; i < 100; i++ {
		a.offer(10, acknowledge)
	}
	a.offer(11, acknowledge)
	a.offer(11, acknowledge)
	a.mu.Lock()
	pending, sessionID := a.pending, a.sessionID
	a.mu.Unlock()
	if pending != 2 || sessionID != 11 {
		t.Fatalf("obsolete session retained: pending=%d, session=%d", pending, sessionID)
	}
	select {
	case <-calls:
		t.Fatal("more than one concurrent ACK")
	default:
	}
	unblock <- struct{}{}
	receive(11)
	unblock <- struct{}{}
	receive(11)
}

func TestScreencastACKSessionIDContractIntegration(t *testing.T) {
	ctx, s, tab := liveTestSession(t)
	frames := make(chan int64, 32)
	chromedp.ListenTarget(tab.ctx, func(event any) {
		if frame, ok := event.(*page.EventScreencastFrame); ok {
			select {
			case frames <- frame.SessionID:
			default:
			}
		}
	})
	if err := s.runTab(ctx, tab, chromedp.Evaluate(`let hue = 0; setInterval(() => { document.body.style.background = 'hsl(' + (hue++ * 37 % 360) + ',100%,50%)' }, 50)`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := tab.acquireStream(ctx); err != nil {
		t.Fatal(err)
	}
	defer tab.releaseStream(ctx)
	var sessionID int64
	deadline := time.After(5 * time.Second)
	// More than Chrome's three-frame flight window proves ACKs keep flowing.
	for i := 0; i < 12; i++ {
		select {
		case id := <-frames:
			if i == 0 {
				sessionID = id
			} else if id != sessionID {
				t.Fatalf("Chrome changed ID within a screencast: %d -> %d", sessionID, id)
			}
		case <-deadline:
			t.Fatalf("screencast stalled after %d frames", i)
		}
	}
}

func TestLiveFillCoordinatesAfterFocusIntegration(t *testing.T) {
	ctx, s, tab := liveTestSession(t)
	if err := s.runTab(ctx, tab, page.BringToFront(), chromedp.Evaluate(`document.querySelector('#field').blur();document.querySelector('#field').onfocus=function(){this.style.top='350px'}`, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fill(ctx, map[string]any{"selector": "#field", "value": "value"}); err != nil {
		t.Fatal(err)
	}
	tab.liveMu.Lock()
	activity := tab.liveActivity
	tab.liveMu.Unlock()
	if activity.Kind != "fill" || activity.Y < 350 || activity.Y > 400 {
		t.Fatalf("fill used pre-focus coordinates: %+v", activity)
	}
}

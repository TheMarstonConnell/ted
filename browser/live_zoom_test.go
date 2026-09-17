package browser

import (
	"bytes"
	"encoding/base64"
	"image/jpeg"
	"math"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

type liveColorBounds struct{ x0, y0, x1, y1, width, height int }

func liveFrameColorBounds(t *testing.T, frame LiveEvent, blue bool) liveColorBounds {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b := liveColorBounds{x0: img.Bounds().Dx(), y0: img.Bounds().Dy(), x1: -1, y1: -1, width: img.Bounds().Dx(), height: img.Bounds().Dy()}
	for y := range b.height {
		for x := range b.width {
			r, g, bl, _ := img.At(x, y).RGBA()
			matches := r > 50000 && g < 10000 && bl < 10000
			if blue {
				matches = bl > 50000 && g < 10000 && r < 10000
			}
			if matches {
				b.x0 = min(b.x0, x)
				b.y0 = min(b.y0, y)
				b.x1 = max(b.x1, x)
				b.y1 = max(b.y1, y)
			}
		}
	}
	if b.x1 < 0 {
		t.Fatal("fixture target is missing from actual screencast pixels")
	}
	return b
}

func (b liveColorBounds) cssCenter(frame LiveEvent) (float64, float64) {
	return float64(b.x0+b.x1+1) / 2 * frame.Width / float64(b.width), float64(b.y0+b.y1+1) / 2 * frame.Height / float64(b.height)
}

func TestLiveZoomCoordinatesIntegration(t *testing.T) {
	ctx, s, tab := liveTestSession(t)
	if err := s.runTab(ctx, tab, emulation.SetDeviceMetricsOverride(800, 600, 2, false), emulation.SetPageScaleFactor(2), chromedp.Evaluate(`document.body.innerHTML='<div style="height:3000px;width:3000px"></div><button id="hit" style="position:absolute;left:100px;top:100px;width:100px;height:50px;background:red;border:0" onclick="window.hitCount++">Hit</button><input id="zoom-field" style="position:absolute;left:100px;top:180px;width:100px;height:40px;box-sizing:border-box;border:0;background:blue">';window.hitCount=0;`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := tab.acquireStream(ctx); err != nil {
		t.Fatal(err)
	}
	defer tab.releaseStream(ctx)
	_ = waitLiveFrame(t, tab, "")
	for _, pan := range []bool{false, true} {
		if pan {
			if err := s.runTab(ctx, tab, input.SynthesizeScrollGesture(200, 200).WithYDistance(-50).WithXDistance(-50).WithGestureSourceType(input.GestureMouse)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		var viewport struct{ Width, Height, OffsetX, OffsetY float64 }
		if err := s.runTab(ctx, tab, chromedp.Evaluate(`({width:visualViewport.width,height:visualViewport.height,offsetX:visualViewport.offsetLeft,offsetY:visualViewport.offsetTop})`, &viewport)); err != nil {
			t.Fatal(err)
		}
		if pan && (viewport.OffsetX < 40 || viewport.OffsetY < 40) {
			t.Fatalf("fixture did not pan the zoomed visual viewport: %+v", viewport)
		}
		tab.liveMu.Lock()
		frame := tab.liveFrame
		tab.liveMu.Unlock()
		if math.Abs(frame.Width-viewport.Width) > 1 || math.Abs(frame.Height-viewport.Height) > 1 {
			t.Fatalf("frame extents %gx%g do not match CSS visual viewport %+v", frame.Width, frame.Height, viewport)
		}
		bounds := liveFrameColorBounds(t, frame, false)
		if bounds.width != 800 || bounds.height != 600 {
			t.Fatalf("zoom normalization changed recording pixels: %+v", bounds)
		}
		x, y := bounds.cssCenter(frame)
		command := LiveCommand{Type: "mouse", TabID: string(tab.id), Event: "mousePressed", Button: "left", Buttons: 1, X: x, Y: y}
		if err := dispatchLiveInput(ctx, tab, command); err != nil {
			t.Fatal(err)
		}
		command.Event = "mouseReleased"
		command.Buttons = 0
		if err := dispatchLiveInput(ctx, tab, command); err != nil {
			t.Fatal(err)
		}
		var hits int
		if err := s.runTab(ctx, tab, chromedp.Evaluate(`window.hitCount`, &hits)); err != nil {
			t.Fatal(err)
		}
		want := 1
		if pan {
			want = 2
		}
		if hits != want {
			t.Fatalf("UI raster-to-CSS click missed zoomed target: pan=%v point=%g,%g hits=%d bounds=%+v", pan, x, y, hits, bounds)
		}
		if _, err := s.cdpCall(ctx, map[string]any{"method": "Input.dispatchMouseEvent", "params": map[string]any{"type": "mouseMoved", "x": x, "y": y}}); err != nil {
			t.Fatal(err)
		}
		tab.liveMu.Lock()
		activity := tab.liveActivity
		tab.liveMu.Unlock()
		projectedX := activity.X / frame.Width * float64(bounds.width)
		projectedY := activity.Y / frame.Height * float64(bounds.height)
		if projectedX < float64(bounds.x0) || projectedX > float64(bounds.x1) || projectedY < float64(bounds.y0) || projectedY > float64(bounds.y1) {
			t.Fatalf("raw-CDP cursor misses visible target: %+v bounds=%+v", activity, bounds)
		}
	}
	if _, err := s.fill(ctx, map[string]any{"selector": "#zoom-field", "value": "zoomed input"}); err != nil {
		t.Fatal(err)
	}
	var expected struct{ X, Y float64 }
	if err := s.runTab(ctx, tab, chromedp.Evaluate(`(()=>{const r=document.querySelector('#zoom-field').getBoundingClientRect();return {x:r.left+r.width/2-visualViewport.offsetLeft,y:r.top+r.height/2-visualViewport.offsetTop}})()`, &expected)); err != nil {
		t.Fatal(err)
	}
	tab.liveMu.Lock()
	activity := tab.liveActivity
	tab.liveMu.Unlock()
	if activity.Kind != "fill" || math.Abs(activity.X-expected.X) > 1 || math.Abs(activity.Y-expected.Y) > 1 {
		t.Fatalf("fill cursor used layout coordinates instead of visual coordinates: %+v expected %+v", activity, expected)
	}
	time.Sleep(100 * time.Millisecond)
	tab.liveMu.Lock()
	frame := tab.liveFrame
	tab.liveMu.Unlock()
	bounds := liveFrameColorBounds(t, frame, true)
	x, y := bounds.cssCenter(frame)
	if math.Abs(activity.X-x) > 2 || math.Abs(activity.Y-y) > 2 {
		t.Fatalf("fill overlay does not align with zoomed pixels: %+v raster center=%g,%g", activity, x, y)
	}

	if _, err := s.click(ctx, map[string]any{"selector": "#hit"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	tab.liveMu.Lock()
	frame = tab.liveFrame
	activity = tab.liveActivity
	tab.liveMu.Unlock()
	bounds = liveFrameColorBounds(t, frame, false)
	x, y = bounds.cssCenter(frame)
	var hits int
	if err := s.runTab(ctx, tab, chromedp.Evaluate(`window.hitCount`, &hits)); err != nil {
		t.Fatal(err)
	}
	if hits != 3 || activity.Kind != "click" || math.Abs(activity.X-x) > 2 || math.Abs(activity.Y-y) > 2 {
		t.Fatalf("high-level click/overlay missed zoomed target: hits=%d activity=%+v raster center=%g,%g", hits, activity, x, y)
	}
}

func TestLiveFrameScaleAndTopOffset(t *testing.T) {
	tab := &browserTab{id: "tab", ctx: t.Context()}
	recording := &recording{}
	tab.rec = recording
	tab.handleRecordingEvent(&page.EventScreencastFrame{Data: "unchanged-recording-pixels", Metadata: &page.ScreencastFrameMetadata{DeviceWidth: 800, DeviceHeight: 600, PageScaleFactor: 2, OffsetTop: 40}})
	if tab.liveFrame.Width != 400 || tab.liveFrame.Height != 300 || tab.liveOffsetTop != 20 {
		t.Fatalf("frame geometry %+v offset=%g", tab.liveFrame, tab.liveOffsetTop)
	}
	if recording.latestFrame != "unchanged-recording-pixels" {
		t.Fatal("frame normalization changed recording")
	}
	tab.activity("move", 50, 60)
	if tab.liveActivity.X != 50 || tab.liveActivity.Y != 80 {
		t.Fatalf("cursor offset %+v", tab.liveActivity)
	}
	tab.handleRecordingEvent(&page.EventScreencastFrame{Metadata: &page.ScreencastFrameMetadata{DeviceWidth: 800, DeviceHeight: 600}})
	if tab.liveFrame.Width != 800 || tab.liveFrame.Height != 600 || tab.liveOffsetTop != 0 {
		t.Fatal("unscaled frame retained stale scale/offset")
	}
}

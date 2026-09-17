package browser

import (
	"bytes"
	"encoding/base64"
	"image/jpeg"
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

func TestDesktopViewportIntegration(t *testing.T) {
	ctx, s, tab := liveTestSession(t)
	var viewport struct {
		Width, Height, ScreenWidth, ScreenHeight, DPR float64
		Desktop                                       bool
	}
	if err := s.runTab(ctx, tab, chromedp.Evaluate(`({width:innerWidth,height:innerHeight,screenWidth:screen.width,screenHeight:screen.height,dpr:devicePixelRatio,desktop:matchMedia('(min-width:1200px)').matches})`, &viewport)); err != nil {
		t.Fatal(err)
	}
	if viewport.Width != 1440 || viewport.Height != 900 || viewport.ScreenWidth != 1440 || viewport.ScreenHeight != 900 || viewport.DPR != 1 || !viewport.Desktop {
		t.Fatalf("unexpected desktop environment: %+v", viewport)
	}
	if err := tab.acquireStream(ctx); err != nil {
		t.Fatal(err)
	}
	frame := waitLiveFrame(t, tab, "")
	if err := tab.releaseStream(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil {
		t.Fatal(err)
	}
	image, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if image.Width != 1440 || image.Height != 900 || frame.Width != 1440 || frame.Height != 900 {
		t.Fatalf("live stream lost desktop resolution: raster=%+v geometry=%gx%g", image, frame.Width, frame.Height)
	}

	// Viewing must not reset an agent's explicit mobile/device emulation.
	if err := s.runTab(ctx, tab, emulation.SetDeviceMetricsOverride(390, 844, 2, true)); err != nil {
		t.Fatal(err)
	}
	if err := tab.acquireStream(ctx); err != nil {
		t.Fatal(err)
	}
	defer tab.releaseStream(ctx)
	var width float64
	if err := s.runTab(ctx, tab, chromedp.Evaluate(`screen.width`, &width)); err != nil {
		t.Fatal(err)
	}
	if width != 390 {
		t.Fatalf("viewing reset custom emulation: screen.width=%g", width)
	}
}

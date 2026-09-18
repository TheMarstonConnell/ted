//go:build !windows

package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/jpeg"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Exercise managed Xvfb when Linux has no inherited display.
func headedTestExecutable(t *testing.T) string {
	t.Helper()
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1")
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" {
		if _, err := exec.LookPath("Xvfb"); err != nil {
			t.Fatal("headed tests require Xvfb or a private DISPLAY")
		}
	}
	executable := os.Getenv("TED_BROWSER_HEADED_TEST_EXECUTABLE")
	if executable == "" {
		for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
			if path, err := exec.LookPath(name); err == nil {
				executable = path
				break
			}
		}
	}
	if executable == "" {
		t.Fatal("set TED_BROWSER_HEADED_TEST_EXECUTABLE to a Chrome executable")
	}
	return executable
}

func assertViewportCSS(t *testing.T, ctx context.Context, width, height int) {
	t.Helper()
	var got struct {
		Width, Height int
		DPR           float64
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`({width:innerWidth,height:innerHeight,dpr:devicePixelRatio})`, &got)); err != nil {
		t.Fatal(err)
	}
	if got.Width != width || got.Height != height || got.DPR != 1 {
		t.Fatalf("CSS viewport = %+v, want %dx%d at DPR 1", got, width, height)
	}
}

func assertFirstViewportRaster(t *testing.T, ctx context.Context, width, height int) {
	t.Helper()
	listenCtx, stopListen := context.WithCancel(ctx)
	defer stopListen()
	frames := make(chan *page.EventScreencastFrame, 1)
	chromedp.ListenTarget(listenCtx, func(event any) {
		if frame, ok := event.(*page.EventScreencastFrame); ok {
			select {
			case frames <- frame:
			default:
			}
		}
	})
	if err := chromedp.Run(ctx, page.BringToFront(), page.StartScreencast().WithFormat(page.ScreencastFormatJpeg).WithQuality(90)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := chromedp.Run(ctx, page.StopScreencast()); err != nil {
			t.Error(err)
		}
	}()
	select {
	case frame := <-frames:
		assertViewportRaster(t, frame.Data, width, height)
	case <-time.After(10 * time.Second):
		t.Fatal("no first native screencast frame")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func viewportWindowID(t *testing.T, ctx context.Context) cdpbrowser.WindowID {
	t.Helper()
	c := chromedp.FromContext(ctx)
	id, _, err := cdpbrowser.GetWindowForTarget().WithTargetID(c.Target.TargetID).Do(cdp.WithExecutor(ctx, c.Browser))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHeadedViewportIntegration(t *testing.T) {
	executable := headedTestExecutable(t)
	home := t.TempDir()
	config, err := json.Marshal(launchConfig{Mode: modeHeaded, Executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	writeBrowserConfig(t, home, string(config))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	mgr := newManager(ctx, home)
	defer mgr.close()
	root := t.TempDir()
	project := mgr.project(root, projectKey(root))
	if err := project.ensureStarted(ctx); err != nil {
		t.Fatal(err)
	}
	owner := project.browserCtx
	var userAgent string
	if err := chromedp.Run(owner, chromedp.Evaluate(`navigator.userAgent`, &userAgent)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(userAgent, "HeadlessChrome") {
		t.Fatalf("headed configuration launched headless Chrome: %s", userAgent)
	}
	ownerWindow := viewportWindowID(t, owner)
	t.Run("initial hidden owner", func(t *testing.T) {
		assertFirstViewportRaster(t, owner, 1440, 900)
		assertViewportCSS(t, owner, 1440, 900)
	})
	s, _, err := project.getSession(ctx, "headed-viewport", false)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := s.selectedTab()
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Project: root, Thread: s.thread, Timeout: 20 * time.Second}
	t.Run("first blank page", func(t *testing.T) {
		var url string
		if err := chromedp.Run(tab.ctx, chromedp.Location(&url)); err != nil {
			t.Fatal(err)
		}
		if url != "about:blank" {
			t.Fatalf("initial URL = %q", url)
		}
		assertViewportCSS(t, tab.ctx, 1440, 900)
		viewer := liveTestClient(t, ctx, mgr, req)
		frame := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "frame" })
		assertViewportRaster(t, frame.Data, 1440, 900)
		if frame.Width != 1440 || frame.Height != 900 {
			t.Fatalf("live CSS extent = %gx%g", frame.Width, frame.Height)
		}
	})
	t.Run("new tab", func(t *testing.T) {
		created, err := s.newTab(ctx, "about:blank")
		if err != nil {
			t.Fatal(err)
		}
		assertViewportCSS(t, created.ctx, 1440, 900)
		assertFirstViewportRaster(t, created.ctx, 1440, 900)
	})
	t.Run("isolated window", func(t *testing.T) {
		isolated, _, err := project.getSession(ctx, "headed-isolated", true)
		if err != nil {
			t.Fatal(err)
		}
		if viewportWindowID(t, isolated.ownerCtx) == ownerWindow {
			t.Fatal("isolated context did not create a separate native window")
		}
		assertFirstViewportRaster(t, isolated.ownerCtx, 1440, 900)
		assertViewportCSS(t, isolated.ownerCtx, 1440, 900)
		isolatedTab, err := isolated.selectedTab()
		if err != nil {
			t.Fatal(err)
		}
		assertViewportCSS(t, isolatedTab.ctx, 1440, 900)
		assertFirstViewportRaster(t, isolatedTab.ctx, 1440, 900)
	})
	t.Run("adopted popup window", func(t *testing.T) {
		popupCtx, cancelWait := context.WithTimeout(tab.ctx, 10*time.Second)
		defer cancelWait()
		popup := chromedp.WaitNewTarget(popupCtx, func(info *target.Info) bool {
			return info.OpenerID == tab.id
		})
		if err := chromedp.Run(tab.ctx, chromedp.Evaluate(`void window.open('about:blank', '_blank', 'popup,width=500,height=400')`, nil)); err != nil {
			t.Fatal(err)
		}
		var id target.ID
		select {
		case id = <-popup:
			if id == "" {
				t.Fatal("popup was not created")
			}
		case <-popupCtx.Done():
			t.Fatal(popupCtx.Err())
		}
		viewer := liveTestClient(t, ctx, mgr, req)
		receiveLive(t, viewer, func(e LiveEvent) bool {
			for _, info := range e.Tabs {
				if info.ID == string(id) {
					return true
				}
			}
			return false
		})
		s.structureMu.Lock()
		adopted := s.tabs[id]
		s.structureMu.Unlock()
		if adopted == nil {
			t.Fatal("popup was not adopted")
		}
		if viewportWindowID(t, adopted.ctx) == ownerWindow {
			t.Fatal("popup did not create a separate native window")
		}
		assertViewportCSS(t, adopted.ctx, 1440, 900)
		if err := viewer.Send(LiveCommand{Type: "watch", TabID: string(id)}); err != nil {
			t.Fatal(err)
		}
		frame := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "frame" && e.TabID == string(id) })
		assertViewportRaster(t, frame.Data, 1440, 900)
		browserCtx := cdp.WithExecutor(adopted.ctx, chromedp.FromContext(adopted.ctx).Browser)
		_, bounds, err := cdpbrowser.GetWindowForTarget().WithTargetID(adopted.id).Do(browserCtx)
		if err != nil {
			t.Fatal(err)
		}
		if bounds.Width < 1440 || bounds.Height < 900 {
			t.Fatalf("popup native window is smaller than its viewport: %+v", bounds)
		}
	})
	t.Run("viewer preserves explicit emulation", func(t *testing.T) {
		if err := chromedp.Run(tab.ctx, emulation.SetDeviceMetricsOverride(800, 600, 1, false), headedWindowSize()); err != nil {
			t.Fatal(err)
		}
		windowCtx := cdp.WithExecutor(tab.ctx, chromedp.FromContext(tab.ctx).Browser)
		before, err := cdpbrowser.GetWindowBounds(ownerWindow).Do(windowCtx)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			viewer := liveTestClient(t, ctx, mgr, req)
			if err := viewer.Send(LiveCommand{Type: "watch", TabID: string(tab.id)}); err != nil {
				t.Fatal(err)
			}
			frame := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "frame" && e.TabID == string(tab.id) })
			assertViewportRaster(t, frame.Data, 800, 600)
			assertViewportCSS(t, tab.ctx, 800, 600)
			if err := viewer.Close(); err != nil {
				t.Fatal(err)
			}
		}
		after, err := cdpbrowser.GetWindowBounds(ownerWindow).Do(windowCtx)
		if err != nil {
			t.Fatal(err)
		}
		if *before != *after {
			t.Fatalf("viewer resized native window: before=%+v after=%+v", before, after)
		}
		assertViewportCSS(t, tab.ctx, 800, 600)
	})
}

func assertViewportRaster(t *testing.T, data string, width, height int) {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(decoded))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != width || img.Bounds().Dy() != height {
		t.Fatalf("actual screencast raster = %dx%d, want %dx%d", img.Bounds().Dx(), img.Bounds().Dy(), width, height)
	}
}

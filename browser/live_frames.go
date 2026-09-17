package browser

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func (t *browserTab) acquireStream(ctx context.Context) error {
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if t.streamUsers == 0 {
		t.liveMu.Lock()
		t.liveFrame = LiveEvent{}
		t.liveMu.Unlock()
		// Activate background tabs so Chrome produces an initial frame.
		start := page.StartScreencast().WithFormat(page.ScreencastFormatJpeg).
			WithQuality(90).WithEveryNthFrame(1)
		if err := t.runStream(ctx, page.BringToFront(), start); err != nil {
			// Cancellation may race Chrome accepting StartScreencast.
			_ = t.stopStream(ctx)
			return err
		}
	}
	t.streamUsers++
	return nil
}

func (t *browserTab) releaseStream(ctx context.Context) error {
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	if t.streamUsers == 0 {
		return nil
	}
	t.streamUsers--
	if t.streamUsers != 0 {
		return nil
	}
	return t.stopStream(ctx)
}

func (t *browserTab) stopStream(ctx context.Context) error {
	// The last owner is gone even when its request has been canceled.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return t.runStream(cleanup, page.StopScreencast())
}

func (t *browserTab) runStream(request context.Context, actions ...chromedp.Action) error {
	ctx, cancel := linkedContext(t.ctx, request)
	defer cancel()
	if err := chromedp.Run(ctx, actions...); err != nil {
		if request.Err() != nil {
			return request.Err()
		}
		return fail("action_failed", "screencast: %v", err)
	}
	return nil
}

func (t *browserTab) activity(kind string, x, y float64) {
	switch kind {
	case "move", "click", "fill":
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			return
		}
	case "clear":
		x, y = 0, 0
	default:
		return
	}
	t.liveMu.Lock()
	if kind != "clear" {
		y += t.liveOffsetTop
	}
	t.liveActivity = LiveEvent{Type: "activity", TabID: string(t.id), Kind: kind, X: x, Y: y}
	t.liveActivitySeq++
	t.liveActivityAt = time.Now()
	t.liveMu.Unlock()
}

// Chrome reuses a session ID for every frame in one screencast. Only ACKs for
// the current session affect its flow control, so obsolete work can be dropped.
type screencastAcks struct {
	mu        sync.Mutex
	running   bool
	sessionID int64
	pending   int
}

func (a *screencastAcks) offer(sessionID int64, acknowledge func(int64)) {
	a.mu.Lock()
	if a.sessionID != sessionID {
		a.sessionID = sessionID
		a.pending = 0
	}
	a.pending++
	if a.running {
		a.mu.Unlock()
		return
	}
	a.running = true
	a.mu.Unlock()
	go func() {
		for {
			a.mu.Lock()
			if a.pending == 0 {
				a.running = false
				a.mu.Unlock()
				return
			}
			id := a.sessionID
			a.pending--
			a.mu.Unlock()
			acknowledge(id)
		}
	}()
}

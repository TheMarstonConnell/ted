package browser

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func (s *session) startTargetTracking(request context.Context) error {
	ctx, cancel := context.WithCancel(s.parentCtx)
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	chromedp.ListenBrowser(ctx, func(event any) {
		switch event.(type) {
		case *target.EventTargetCreated, *target.EventTargetInfoChanged, *target.EventTargetDestroyed:
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	})
	browser := chromedp.FromContext(s.parentCtx).Browser
	if err := target.SetDiscoverTargets(true).Do(cdp.WithExecutor(request, browser)); err != nil {
		cancel()
		return fail("browser_unavailable", "watch browser targets: %v", err)
	}
	s.structureMu.Lock()
	s.targetCancel, s.targetDone, s.targetWake = cancel, done, wake
	s.structureMu.Unlock()
	go s.trackTargets(ctx, wake, done)
	return nil
}

func (s *session) wakeTargetTracking() {
	s.structureMu.Lock()
	wake := s.targetWake
	closed := s.closed
	s.structureMu.Unlock()
	if wake == nil || closed {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *session) trackTargets(ctx context.Context, wake <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.reconcileTargets(ctx); err != nil && ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
		}
	}
}

func (s *session) targetInfos(ctx context.Context) ([]*target.Info, error) {
	browser := chromedp.FromContext(s.parentCtx).Browser
	return target.GetTargets().Do(cdp.WithExecutor(ctx, browser))
}

func (s *session) reconcileTargets(ctx context.Context) error {
	s.targetMu.Lock()
	defer s.targetMu.Unlock()
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	infos, err := s.targetInfos(queryCtx)
	cancel()
	if err != nil {
		return err
	}

	s.structureMu.Lock()
	if s.closed {
		s.structureMu.Unlock()
		return context.Canceled
	}
	owned := make(map[target.ID]struct{}, len(s.ownedTargets))
	for id := range s.ownedTargets {
		owned[id] = struct{}{}
	}
	existing := make(map[target.ID]struct{}, len(s.tabs))
	for id := range s.tabs {
		existing[id] = struct{}{}
	}
	s.structureMu.Unlock()

	claimed := s.claimedPageTargets(infos, owned)
	present := make(map[target.ID]struct{}, len(infos))
	for _, info := range infos {
		present[info.TargetID] = struct{}{}
		if _, ok := claimed[info.TargetID]; !ok {
			continue
		}
		if _, ok := existing[info.TargetID]; ok {
			continue
		}
		if _, wasOwned := owned[info.TargetID]; wasOwned {
			continue
		}
		if err := s.attachPageTarget(ctx, info.TargetID); err == nil {
			existing[info.TargetID] = struct{}{}
		}
	}
	for id := range existing {
		if _, ok := present[id]; !ok {
			s.removeDestroyedTarget(id)
		}
	}
	return nil
}

func (s *session) claimedPageTargets(infos []*target.Info, seeds map[target.ID]struct{}) map[target.ID]struct{} {
	claimed := make(map[target.ID]struct{}, len(seeds))
	for id := range seeds {
		claimed[id] = struct{}{}
	}
	if s.isolated {
		for _, info := range infos {
			if info.Type == "page" && info.TargetID != s.ownerTarget && info.BrowserContextID == s.browserContext {
				claimed[info.TargetID] = struct{}{}
			}
		}
		return claimed
	}
	for changed := true; changed; {
		changed = false
		for _, info := range infos {
			if info.Type != "page" || info.OpenerID == "" {
				continue
			}
			if _, ok := claimed[info.OpenerID]; !ok {
				continue
			}
			if _, ok := claimed[info.TargetID]; !ok {
				claimed[info.TargetID] = struct{}{}
				changed = true
			}
		}
	}
	return claimed
}

func (s *session) attachPageTarget(request context.Context, id target.ID) error {
	tabCtx, cancel := chromedp.NewContext(s.parentCtx, chromedp.WithTargetID(id))
	t := &browserTab{id: id, ctx: tabCtx, cancel: cancel}
	if err := initializeContext(tabCtx, request, cancel, s.project.tabSetup()...); err != nil {
		return err
	}
	t.installListeners()

	s.structureMu.Lock()
	if s.closed {
		s.structureMu.Unlock()
		cancel()
		return context.Canceled
	}
	if s.tabs[id] != nil {
		s.structureMu.Unlock()
		cancel()
		return nil
	}
	s.tabs[id] = t
	s.ownedTargets[id] = struct{}{}
	s.order = append(s.order, id)
	if s.selected == "" {
		s.selected = id
	}
	s.structureMu.Unlock()
	s.wakeTargetTracking()
	return nil
}

func (s *session) removeDestroyedTarget(id target.ID) {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	s.structureMu.Lock()
	t := s.tabs[id]
	if t == nil {
		s.structureMu.Unlock()
		return
	}
	delete(s.tabs, id)
	for i, ordered := range s.order {
		if ordered == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	if s.selected == id {
		s.selected = ""
		if len(s.order) != 0 {
			s.selected = s.order[len(s.order)-1]
		}
	}
	s.structureMu.Unlock()
	if s.recording != nil && s.recording.tab == t {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = s.stopRecordingLocked(cleanup)
		cancel()
	}
	t.cancel()
}

func (s *session) closeOwnedTargets(seeds map[target.ID]struct{}) error {
	if len(seeds) == 0 && !s.isolated {
		return nil
	}
	if s.parentCtx == nil {
		return fmt.Errorf("browser context unavailable during session cleanup")
	}
	if err := s.parentCtx.Err(); err != nil {
		return err
	}
	// Keep descendants discovered during a failed cleanup available to the retry.
	defer func() {
		s.structureMu.Lock()
		defer s.structureMu.Unlock()
		if s.ownedTargets == nil {
			s.ownedTargets = make(map[target.ID]struct{})
		}
		for id := range seeds {
			s.ownedTargets[id] = struct{}{}
		}
	}()
	var closeErr error
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		infos, err := s.targetInfos(ctx)
		if err != nil {
			cancel()
			return fmt.Errorf("query remaining browser targets: %w", err)
		}
		claimed := s.claimedPageTargets(infos, seeds)
		remaining := make([]target.ID, 0)
		for _, info := range infos {
			_, owned := claimed[info.TargetID]
			if owned || (s.isolated && info.TargetID == s.ownerTarget) {
				seeds[info.TargetID] = struct{}{}
				remaining = append(remaining, info.TargetID)
			}
		}
		if len(remaining) == 0 {
			cancel()
			return nil
		}
		if attempt == 3 {
			cancel()
			return errors.Join(fmt.Errorf("%d browser targets remain after cleanup", len(remaining)), closeErr)
		}
		browser := chromedp.FromContext(s.parentCtx).Browser
		for _, id := range remaining {
			if err := target.CloseTarget(id).Do(cdp.WithExecutor(ctx, browser)); err != nil {
				closeErr = fmt.Errorf("close browser target %s: %w", id, err)
			}
		}
		cancel()
		time.Sleep(25 * time.Millisecond)
	}
}

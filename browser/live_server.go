package browser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const maxLiveCommandBytes = 512 << 10

type liveSubscription struct {
	manager           *manager
	root, key, thread string
	lifecycle         *sessionLifecycle
	mu                sync.Mutex
	watch             string
	inputs            map[*browserTab]*liveInputState
}

type liveInputState struct {
	buttons map[string]LiveCommand
	keys    map[string]LiveCommand
}

func serveLive(parent context.Context, mgr *manager, conn net.Conn, req Request, reader io.Reader) {
	err := validateThread(req.Thread)
	var root string
	if err == nil {
		root, err = mgr.resolveProject(req.Project, false)
		if err != nil {
			err = fail("invalid_project", "%v", err)
		}
	}
	if err != nil {
		_ = json.NewEncoder(conn).Encode(errorResponse(err))
		return
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	key := projectKey(root)
	lifecycle, unregister := mgr.registerLive(key, req.Thread, cancel)
	defer unregister()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(Response{OK: true}); err != nil {
		return
	}
	sub := &liveSubscription{manager: mgr, root: root, key: key, thread: req.Thread, lifecycle: lifecycle, inputs: make(map[*browserTab]*liveInputState)}
	states := make(chan LiveEvent, 1)
	frames := make(chan LiveEvent, 1)
	events := make(chan LiveEvent, 16)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer cancel()
		enc := json.NewEncoder(conn)
		for {
			var event LiveEvent
			select {
			case <-ctx.Done():
				return
			case event = <-events:
			case event = <-states:
			case event = <-frames:
			}
			if event.Type == "frame" {
				select {
				case state := <-states:
					_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					if enc.Encode(state) != nil {
						return
					}
				default:
				}
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if enc.Encode(event) != nil {
				return
			}
		}
	}()
	go func() { defer wg.Done(); sub.observe(ctx, states, frames, events) }()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxLiveCommandBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		command, err := ParseLiveCommand(line)
		if err == nil {
			opCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			err = sub.execute(opCtx, command)
			stop()
		}
		if err != nil {
			response := errorResponse(err)
			select {
			case events <- LiveEvent{Type: "error", Code: response.Error.Code, Message: response.Error.Message}:
			case <-ctx.Done():
				break
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	cancel()
	sub.releaseAll()
	wg.Wait()
}

func offerLatest(ch chan LiveEvent, event LiveEvent) {
	select {
	case ch <- event:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- event:
	default:
	}
}

func (l *liveSubscription) session() *session {
	l.manager.mu.Lock()
	p := l.manager.projects[l.key]
	l.manager.mu.Unlock()
	if p == nil {
		return nil
	}
	p.mu.Lock()
	s := p.sessions[l.thread]
	p.mu.Unlock()
	return s
}

func (l *liveSubscription) observe(ctx context.Context, states, frames, events chan LiveEvent) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var viewing *browserTab
	var attempted *browserTab
	var failedStream *browserTab
	var previousFrame LiveEvent
	var activitySeq uint64
	var nextState time.Time
	defer func() {
		if viewing != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = viewing.releaseStream(cleanup)
		}
	}()
	for {
		s := l.session()
		var tabs []*browserTab
		var selected target.ID
		if s != nil {
			tabs, selected = s.tabSnapshot()
		}
		l.mu.Lock()
		watch := l.watch
		l.mu.Unlock()
		viewed := watch
		if viewed == "" {
			viewed = string(selected)
		}
		var desired *browserTab
		for _, t := range tabs {
			if string(t.id) == viewed {
				desired = t
				break
			}
		}
		if desired != viewing {
			changed := desired != attempted
			if changed {
				opCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				if viewing != nil {
					_ = viewing.releaseStream(opCtx)
				}
				cancel()
				viewing = nil
				attempted = desired
				failedStream = nil
				previousFrame = LiveEvent{}
				activitySeq = 0
				nextState = time.Time{}
			}
			if desired != nil {
				opCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				err := desired.acquireStream(opCtx)
				cancel()
				if err == nil {
					viewing = desired
					failedStream = nil
					nextState = time.Time{}
				} else if failedStream != desired && ctx.Err() == nil {
					failedStream = desired
					response := errorResponse(err)
					select {
					case events <- LiveEvent{Type: "error", Code: response.Error.Code, Message: response.Error.Message}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
		if time.Now().After(nextState) {
			state := LiveEvent{Type: "state", Selected: string(selected), Pinned: watch, Tabs: make([]LiveTab, 0, len(tabs))}
			if viewing != nil {
				state.TabID = string(viewing.id)
			}
			infoByID := make(map[target.ID]*target.Info)
			if len(tabs) > 0 {
				opCtx, cancel := linkedContext(tabs[0].ctx, ctx)
				timeout, stop := context.WithTimeout(opCtx, 2*time.Second)
				_ = chromedp.Run(timeout, chromedp.ActionFunc(func(exec context.Context) error {
					infos, err := target.GetTargets().Do(cdp.WithExecutor(exec, chromedp.FromContext(exec).Browser))
					for _, info := range infos {
						infoByID[info.TargetID] = info
					}
					return err
				}))
				stop()
				cancel()
			}
			for _, t := range tabs {
				tab := LiveTab{ID: string(t.id)}
				if info := infoByID[t.id]; info != nil {
					tab.URL = info.URL
					tab.Title = info.Title
				}
				state.Tabs = append(state.Tabs, tab)
			}
			offerLatest(states, state)
			nextState = time.Now().Add(time.Second)
		}
		if viewing != nil {
			viewing.liveMu.Lock()
			frame := viewing.liveFrame
			activity := viewing.liveActivity
			seq := viewing.liveActivitySeq
			activityAt := viewing.liveActivityAt
			viewing.liveMu.Unlock()
			if frame.Data != "" && (frame.Data != previousFrame.Data || frame.Width != previousFrame.Width || frame.Height != previousFrame.Height) {
				offerLatest(frames, frame)
				previousFrame = frame
			}
			if seq != activitySeq && (activity.Kind == "clear" || time.Since(activityAt) < 1500*time.Millisecond) {
				select {
				case events <- activity:
				default:
				}
				activitySeq = seq
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *liveSubscription) execute(ctx context.Context, c LiveCommand) error {
	l.lifecycle.gate.RLock()
	defer l.lifecycle.gate.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Type == "release" {
		for tab := range l.inputs {
			if string(tab.id) == c.TabID {
				return l.release(ctx, tab)
			}
		}
		return nil
	}
	if c.Type == "watch" {
		if c.TabID != "" {
			if _, _, err := l.tab(c.TabID); err != nil {
				return err
			}
		}
		l.mu.Lock()
		l.watch = c.TabID
		l.mu.Unlock()
		return nil
	}
	s := l.session()
	if c.Type == "new" || (c.Type == "navigate" && c.TabID == "") {
		return l.open(ctx, c, s)
	}
	s, t, err := l.tab(c.TabID)
	if err != nil {
		return err
	}
	switch c.Type {
	case "navigate":
		return liveNavigate(ctx, t, c.URL)
	case "close":
		_ = l.release(ctx, t)
		_, err := s.tabClose(ctx, map[string]any{"id": c.TabID})
		return err
	case "reload":
		return liveRun(ctx, t, page.Reload())
	case "back", "forward":
		return liveRun(ctx, t, chromedp.ActionFunc(func(exec context.Context) error {
			index, entries, err := page.GetNavigationHistory().Do(exec)
			if err != nil {
				return err
			}
			if c.Type == "back" {
				index--
			} else {
				index++
			}
			if index < 0 || index >= int64(len(entries)) {
				return nil
			}
			return page.NavigateToHistoryEntry(entries[index].ID).Do(exec)
		}))
	case "mouse", "key", "text":
		pressed := c.Type == "key" && c.Event == "keyDown" || c.Type == "mouse" && c.Event == "mousePressed"
		if pressed {
			count := 0
			for tab, held := range l.inputs {
				if tab.ctx.Err() != nil {
					delete(l.inputs, tab)
					continue
				}
				count += len(held.keys) + len(held.buttons)
			}
			if count >= 128 {
				return fail("invalid_params", "too many held inputs; release input first")
			}
			// A canceled CDP response may still have delivered the press.
			l.track(t, c)
		}
		err := dispatchLiveInput(ctx, t, c)
		if err == nil && !pressed {
			l.track(t, c)
		}
		return err
	}
	return fail("invalid_params", "unsupported live command")
}

func (l *liveSubscription) open(ctx context.Context, c LiveCommand, s *session) error {
	created := false
	if s == nil {
		p := l.manager.project(l.root, l.key)
		if err := p.ensureStarted(ctx); err != nil {
			return fail("browser_unavailable", "start Chrome: %v", err)
		}
		var err error
		s, created, err = p.getSession(ctx, l.thread, false)
		if err != nil {
			return err
		}
	}
	if c.Type == "navigate" && !created {
		tabs, _ := s.tabSnapshot()
		if len(tabs) > 0 {
			return fail("invalid_params", "navigate requires explicit tab_id when tabs exist")
		}
	}
	var t *browserTab
	var err error
	if created {
		t, err = s.selectedTab()
	} else {
		t, err = s.createTab(ctx, "about:blank", false)
	}
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.watch = string(t.id)
	l.mu.Unlock()
	if c.URL != "" {
		return liveNavigate(ctx, t, c.URL)
	}
	return nil
}

func (l *liveSubscription) tab(id string) (*session, *browserTab, error) {
	s := l.session()
	if s == nil {
		return nil, nil, fail("not_found", "no browser session")
	}
	s.structureMu.Lock()
	t := s.tabs[target.ID(id)]
	s.structureMu.Unlock()
	if t == nil {
		return nil, nil, fail("not_found", "tab not found in thread session")
	}
	return s, t, nil
}

func liveRun(ctx context.Context, t *browserTab, actions ...chromedp.Action) error {
	run, cancel := linkedContext(t.ctx, ctx)
	defer cancel()
	return chromedp.Run(run, actions...)
}
func liveNavigate(ctx context.Context, t *browserTab, url string) error {
	url, err := normalizeURL(url)
	if err != nil {
		return err
	}
	return liveRun(ctx, t, chromedp.ActionFunc(func(exec context.Context) error {
		_, _, message, _, err := page.Navigate(url).Do(exec)
		if err == nil && message != "" {
			err = fail("action_failed", "navigate: %s", message)
		}
		return err
	}))
}

func dispatchLiveInput(ctx context.Context, t *browserTab, c LiveCommand) error {
	switch c.Type {
	case "mouse":
		t.liveMu.Lock()
		c.Y -= t.liveOffsetTop
		t.liveMu.Unlock()
		button := input.MouseButton(c.Button)
		if button == "" {
			button = input.None
		}
		count := c.ClickCount
		if count == 0 && (c.Event == "mousePressed" || c.Event == "mouseReleased") {
			count = 1
		}
		return liveRun(ctx, t, input.DispatchMouseEvent(input.MouseType(c.Event), c.X, c.Y).WithButton(button).WithButtons(c.Buttons).WithClickCount(count).WithModifiers(input.Modifier(c.Modifiers)).WithDeltaX(c.DeltaX).WithDeltaY(c.DeltaY))
	case "key":
		return liveRun(ctx, t, input.DispatchKeyEvent(input.KeyType(c.Event)).WithKey(c.Key).WithCode(c.Code).WithText(c.Text).WithModifiers(input.Modifier(c.Modifiers)).WithWindowsVirtualKeyCode(c.KeyCode))
	case "text":
		return liveRun(ctx, t, input.InsertText(c.Text))
	}
	return fail("invalid_params", "unsupported input")
}

func (l *liveSubscription) track(t *browserTab, c LiveCommand) {
	state := l.inputs[t]
	if state == nil {
		state = &liveInputState{buttons: make(map[string]LiveCommand), keys: make(map[string]LiveCommand)}
		l.inputs[t] = state
	}
	switch c.Type {
	case "mouse":
		if c.Event == "mousePressed" {
			state.buttons[c.Button] = c
		} else if c.Event == "mouseReleased" {
			delete(state.buttons, c.Button)
		}
		for button, held := range state.buttons {
			held.X = c.X
			held.Y = c.Y
			state.buttons[button] = held
		}
	case "key":
		id := c.Code
		if id == "" {
			id = c.Key
		}
		if c.Event == "keyDown" {
			state.keys[id] = c
		} else {
			delete(state.keys, id)
		}
	}
	if len(state.buttons) == 0 && len(state.keys) == 0 {
		delete(l.inputs, t)
	}
}
func (l *liveSubscription) release(ctx context.Context, t *browserTab) error {
	state := l.inputs[t]
	if state == nil {
		return nil
	}
	if t.ctx.Err() != nil {
		delete(l.inputs, t)
		return nil
	}
	var first error
	for button, c := range state.buttons {
		c.Event = "mouseReleased"
		c.Buttons = 0
		c.Modifiers = 0
		if err := dispatchLiveInput(ctx, t, c); err != nil {
			if first == nil {
				first = err
			}
		} else {
			delete(state.buttons, button)
		}
	}
	for key, c := range state.keys {
		c.Event = "keyUp"
		c.Text = ""
		c.Modifiers = 0
		if err := dispatchLiveInput(ctx, t, c); err != nil {
			if first == nil {
				first = err
			}
		} else {
			delete(state.keys, key)
		}
	}
	if t.ctx.Err() != nil {
		delete(l.inputs, t)
		return nil
	}
	if len(state.buttons) == 0 && len(state.keys) == 0 {
		delete(l.inputs, t)
	}
	return first
}
func (l *liveSubscription) releaseAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for t := range l.inputs {
		_ = l.release(ctx, t)
	}
}

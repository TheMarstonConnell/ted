package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

func (s *session) perform(ctx context.Context, action string, params map[string]any) (any, error) {
	switch action {
	case "open":
		return s.open(ctx, params)
	case "snapshot":
		return s.snapshot(ctx)
	case "click":
		return s.click(ctx, params)
	case "fill":
		return s.fill(ctx, params)
	case "select":
		return s.selectValue(ctx, params)
	case "press":
		return s.press(ctx, params)
	case "scroll":
		return s.scroll(ctx, params)
	case "wait":
		return s.wait(ctx, params)
	case "tabs":
		return s.listTabs(ctx)
	case "tab-new":
		return s.tabNew(ctx, params)
	case "tab-select":
		return s.tabSelect(params)
	case "tab-close":
		return s.tabClose(ctx, params)
	case "screenshot":
		return s.screenshot(ctx, params)
	case "record-start":
		return s.startRecordingLocked(ctx, params)
	case "record-stop":
		return s.stopRecordingLocked(ctx)
	case "console":
		return s.readEvents(params, false)
	case "errors":
		return s.readEvents(params, true)
	case "cdp":
		return s.cdpCall(ctx, params)
	case "cdp-listen":
		return s.cdpListen(ctx, params)
	default:
		return nil, fail("unknown_action", "unknown browser action %q", action)
	}
}

func (s *session) runTab(request context.Context, tab *browserTab, actions ...chromedp.Action) error {
	ctx, cancel := linkedContext(tab.ctx, request)
	defer cancel()
	if err := chromedp.Run(ctx, actions...); err != nil {
		if request.Err() != nil || ctx.Err() == context.DeadlineExceeded {
			return context.DeadlineExceeded
		}
		return fail("action_failed", "%v", err)
	}
	return nil
}

func (s *session) open(ctx context.Context, params map[string]any) (any, error) {
	raw, err := stringParam(params, "url", true)
	if err != nil {
		return nil, err
	}
	if err := validateURL(raw); err != nil {
		return nil, err
	}
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	if err := s.runTab(ctx, t, chromedp.Navigate(raw)); err != nil {
		return nil, err
	}
	return s.tabInfo(ctx, t)
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return fail("invalid_params", "url must be an absolute URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "file", "about", "data":
		return nil
	default:
		return fail("invalid_params", "unsupported URL scheme %q", u.Scheme)
	}
}

func keySequence(key string) string {
	switch strings.ToLower(key) {
	case "enter", "return":
		return kb.Enter
	case "escape", "esc":
		return kb.Escape
	case "tab":
		return kb.Tab
	case "backspace":
		return kb.Backspace
	case "delete":
		return kb.Delete
	case "arrowup", "up":
		return kb.ArrowUp
	case "arrowdown", "down":
		return kb.ArrowDown
	case "arrowleft", "left":
		return kb.ArrowLeft
	case "arrowright", "right":
		return kb.ArrowRight
	case "home":
		return kb.Home
	case "end":
		return kb.End
	case "pageup":
		return kb.PageUp
	case "pagedown":
		return kb.PageDown
	case "space":
		return " "
	default:
		return key
	}
}

func (s *session) press(ctx context.Context, params map[string]any) (any, error) {
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	key, err := stringParam(params, "key", true)
	if err != nil {
		return nil, err
	}
	sel, err := s.resolveSelector(ctx, t, params, false)
	if err != nil {
		return nil, err
	}
	seq := keySequence(key)
	var act chromedp.Action
	if sel != "" {
		act = chromedp.SendKeys(sel, seq, chromedp.ByQuery)
	} else {
		act = chromedp.KeyEvent(seq)
	}
	if err := s.runTab(ctx, t, act); err != nil {
		return nil, err
	}
	return map[string]any{"pressed": key}, nil
}

func (s *session) scroll(ctx context.Context, params map[string]any) (any, error) {
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	sel, err := s.resolveSelector(ctx, t, params, false)
	if err != nil {
		return nil, err
	}
	if sel != "" {
		sb, _ := json.Marshal(sel)
		expr := fmt.Sprintf(`(() => {const e=document.querySelector(%s);if(!e)return false;e.scrollIntoView({block:'center',inline:'center'});return true})()`, sb)
		var found bool
		if err := s.runTab(ctx, t, chromedp.Evaluate(expr, &found)); err != nil {
			return nil, err
		}
		if !found {
			return nil, fail("not_found", "scroll element not found")
		}
		return map[string]any{"scrolled": true, "selector": sel}, nil
	}
	x, err := numberParam(params, "x", 0)
	if err != nil {
		return nil, err
	}
	y, err := numberParam(params, "y", 0)
	if err != nil {
		return nil, err
	}
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return nil, fail("invalid_params", "scroll offsets must be finite")
	}
	expr := fmt.Sprintf(`window.scrollBy(%g,%g)`, x, y)
	if err := s.runTab(ctx, t, chromedp.Evaluate(expr, nil)); err != nil {
		return nil, err
	}
	return map[string]any{"scrolled": true, "x": x, "y": y}, nil
}

func (s *session) wait(ctx context.Context, params map[string]any) (any, error) {
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	sel, err := s.resolveSelector(ctx, t, params, false)
	if err != nil {
		return nil, err
	}
	pattern, err := stringParam(params, "url-pattern", false)
	if err != nil {
		return nil, err
	}
	if sel != "" {
		if err := s.runTab(ctx, t, chromedp.WaitVisible(sel, chromedp.ByQuery)); err != nil {
			return nil, err
		}
		return map[string]any{"matched": "selector", "selector": sel}, nil
	}
	if pattern != "" {
		re, err := wildcardRegexp(pattern)
		if err != nil {
			return nil, fail("invalid_params", "invalid url-pattern: %v", err)
		}
		// Poll browser-owned target metadata, not JavaScript in the document:
		// navigation destroys execution contexts and interrupts chromedp.Poll.
		// The request context is the only timeout, including waits over 30s.
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			var info *target.Info
			err := s.runTab(ctx, t, chromedp.ActionFunc(func(execCtx context.Context) error {
				var err error
				executor := cdp.WithExecutor(execCtx, chromedp.FromContext(execCtx).Browser)
				info, err = target.GetTargetInfo().WithTargetID(t.id).Do(executor)
				return err
			}))
			if err != nil {
				return nil, err
			}
			if re.MatchString(info.URL) {
				return map[string]any{"matched": "url", "pattern": pattern}, nil
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
			}
		}
	}
	ms, err := numberParam(params, "value", 0)
	if err != nil {
		return nil, err
	}
	if ms < 0 {
		return nil, fail("invalid_params", "wait value may not be negative")
	}
	if ms == 0 {
		return nil, fail("invalid_params", "wait requires selector, url-pattern, or millisecond value")
	}
	timer := time.NewTimer(time.Duration(ms * float64(time.Millisecond)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return map[string]any{"waited_ms": ms}, nil
	}
}

func wildcardRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteByte('^')
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteByte('$')
	return regexp.Compile(b.String())
}

func (s *session) tabNew(ctx context.Context, params map[string]any) (any, error) {
	raw, err := stringParam(params, "url", false)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		raw = "about:blank"
	}
	if err := validateURL(raw); err != nil {
		return nil, err
	}
	t, err := s.newTab(ctx, raw)
	if err != nil {
		return nil, err
	}
	return s.tabInfo(ctx, t)
}

func (s *session) tabSelect(params map[string]any) (any, error) {
	id, err := stringParam(params, "id", true)
	if err != nil {
		return nil, err
	}
	t := s.tabs[target.ID(id)]
	if t == nil {
		return nil, fail("not_found", "tab %q not found in thread session", id)
	}
	s.selected = t.id
	return map[string]any{"selected": string(t.id)}, nil
}

func (s *session) tabClose(ctx context.Context, params map[string]any) (any, error) {
	id, err := stringParam(params, "id", false)
	if err != nil {
		return nil, err
	}
	if id == "" {
		id = string(s.selected)
	}
	t := s.tabs[target.ID(id)]
	if t == nil {
		return nil, fail("not_found", "tab %q not found in thread session", id)
	}
	if s.recording != nil && s.recording.tab == t {
		if _, err := s.stopRecordingLocked(ctx); err != nil {
			return nil, err
		}
	}
	t.cancel()
	delete(s.tabs, t.id)
	for i, tid := range s.order {
		if tid == t.id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	if s.selected == t.id {
		if len(s.order) > 0 {
			s.selected = s.order[len(s.order)-1]
		} else {
			s.selected = ""
		}
	}
	return map[string]any{"closed": id, "selected": string(s.selected)}, nil
}

func (s *session) listTabs(ctx context.Context) (any, error) {
	out := make([]map[string]any, 0, len(s.order))
	for _, id := range s.order {
		t := s.tabs[id]
		if t == nil {
			continue
		}
		info, err := s.tabInfo(ctx, t)
		if err != nil {
			return nil, err
		}
		info["selected"] = id == s.selected
		out = append(out, info)
	}
	return map[string]any{"tabs": out}, nil
}

func (s *session) tabInfo(ctx context.Context, t *browserTab) (map[string]any, error) {
	var title, location string
	if err := s.runTab(ctx, t, chromedp.Title(&title), chromedp.Location(&location)); err != nil {
		return nil, err
	}
	return map[string]any{"id": string(t.id), "url": location, "title": title}, nil
}

func (s *session) screenshot(ctx context.Context, params map[string]any) (any, error) {
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	full, err := boolParam(params, "full-page", false)
	if err != nil {
		return nil, err
	}
	sel, err := s.resolveSelector(ctx, t, params, false)
	if err != nil {
		return nil, err
	}
	var data []byte
	if sel != "" {
		err = s.runTab(ctx, t, chromedp.Screenshot(sel, &data, chromedp.ByQuery))
	} else if full {
		err = s.runTab(ctx, t, chromedp.FullScreenshot(&data, 100))
	} else {
		err = s.runTab(ctx, t, chromedp.CaptureScreenshot(&data))
	}
	if err != nil {
		return nil, err
	}
	artifactDir := filepath.Join(s.dir, "artifacts")
	if err := mkdirPrivate(artifactDir); err != nil {
		return nil, fail("internal", "create artifact directory: %v", err)
	}
	name := fmt.Sprintf("screenshot-%d.png", time.Now().UnixNano())
	path := filepath.Join(artifactDir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fail("internal", "create screenshot: %v", err)
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return nil, fail("internal", "write screenshot: %v", err)
	}
	if err := s.registerScreenshot(path); err != nil {
		os.Remove(path)
		return nil, err
	}
	return map[string]any{"path": path, "type": "screenshot", "mime_type": "image/png", "bytes": len(data)}, nil
}

func (s *session) registerScreenshot(path string) error {
	manifest := filepath.Join(s.dir, "artifacts.jsonl")
	f, err := os.OpenFile(manifest, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fail("internal", "open artifact manifest: %v", err)
	}
	_ = os.Chmod(manifest, 0o600)
	line, _ := json.Marshal(map[string]string{"type": "screenshot", "path": path})
	line = append(line, '\n')
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return fail("internal", "write artifact manifest: %v", werr)
	}
	if cerr != nil {
		return fail("internal", "close artifact manifest: %v", cerr)
	}
	return nil
}

func (s *session) readEvents(params map[string]any, errorsOnly bool) (any, error) {
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	clear, err := boolParam(params, "clear", false)
	if err != nil {
		return nil, err
	}
	t.eventsMu.Lock()
	defer t.eventsMu.Unlock()
	var src []any
	key := "console"
	if errorsOnly {
		src = t.errors
		key = "errors"
	} else {
		src = t.console
	}
	out := append([]any(nil), src...)
	if clear {
		if errorsOnly {
			t.errors = nil
		} else {
			t.console = nil
		}
	}
	return map[string]any{key: out}, nil
}

func (s *session) cdpCall(ctx context.Context, params map[string]any) (any, error) {
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	method, err := stringParam(params, "method", true)
	if err != nil {
		return nil, err
	}
	browser, err := boolParam(params, "browser", false)
	if err != nil {
		return nil, err
	}
	callParams := map[string]any{}
	if raw, ok := params["params"]; ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fail("invalid_params", "params must be an object")
		}
		callParams = m
	}
	var result any
	action := chromedp.ActionFunc(func(execCtx context.Context) error {
		if browser {
			execCtx = cdp.WithExecutor(execCtx, chromedp.FromContext(execCtx).Browser)
		}
		return cdp.Execute(execCtx, method, callParams, &result)
	})
	if err := s.runTab(ctx, t, action); err != nil {
		return nil, err
	}
	return map[string]any{"method": method, "result": result}, nil
}

func (s *session) cdpListen(ctx context.Context, params map[string]any) (any, error) {
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	event, err := stringParam(params, "event", true)
	if err != nil {
		return nil, err
	}
	browser, err := boolParam(params, "browser", false)
	if err != nil {
		return nil, err
	}
	limitF, err := numberParam(params, "limit", 1)
	if err != nil {
		return nil, err
	}
	if limitF < 1 || limitF > 1000 || math.Trunc(limitF) != limitF {
		return nil, fail("invalid_params", "limit must be an integer from 1 to 1000")
	}
	limit := int(limitF)
	listenCtx, cancel := linkedContext(t.ctx, ctx)
	defer cancel()
	ch := make(chan any, limit)
	listener := func(ev any) {
		if eventMatches(event, ev) {
			select {
			case ch <- ev:
			default:
			}
		}
	}
	if browser {
		chromedp.ListenBrowser(listenCtx, listener)
	} else {
		chromedp.ListenTarget(listenCtx, listener)
	}
	// Most domains require enable before they emit events. Ignore unknown enable
	// methods (some domains, such as Target, do not have one).
	if dot := strings.IndexByte(event, '.'); dot > 0 {
		_ = s.runTab(ctx, t, chromedp.ActionFunc(func(execCtx context.Context) error {
			if browser {
				execCtx = cdp.WithExecutor(execCtx, chromedp.FromContext(execCtx).Browser)
			}
			return cdp.Execute(execCtx, event[:dot]+".enable", map[string]any{}, nil)
		}))
	}
	items := make([]any, 0, limit)
	for len(items) < limit {
		select {
		case <-ctx.Done():
			// Listening is explicitly bounded by the request timeout. Return any
			// events collected by then rather than discarding useful data.
			return map[string]any{"events": items, "timed_out": true}, nil
		case ev := <-ch:
			items = append(items, map[string]any{"event": event, "params": ev})
		}
	}
	return map[string]any{"events": items, "timed_out": false}, nil
}

func eventMatches(requested string, ev any) bool {
	t := reflect.TypeOf(ev)
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	pkg := t.PkgPath()
	if i := strings.LastIndexByte(pkg, '/'); i >= 0 {
		pkg = pkg[i+1:]
	}
	name := strings.TrimPrefix(t.Name(), "Event")
	norm := func(v string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(v) {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	return norm(requested) == norm(pkg+name)
}

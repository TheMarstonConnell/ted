package browser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

type manager struct {
	ctx      context.Context
	home     string
	mu       sync.Mutex
	projects map[string]*projectBrowser
	traceMu  sync.Mutex
}

type projectBrowser struct {
	manager *manager
	root    string
	key     string

	mu            sync.Mutex
	started       bool
	allocCtx      context.Context
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
	sessions      map[string]*session
}

type session struct {
	project  *projectBrowser
	thread   string
	dir      string
	isolated bool

	mu             sync.Mutex
	ownerCtx       context.Context // hidden owner of an isolated BrowserContext
	ownerCancel    context.CancelFunc
	browserContext cdp.BrowserContextID
	tabs           map[target.ID]*browserTab
	order          []target.ID
	selected       target.ID
	refCounter     int
	recording      *recording
}

type browserTab struct {
	id     target.ID
	ctx    context.Context
	cancel context.CancelFunc

	eventsMu sync.Mutex
	console  []any
	errors   []any
	recMu    sync.Mutex
	rec      *recording
}

func newManager(ctx context.Context, home string) *manager {
	return &manager{ctx: ctx, home: home, projects: make(map[string]*projectBrowser)}
}

func (m *manager) close() {
	m.mu.Lock()
	projects := make([]*projectBrowser, 0, len(m.projects))
	for _, p := range m.projects {
		projects = append(projects, p)
	}
	m.projects = make(map[string]*projectBrowser)
	m.mu.Unlock()
	for _, p := range projects {
		p.close()
	}
}

func (m *manager) dispatch(serverCtx context.Context, req Request) (any, error) {
	action := normalizeAction(req.Action)
	if action == "" {
		return nil, fail("invalid_request", "action is required")
	}
	if req.Params == nil {
		req.Params = make(map[string]any)
	}
	if action == "status" {
		return m.status(), nil
	}
	if err := validateThread(req.Thread); err != nil {
		return nil, err
	}
	root, err := ProjectRoot(req.Project)
	if err != nil {
		return nil, fail("invalid_project", "%v", err)
	}
	key := projectKey(root)
	if action == "session-close" {
		return m.closeSession(key, req.Thread), nil
	}
	if !knownAction(action) {
		return nil, fail("unknown_action", "unknown browser action %q", req.Action)
	}

	p := m.project(root, key)
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > 24*time.Hour {
		return nil, fail("invalid_params", "timeout may not exceed 24h")
	}
	opCtx, cancel := context.WithTimeout(serverCtx, timeout)
	defer cancel()
	if err := p.ensureStarted(opCtx); err != nil {
		if opCtx.Err() != nil {
			return nil, opCtx.Err()
		}
		return nil, fail("browser_unavailable", "start Chrome: %v", err)
	}
	isolated, err := boolParam(req.Params, "isolated", false)
	if err != nil {
		return nil, err
	}
	s, err := p.getSession(opCtx, req.Thread, isolated)
	if err != nil {
		return nil, err
	}
	// Serializing operations within a thread keeps tab selection, generated
	// refs, recording state, and form interactions deterministic. The session
	// contexts are parents of opCtx and are never cancelled by a request.
	s.mu.Lock()
	defer s.mu.Unlock()
	if opCtx.Err() != nil {
		return nil, opCtx.Err()
	}
	started := time.Now()
	data, actionErr := s.perform(opCtx, action, req.Params)
	s.writeTrace(action, started, actionErr)
	return data, actionErr
}

func knownAction(a string) bool {
	switch a {
	case "open", "snapshot", "click", "fill", "select", "press", "scroll", "wait",
		"tabs", "tab-new", "tab-select", "tab-close", "screenshot", "record-start",
		"record-stop", "console", "errors", "cdp", "cdp-listen":
		return true
	default:
		return false
	}
}

func (m *manager) project(root, key string) *projectBrowser {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.projects[key]; p != nil {
		return p
	}
	p := &projectBrowser{manager: m, root: root, key: key, sessions: make(map[string]*session)}
	m.projects[key] = p
	return p
}

func (m *manager) status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]map[string]any, 0, len(m.projects))
	for _, p := range m.projects {
		p.mu.Lock()
		item := map[string]any{"project": p.root, "running": p.started, "sessions": len(p.sessions)}
		p.mu.Unlock()
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["project"].(string) < items[j]["project"].(string) })
	return map[string]any{"daemon": true, "projects": items}
}

func (m *manager) closeSession(key, thread string) map[string]any {
	m.mu.Lock()
	p := m.projects[key]
	m.mu.Unlock()
	if p == nil {
		return map[string]any{"closed": false, "tabs": 0}
	}
	p.mu.Lock()
	s := p.sessions[thread]
	if s != nil {
		delete(p.sessions, thread)
	}
	p.mu.Unlock()
	if s == nil {
		return map[string]any{"closed": false, "tabs": 0}
	}
	s.mu.Lock()
	n := len(s.tabs)
	s.writeTrace("session-close", time.Now(), nil)
	s.closeLocked()
	s.mu.Unlock()
	return map[string]any{"closed": true, "tabs": n}
}

func (p *projectBrowser) ensureStarted(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil
	}
	base := filepath.Join(p.manager.home, "browser", "projects", p.key)
	profile := filepath.Join(base, "profile")
	if err := mkdirPrivate(profile); err != nil {
		return err
	}
	meta, _ := json.Marshal(map[string]string{"project": p.root})
	if err := os.WriteFile(filepath.Join(base, "project.json"), append(meta, '\n'), 0o600); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Join(base, "project.json"), 0o600)

	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.UserDataDir(profile),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-component-update", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.WithoutCancel(p.manager.ctx), opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	// Allocate against the durable session context. ExecAllocator binds Chrome's
	// lifetime to the first Run context, not merely NewExecAllocator's context.
	started := make(chan error, 1)
	go func() { started <- chromedp.Run(browserCtx) }()
	select {
	case err := <-started:
		if err != nil {
			browserCancel()
			allocCancel()
			return err
		}
	case <-ctx.Done():
		browserCancel()
		allocCancel()
		<-started
		return ctx.Err()
	}
	p.allocCtx, p.allocCancel = allocCtx, allocCancel
	p.browserCtx, p.browserCancel = browserCtx, browserCancel
	p.started = true
	return nil
}

func (p *projectBrowser) getSession(ctx context.Context, thread string, isolated bool) (*session, error) {
	// Keep the project lock through initialization so concurrent first requests
	// for one thread cannot observe a half-created session.
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.sessions[thread]; s != nil {
		if s.isolated != isolated && isolated {
			return nil, fail("conflict", "thread session already exists without isolation")
		}
		return s, nil
	}
	dir, err := threadDir(p.manager.home, thread)
	if err != nil {
		return nil, err
	}
	s := &session{project: p, thread: thread, dir: dir, isolated: isolated, tabs: make(map[target.ID]*browserTab)}

	if isolated {
		// Create a window explicitly: recent Chrome versions reject the first
		// target in a new incognito context unless newWindow is requested.
		browser := chromedp.FromContext(p.browserCtx).Browser
		executor := cdp.WithExecutor(ctx, browser)
		id, err := target.CreateBrowserContext().WithDisposeOnDetach(true).Do(executor)
		if err != nil {
			return nil, fail("browser_unavailable", "create isolated browser context: %v", err)
		}
		dispose := func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = target.DisposeBrowserContext(id).Do(cdp.WithExecutor(cleanup, browser))
		}
		ownerID, err := target.CreateTarget("about:blank").WithBrowserContextID(id).WithNewWindow(true).Do(executor)
		if err != nil {
			dispose()
			return nil, fail("browser_unavailable", "create isolated window: %v", err)
		}
		ownerCtx, ownerCancel := chromedp.NewContext(p.browserCtx, chromedp.WithTargetID(ownerID))
		if err := initializeContext(ownerCtx, ctx, ownerCancel); err != nil {
			ownerCancel()
			dispose()
			return nil, fail("browser_unavailable", "attach isolated window: %v", err)
		}
		s.ownerCtx = ownerCtx
		s.ownerCancel = func() { ownerCancel(); dispose() }
		s.browserContext = id
	}

	if _, err := s.newTab(ctx, "about:blank"); err != nil {
		s.closeLocked()
		return nil, err
	}
	p.sessions[thread] = s
	return s, nil
}

func linkedContext(parent, request context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(request, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (p *projectBrowser) close() {
	p.mu.Lock()
	sessions := make([]*session, 0, len(p.sessions))
	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}
	p.sessions = make(map[string]*session)
	browserCtx, browserCancel, allocCancel := p.browserCtx, p.browserCancel, p.allocCancel
	p.browserCtx, p.browserCancel, p.allocCancel = nil, nil, nil
	p.started = false
	p.mu.Unlock()
	for _, s := range sessions {
		s.mu.Lock()
		s.closeLocked()
		s.mu.Unlock()
	}
	if browserCtx != nil && browserCtx.Err() == nil {
		// Browser.close lets Chrome flush cookies and storage. Abrupt context
		// cancellation can lose newly authenticated sessions on shutdown.
		closeCtx, closeCancel := context.WithTimeout(browserCtx, 5*time.Second)
		_ = chromedp.Cancel(closeCtx)
		closeCancel()
	}
	if browserCancel != nil {
		browserCancel()
	}
	if allocCancel != nil {
		allocCancel()
	}
}

func (s *session) newTab(ctx context.Context, url string) (*browserTab, error) {
	var tabCtx context.Context
	var cancel context.CancelFunc
	if s.isolated {
		tabCtx, cancel = chromedp.NewContext(s.project.browserCtx, chromedp.WithExistingBrowserContext(s.browserContext))
	} else {
		tabCtx, cancel = chromedp.NewContext(s.project.browserCtx)
	}
	t := &browserTab{ctx: tabCtx, cancel: cancel}
	t.installListeners()
	if err := initializeContext(tabCtx, ctx, cancel, runtime.Enable(), log.Enable()); err != nil {
		return nil, fail("browser_unavailable", "initialize tab: %v", err)
	}
	runCtx, runCancel := linkedContext(tabCtx, ctx)
	defer runCancel()
	var actions []chromedp.Action
	if url != "" && url != "about:blank" {
		actions = append(actions, chromedp.Navigate(url))
	}
	if err := chromedp.Run(runCtx, actions...); err != nil {
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail("action_failed", "create tab: %v", err)
	}
	t.id = chromedp.FromContext(tabCtx).Target.TargetID
	s.tabs[t.id] = t
	s.order = append(s.order, t.id)
	s.selected = t.id
	return t, nil
}

func (t *browserTab) installListeners() {
	chromedp.ListenTarget(t.ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			args := make([]any, 0, len(e.Args))
			for _, arg := range e.Args {
				if len(arg.Value) != 0 {
					var v any
					if json.Unmarshal(arg.Value, &v) == nil {
						args = append(args, v)
						continue
					}
				}
				if arg.Description != "" {
					args = append(args, arg.Description)
				} else {
					args = append(args, string(arg.Type))
				}
			}
			t.appendConsole(map[string]any{"type": e.Type, "args": args, "timestamp": e.Timestamp})
		case *runtime.EventExceptionThrown:
			t.appendError(map[string]any{"text": e.ExceptionDetails.Text, "line": e.ExceptionDetails.LineNumber, "column": e.ExceptionDetails.ColumnNumber, "url": e.ExceptionDetails.URL, "timestamp": e.Timestamp})
		case *log.EventEntryAdded:
			entry := e.Entry
			item := map[string]any{"level": entry.Level, "text": entry.Text, "source": entry.Source, "url": entry.URL, "line": entry.LineNumber, "timestamp": entry.Timestamp}
			if entry.Level == log.LevelError || entry.Level == log.LevelWarning {
				t.appendError(item)
			} else {
				t.appendConsole(item)
			}
		default:
			t.handleRecordingEvent(ev)
		}
	})
}

func (t *browserTab) appendConsole(v any) {
	t.eventsMu.Lock()
	t.console = appendBounded(t.console, v)
	t.eventsMu.Unlock()
}
func (t *browserTab) appendError(v any) {
	t.eventsMu.Lock()
	t.errors = appendBounded(t.errors, v)
	t.eventsMu.Unlock()
}
func appendBounded(in []any, v any) []any {
	const max = 500
	if len(in) >= max {
		copy(in, in[len(in)-max+1:])
		in = in[:max-1]
	}
	return append(in, v)
}

func (s *session) selectedTab() (*browserTab, error) {
	t := s.tabs[s.selected]
	if t == nil {
		return nil, fail("not_found", "no selected tab")
	}
	return t, nil
}

func (s *session) closeLocked() {
	if s.recording != nil {
		_, _ = s.stopRecordingLocked(context.Background())
	}
	for _, id := range s.order {
		if t := s.tabs[id]; t != nil {
			t.cancel()
		}
	}
	s.tabs = make(map[target.ID]*browserTab)
	s.order = nil
	s.selected = ""
	if s.ownerCancel != nil {
		s.ownerCancel()
		s.ownerCancel = nil
	}
}

func boolParam(params map[string]any, key string, fallback bool) (bool, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return fallback, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fail("invalid_params", "%s must be a boolean", key)
	}
	return b, nil
}

func stringParam(params map[string]any, key string, required bool) (string, error) {
	v, ok := params[key]
	if !ok || v == nil {
		if required {
			return "", fail("invalid_params", "%s is required", key)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fail("invalid_params", "%s must be a string", key)
	}
	if required && s == "" {
		return "", fail("invalid_params", "%s may not be empty", key)
	}
	return s, nil
}

func numberParam(params map[string]any, key string, fallback float64) (float64, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return fallback, nil
	}
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		f, err := n.Float64()
		if err == nil {
			return f, nil
		}
	}
	return 0, fail("invalid_params", "%s must be a number", key)
}

// chromedp binds its browser/target read loops to the first Run context.
// Initialize with a durable context; cancellation only tears it down on failure.
func initializeContext(durable, request context.Context, cancel context.CancelFunc, actions ...chromedp.Action) error {
	done := make(chan error, 1)
	go func() { done <- chromedp.Run(durable, actions...) }()
	select {
	case err := <-done:
		if err != nil {
			cancel()
		}
		return err
	case <-request.Done():
		cancel()
		<-done
		return request.Err()
	}
}

package controlplane

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/TheMarstonConnell/ted/browser"
	"github.com/gorilla/websocket"
)

const (
	maxBrowserInput        = 64 << 10
	maxBrowserOutput       = 4 << 20
	browserQueueSize       = 32
	maxBrowserViewers      = 32
	maxAgentBrowserViewers = 4
)

type browserLiveConnection interface {
	Send(browser.LiveCommand) error
	Receive() (browser.LiveEvent, error)
	Close() error
}

type browserLiveConnector func(context.Context, browser.Request) (browserLiveConnection, error)

func openBrowserLive(ctx context.Context, req browser.Request) (browserLiveConnection, error) {
	return browser.OpenLive(ctx, req)
}

type browserSessionCloser func(context.Context, string, string, string) error

func closeBrowserSession(ctx context.Context, home, project, thread string) error {
	return browser.CloseSessionIfRunning(ctx, home, project, thread)
}

func (s *Service) beginBrowserViewer(id string, cancel context.CancelFunc) (browser.Request, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return browser.Request{}, nil, problem(503, "shutting_down", "server is shutting down")
	}
	a, err := s.recordLocked(id)
	if err != nil {
		return browser.Request{}, nil, err
	}
	project, ok := s.state.Projects[a.Agent.ProjectID]
	if !ok || !filepath.IsAbs(project.Root) {
		return browser.Request{}, nil, problem(409, "workspace_unavailable", "browser project directory is unavailable")
	}
	req := browser.Request{Project: project.Root, Thread: a.Agent.ID, Home: filepath.Join(s.dir, "runtime")}
	total := 0
	for _, clients := range s.browserClients {
		total += len(clients)
	}
	if total >= maxBrowserViewers || len(s.browserClients[id]) >= maxAgentBrowserViewers {
		return browser.Request{}, nil, problem(503, "browser_limit", "too many browser viewers")
	}
	if s.browserClients == nil {
		s.browserClients = make(map[string]map[uint64]context.CancelFunc)
	}
	if s.browserClients[id] == nil {
		s.browserClients[id] = make(map[uint64]context.CancelFunc)
	}
	s.nextBrowserClient++
	token := s.nextBrowserClient
	s.browserClients[id][token] = cancel
	return req, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		clients := s.browserClients[id]
		delete(clients, token)
		if len(clients) == 0 {
			delete(s.browserClients, id)
		}
	}, nil
}

func (s *Service) cancelBrowserViewersLocked(id string) {
	for _, cancel := range s.browserClients[id] {
		cancel()
	}
}

func (s *Service) closeBrowserSessionLocked(id, project string) error {
	s.cancelBrowserViewersLocked(id)
	closer := s.browserClose
	if closer == nil {
		closer = closeBrowserSession
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := closer(ctx, filepath.Join(s.dir, "runtime"), project, id)
	cancel()
	return err
}

func (h *httpAPI) AgentBrowser(w http.ResponseWriter, r *http.Request, id string) {
	if !sameOrigin(r) {
		writeProblem(w, 403, "forbidden", "browser Origin must match this server")
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		writeProblem(w, 400, "invalid", "expected WebSocket upgrade")
		return
	}
	key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(key) != 16 || r.Header.Get("Sec-WebSocket-Version") != "13" {
		writeProblem(w, 400, "invalid", "invalid WebSocket handshake")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	req, release, err := h.service.beginBrowserViewer(id, cancel)
	if err != nil {
		cancel()
		writeRuntimeError(w, err)
		return
	}
	defer release()
	root, err := canonicalWorkspaceDirectory(req.Project)
	if err == nil {
		root, err = browser.ProjectRoot(root)
	}
	if err != nil {
		cancel()
		writeProblem(w, 409, "workspace_unavailable", "browser project directory is unavailable")
		return
	}
	req.Project = root
	defer cancel()
	live, err := h.browserConnect(ctx, req)
	if err != nil {
		writeProblem(w, 503, "browser_unavailable", "cannot connect to browser service")
		return
	}
	defer live.Close()
	upgrader := websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096, HandshakeTimeout: wsWriteTimeout,
		CheckOrigin: sameOrigin,
		Error: func(w http.ResponseWriter, r *http.Request, status int, _ error) {
			writeProblem(w, status, "invalid", "invalid WebSocket handshake")
		},
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	h.bridgeBrowser(ctx, c, live)
}

type browserFailure struct {
	code, message string
	closeCode     int
}

func (h *httpAPI) bridgeBrowser(parent context.Context, c *websocket.Conn, live browserLiveConnection) {
	ctx, cancel := context.WithCancel(parent)
	commands := make(chan browser.LiveCommand, browserQueueSize)
	events := make(chan browser.LiveEvent, browserQueueSize)
	frames := make(chan browser.LiveEvent, 1)
	failures := make(chan browserFailure, 1)
	fail := func(code, message string, closeCode int) {
		select {
		case failures <- browserFailure{code, message, closeCode}:
		default:
		}
	}
	emit := func(event browser.LiveEvent) bool {
		select {
		case events <- event:
			return true
		case <-ctx.Done():
			return false
		default:
			fail("slow_consumer", "browser event queue overflow", websocket.ClosePolicyViolation)
			return false
		}
	}
	var workers sync.WaitGroup
	defer func() {
		cancel()
		_ = c.Close()
		_ = live.Close()
		workers.Wait()
	}()
	c.SetReadLimit(maxBrowserInput)
	_ = c.SetReadDeadline(time.Now().Add(wsPongTimeout))
	c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(wsPongTimeout)) })
	workers.Go(func() {
		tokens, last, violations := 240.0, time.Now(), 0
		for {
			kind, data, err := c.ReadMessage()
			if err != nil {
				fail("disconnected", "browser viewer disconnected", websocket.CloseNormalClosure)
				return
			}
			now := time.Now()
			tokens = min(240, tokens+now.Sub(last).Seconds()*120)
			last = now
			if tokens < 1 {
				fail("rate_limit", "browser command rate exceeded", websocket.ClosePolicyViolation)
				return
			}
			tokens--
			if kind != websocket.TextMessage {
				fail("invalid", "only JSON text messages are accepted", websocket.CloseUnsupportedData)
				return
			}
			command, err := h.validateBrowserCommand(data)
			if err != nil {
				violations++
				if violations >= 8 {
					fail("invalid", "too many invalid browser commands", websocket.ClosePolicyViolation)
					return
				}
				if !emit(browser.LiveEvent{Type: "error", Code: "invalid", Message: err.Error()}) {
					return
				}
				continue
			}
			select {
			case commands <- command:
			case <-ctx.Done():
				return
			default:
				fail("backpressure", "browser command queue overflow", websocket.ClosePolicyViolation)
				return
			}
		}
	})
	workers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case command := <-commands:
				if err := live.Send(command); err != nil {
					fail("browser_unavailable", "browser command transport failed", websocket.CloseInternalServerErr)
					return
				}
			}
		}
	})
	workers.Go(func() {
		for {
			event, err := live.Receive()
			if err != nil {
				fail("browser_unavailable", "browser service disconnected", websocket.CloseInternalServerErr)
				return
			}
			if event.Type == "frame" {
				// Frames are replaceable; state and errors are not.
				select {
				case <-frames:
				default:
				}
				select {
				case frames <- event:
				case <-ctx.Done():
					return
				}
			} else if !emit(event) {
				return
			}
		}
	})
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	tooLarge := errors.New("browser event exceeds output limit")
	write := func(event browser.LiveEvent) error {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if len(data) > maxBrowserOutput {
			return tooLarge
		}
		if err := c.SetWriteDeadline(time.Now().Add(wsWriteTimeout)); err != nil {
			return err
		}
		return c.WriteMessage(websocket.TextMessage, data)
	}
	terminate := func(f browserFailure) {
		_ = write(browser.LiveEvent{Type: "error", Code: f.code, Message: f.message})
		_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(f.closeCode, f.code), time.Now().Add(wsWriteTimeout))
	}
	deliver := func(event browser.LiveEvent) bool {
		if err := write(event); err != nil {
			if errors.Is(err, tooLarge) {
				terminate(browserFailure{"too_large", "browser event exceeds output limit", websocket.CloseMessageTooBig})
			}
			return false
		}
		return true
	}
	for {
		// Prioritize reliable events over replaceable frames.
		select {
		case event := <-events:
			if !deliver(event) {
				return
			}
		default:
		}
		select {
		case <-ctx.Done():
			terminate(browserFailure{"shutting_down", "browser subscription ended", websocket.CloseGoingAway})
			return
		case f := <-failures:
			terminate(f)
			return
		case <-ticker.C:
			if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout)); err != nil {
				return
			}
		case event := <-events:
			if !deliver(event) {
				return
			}
		case frame := <-frames:
			if !deliver(frame) {
				return
			}
		}
	}
}

func (h *httpAPI) validateBrowserCommand(data []byte) (browser.LiveCommand, error) {
	var command browser.LiveCommand
	if !utf8.Valid(data) {
		return command, fmt.Errorf("invalid UTF-8 command")
	}
	invalid := func(message string) (browser.LiveCommand, error) { return command, fmt.Errorf("%s", message) }
	// Reject duplicate keys rather than silently selecting the last value.
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return invalid("expected a JSON object")
	}
	raw := map[string]any{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return invalid("invalid JSON command")
		}
		key, ok := token.(string)
		if !ok {
			return invalid("invalid JSON command")
		}
		if _, exists := raw[key]; exists {
			return invalid("duplicate command field")
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return invalid("invalid JSON command")
		}
		raw[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return invalid("invalid JSON command")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid("expected exactly one JSON object")
	}
	if err := h.spec.Components.Schemas["BrowserLiveCommand"].Value.VisitJSON(raw); err != nil {
		return invalid("invalid browser command fields")
	}
	for _, field := range []struct {
		name  string
		limit int
	}{{"tab_id", 256}, {"key", 128}, {"code", 128}} {
		if text, ok := raw[field.name].(string); ok && len(text) > field.limit {
			return invalid("browser command string exceeds byte limit")
		}
	}
	kind := raw["type"].(string)
	allowed := map[string]bool{"type": true, "tab_id": true}
	require := []string{}
	switch kind {
	case "watch":
	case "new":
		delete(allowed, "tab_id")
		allowed["url"] = true
	case "navigate":
		allowed["url"] = true
		require = append(require, "url")
	case "back", "forward", "reload", "close", "release":
		require = append(require, "tab_id")
	case "mouse":
		for _, key := range []string{"event", "x", "y", "delta_x", "delta_y", "button", "buttons", "click_count", "modifiers"} {
			allowed[key] = true
		}
		require = append(require, "tab_id", "event", "x", "y")
		if event, _ := raw["event"].(string); event != "mouseMoved" && event != "mousePressed" && event != "mouseReleased" && event != "mouseWheel" {
			return invalid("invalid mouse event")
		}
	case "key":
		for _, key := range []string{"event", "key", "code", "text", "modifiers", "key_code"} {
			allowed[key] = true
		}
		require = append(require, "tab_id", "event")
		if (raw["key"] == nil || raw["key"] == "") && (raw["code"] == nil || raw["code"] == "") {
			return invalid("key or code is required")
		}
		if event, _ := raw["event"].(string); event != "keyDown" && event != "keyUp" {
			return invalid("invalid key event")
		}
	case "text":
		allowed["text"] = true
		require = append(require, "tab_id", "text")
	}
	if rawURL, ok := raw["url"].(string); ok {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return invalid("invalid URL")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "file", "about", "data":
		default:
			return invalid("unsupported URL scheme")
		}
	}
	if kind == "mouse" && (raw["event"] == "mousePressed" || raw["event"] == "mouseReleased") && (raw["button"] == nil || raw["button"] == "none") {
		return invalid("mouse button is required")
	}
	for key := range raw {
		if !allowed[key] {
			return invalid("field is not allowed for this command")
		}
	}
	for _, key := range require {
		value, present := raw[key]
		if !present || value == "" {
			return invalid("missing required command field: " + key)
		}
	}
	if err := json.Unmarshal(data, &command); err != nil {
		return invalid("invalid typed browser command")
	}
	return command, nil
}

package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TheMarstonConnell/ted/api"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gorilla/websocket"
)

const (
	maxWSFrame     = 64 << 10
	wsWriteTimeout = 5 * time.Second
	wsPongTimeout  = 60 * time.Second
)

type wsInput struct {
	data []byte
	err  error
}
type wsSubscription struct {
	all                  bool
	explicit             map[string]bool
	eventAgents          map[string]bool
	eventAgentsValidated bool
	known                map[string]bool
	cursors              map[string]uint64
	inventory            map[string][]byte
	wake                 <-chan struct{}
}

type publicOriginKey struct{}

// WithPublicOrigin pins browser Origin validation to an operator-configured origin.
// Forwarded headers are never used to establish trust.
func WithPublicOrigin(next http.Handler, origin string) (http.Handler, error) {
	if origin == "" {
		return next, nil
	}
	u, err := url.Parse(origin)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || strings.ContainsAny(origin, "?#") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("public origin must be an http(s) origin without path, credentials, query or fragment")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), publicOriginKey{}, u)))
	}), nil
}

func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	u, err := url.Parse(origins[0])
	if err != nil || u.User != nil || strings.ContainsAny(origins[0], "?#") || u.Path != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if public, ok := r.Context().Value(publicOriginKey{}).(*url.URL); ok {
		scheme, host = public.Scheme, public.Host
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, host)
}

func (h *httpAPI) WebSocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096, HandshakeTimeout: wsWriteTimeout,
		CheckOrigin: sameOrigin,
		Error: func(w http.ResponseWriter, r *http.Request, status int, reason error) {
			code := "invalid"
			if status == 403 {
				code = "forbidden"
			}
			writeProblem(w, status, code, reason.Error())
		},
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	done := make(chan struct{})
	defer close(done)
	inputs := make(chan wsInput, 1)
	_ = c.SetReadDeadline(time.Now().Add(wsPongTimeout))
	c.SetReadLimit(maxSubmitBody)
	c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(wsPongTimeout)) })
	go func() {
		for {
			kind, data, err := c.ReadMessage()
			if err == nil && kind != websocket.TextMessage {
				err = fmt.Errorf("only JSON text messages are accepted")
			}
			select {
			case inputs <- wsInput{data, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	var sub *wsSubscription
	for {
		var wake <-chan struct{}
		if sub != nil {
			wake = sub.wake
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout)); err != nil {
				return
			}
		case <-wake:
			agents, events, next, err := h.snapshotWS(sub)
			if err != nil {
				_ = writeWSError(c, "", err)
				return
			}
			sub.wake = next
			if err = h.deliverWS(c, sub, agents, events); err != nil {
				return
			}
		case input := <-inputs:
			if input.err != nil {
				return
			}
			command, err := h.validateWS(input.data)
			if err != nil {
				if writeWSError(c, command.requestID, err) != nil {
					return
				}
				continue
			}
			requestID := command.requestID
			switch command.kind {
			case "subscribe":
				candidate := command.subscription
				agents, events, next, err := h.snapshotWS(candidate)
				if err != nil {
					if writeWSError(c, requestID, err) != nil {
						return
					}
					continue
				}
				candidate.wake = next
				if err = writeWS(c, api.WSSubscribed{Type: api.Subscribed, RequestId: requestID}); err != nil {
					return
				}
				sub = candidate
				if err = h.deliverWS(c, sub, agents, events); err != nil {
					return
				}
			case "submit":
				m, err := h.service.SubmitMessage(command.agentID, command.message, command.idempotencyKey)
				if err != nil {
					if writeWSError(c, requestID, err) != nil {
						return
					}
					continue
				}
				if err = writeWS(c, api.WSAck{Type: api.Ack, RequestId: requestID, AgentId: command.agentID, MessageId: m.ID, Status: m.Status}); err != nil {
					return
				}
			}
		}
	}
}

// Both transports validate requests against shared, generated-spec schemas.
type wsCommand struct {
	kind           string
	requestID      string
	subscription   *wsSubscription
	agentID        string
	message        SubmitMessageRequest
	idempotencyKey string
}

func (h *httpAPI) validateWS(data []byte) (command wsCommand, err error) {
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if e := decoder.Decode(&raw); e != nil {
		return command, problem(400, "invalid", "expected a JSON object")
	}
	// Decoder.Decode accepts a valid value followed by another value. Reject any
	// non-whitespace after that value to preserve json.Unmarshal's contract.
	if len(bytes.Trim(data[decoder.InputOffset():], " \t\r\n")) != 0 {
		return command, problem(400, "invalid", "expected a JSON object")
	}
	command.kind, _ = raw["type"].(string)
	command.requestID, _ = raw["request_id"].(string)
	if len(command.requestID) > 256 {
		command.requestID = ""
	}
	name := ""
	switch command.kind {
	case "subscribe":
		if len(data) > 64<<10 {
			return command, problem(413, "too_large", "subscribe frame exceeds 64 KiB")
		}
		name = "WSSubscribe"
	case "submit":
		name = "WSSubmit"
	default:
		return command, problem(400, "invalid", "unknown WebSocket command type")
	}
	if e := h.spec.Components.Schemas[name].Value.VisitJSON(raw, openapi3.SetSchemaErrorMessageCustomizer(schemaErrorMessage)); e != nil {
		return command, problem(400, "invalid", e.Error())
	}
	if command.kind == "subscribe" {
		command.subscription, err = wsSubscriptionFromJSON(raw)
	} else {
		command.agentID = raw["agent_id"].(string)
		command.message.Text = raw["text"].(string)
		if attachments, ok := raw["attachments"].([]any); ok {
			for _, item := range attachments {
				a := item.(map[string]any)
				command.message.Attachments = append(command.message.Attachments, Attachment{Name: a["name"].(string), URL: a["url"].(string)})
			}
		}
		command.message.Kind, _ = raw["kind"].(string)
		command.message.SenderAgentID, _ = raw["sender_agent_id"].(string)
		command.idempotencyKey = raw["idempotency_key"].(string)
	}
	return command, err
}

func wsSubscriptionFromJSON(raw map[string]any) (*wsSubscription, error) {
	s := &wsSubscription{explicit: map[string]bool{}, known: map[string]bool{}, cursors: map[string]uint64{}, inventory: map[string][]byte{}}
	if value, ok := raw["subscribe_all"]; ok {
		s.all = value.(bool)
	}
	if value, ok := raw["agent_ids"]; ok {
		for _, id := range value.([]any) {
			s.explicit[id.(string)] = true
		}
	}
	if value, ok := raw["event_agent_ids"]; ok {
		s.eventAgents = map[string]bool{}
		for _, id := range value.([]any) {
			s.eventAgents[id.(string)] = true
		}
	}
	if value, ok := raw["cursors"]; ok {
		for id, value := range value.(map[string]any) {
			cursor, err := strconv.ParseInt(value.(json.Number).String(), 10, 64)
			if err != nil {
				return nil, problem(400, "invalid", "cursor must be an int64: "+id)
			}
			if !s.all && !s.explicit[id] {
				return nil, problem(400, "invalid", "cursor supplied for an unsubscribed agent: "+id)
			}
			// Include resumed ids in the first atomic snapshot, even if they settled
			// while the client was offline. This delivers their terminal events.
			s.known[id] = true
			s.cursors[id] = uint64(cursor)
		}
	}
	return s, nil
}

func (h *httpAPI) snapshotWS(s *wsSubscription) ([]Agent, []Event, <-chan struct{}, error) {
	selected := map[string]bool{}
	for id := range s.explicit {
		selected[id] = true
	}
	for id := range s.known {
		selected[id] = true
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	agents, events, wake, err := h.service.snapshotFilteredEvents(
		s.cursors, s.all, ids, s.eventAgents, !s.eventAgentsValidated, true,
	)
	if err == nil {
		s.eventAgentsValidated = true
	}
	return agents, events, wake, err
}

func summaryWS(a Agent) api.AgentSummary {
	// Runtime data stays detached. Build the bounded generated wire type directly;
	// converting through JSON needlessly encoded the full Agent only to discard its
	// conversation and queue.
	summary := api.AgentSummary{
		Id:        a.ID,
		ProjectId: a.ProjectID,
		Title:     a.Title,
		Settings:  api.Settings{Model: a.Settings.Model, Effort: a.Settings.Effort},
		Settled:   a.Settled,
		State:     api.AgentSummaryState(a.State),
		Held:      a.Held,
		Cursor:    int64(a.Cursor),
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
	if a.DisplayTitle != "" {
		summary.DisplayTitle = stringPointer(a.DisplayTitle)
	}
	if a.ParentAgentID != "" {
		summary.ParentAgentId = stringPointer(a.ParentAgentID)
	}
	if a.ActiveSettings != nil {
		summary.ActiveSettings = &api.Settings{Model: a.ActiveSettings.Model, Effort: a.ActiveSettings.Effort}
	}
	last := int64(a.LastResponseCursor)
	summary.LastResponseCursor = &last
	read := int64(a.ReadCursor)
	summary.ReadCursor = &read
	summary.Workspace = wireWorkspace(a.Workspace)
	u := a.ContextUsage
	summary.ContextUsage = &api.ContextUsage{
		Model: u.Model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		EstimatedTokens: u.EstimatedTokens, ContextWindow: u.ContextWindow,
		Known: u.Known, Estimated: u.Estimated,
	}
	return summary
}

func stringPointer(v string) *string { return &v }

func wireWorkspace(w Workspace) *api.Workspace {
	result := &api.Workspace{
		Locked: w.Locked,
		Mode:   api.WorkspaceMode(w.Mode),
		Status: api.WorkspaceStatus(w.Status),
	}
	if w.BaseBranch != "" {
		result.BaseBranch = stringPointer(w.BaseBranch)
	}
	if w.BaseCommit != "" {
		result.BaseCommit = stringPointer(w.BaseCommit)
	}
	if w.Branch != "" {
		result.Branch = stringPointer(w.Branch)
	}
	if w.Error != "" {
		result.Error = stringPointer(w.Error)
	}
	if w.Path != "" {
		result.Path = stringPointer(w.Path)
	}
	if w.Shared {
		shared := true
		result.Shared = &shared
	}
	return result
}

func (h *httpAPI) deliverWS(c *websocket.Conn, s *wsSubscription, agents []Agent, events []Event) error {
	for _, a := range agents {
		summary := summaryWS(a)
		frame, err := json.Marshal(api.WSInventory{Type: api.Inventory, Agent: summary})
		if err != nil {
			return err
		}
		if !bytes.Equal(frame, s.inventory[a.ID]) {
			if err = writeWSBytes(c, frame); err != nil {
				return err
			}
			s.inventory[a.ID] = frame
		}
		s.known[a.ID] = true
	}
	for _, e := range events {
		if e.Cursor <= s.cursors[e.AgentID] {
			continue
		}
		frame, err := json.Marshal(struct {
			Type  string `json:"type"`
			Event Event  `json:"event"`
		}{"event", e})
		if err != nil {
			return err
		}
		if len(frame) > maxWSFrame {
			reference := api.WSEventReference{Type: api.EventRef, AgentId: e.AgentID, Cursor: int64(e.Cursor), Url: "/v1/agents/" + url.PathEscape(e.AgentID) + "/events/" + strconv.FormatUint(e.Cursor, 10)}
			if err = writeWS(c, reference); err != nil {
				return err
			}
		} else if err = writeWSBytes(c, frame); err != nil {
			return err
		}
		s.cursors[e.AgentID] = e.Cursor
	}
	for _, a := range agents {
		if a.Settled && a.State == "idle" && !s.explicit[a.ID] {
			delete(s.known, a.ID)
		}
	}
	return nil
}

func writeWS(c *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeWSBytes(c, data)
}
func writeWSBytes(c *websocket.Conn, data []byte) error {
	if len(data) > maxWSFrame {
		return fmt.Errorf("WebSocket frame exceeds %d bytes", maxWSFrame)
	}
	if err := c.SetWriteDeadline(time.Now().Add(wsWriteTimeout)); err != nil {
		return err
	}
	return c.WriteMessage(websocket.TextMessage, data)
}
func writeWSError(c *websocket.Conn, requestID string, err error) error {
	code, message := "internal", "internal server error"
	var e *Error
	if errors.As(err, &e) {
		code, message = e.Code, e.Message
	}
	// Schema errors may quote large client values. Do not reflect unbounded data.
	if len(message) > 2048 {
		message = message[:2048]
	}
	response := api.WSError{Type: api.WSErrorTypeError, Code: api.ErrorCode(code), Message: message}
	if requestID != "" {
		response.RequestId = &requestID
	}
	return writeWS(c, response)
}

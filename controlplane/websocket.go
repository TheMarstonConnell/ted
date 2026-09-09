package controlplane

import (
	"bytes"
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
	all       bool
	explicit  map[string]bool
	known     map[string]bool
	cursors   map[string]uint64
	inventory map[string][]byte
	wake      <-chan struct{}
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
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
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
	inputs := make(chan wsInput, 8)
	_ = c.SetReadDeadline(time.Now().Add(wsPongTimeout))
	c.SetReadLimit(maxWSFrame)
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
			kind, requestID, err := h.validateWS(input.data)
			if err != nil {
				if writeWSError(c, requestID, err) != nil {
					return
				}
				continue
			}
			switch kind {
			case "subscribe":
				var command api.WSSubscribe
				if err = json.Unmarshal(input.data, &command); err != nil {
					if writeWSError(c, requestID, problem(400, "invalid", err.Error())) != nil {
						return
					}
					continue
				}
				candidate, err := newWSSubscription(command)
				if err != nil {
					if writeWSError(c, requestID, err) != nil {
						return
					}
					continue
				}
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
				var command api.WSSubmit
				if err = json.Unmarshal(input.data, &command); err != nil {
					if writeWSError(c, requestID, problem(400, "invalid", err.Error())) != nil {
						return
					}
					continue
				}
				m, err := h.service.Submit(command.AgentId, command.Text, command.IdempotencyKey)
				if err != nil {
					if writeWSError(c, requestID, err) != nil {
						return
					}
					continue
				}
				if err = writeWS(c, api.WSAck{Type: api.Ack, RequestId: requestID, AgentId: command.AgentId, MessageId: m.ID, Status: m.Status}); err != nil {
					return
				}
			}
		}
	}
}

// Both transports validate requests against shared, generated-spec schemas.
func (h *httpAPI) validateWS(data []byte) (kind, requestID string, err error) {
	var raw map[string]any
	if e := json.Unmarshal(data, &raw); e != nil {
		return "", "", problem(400, "invalid", "expected a JSON object")
	}
	kind, _ = raw["type"].(string)
	requestID, _ = raw["request_id"].(string)
	if len(requestID) > 256 {
		requestID = ""
	}
	name := ""
	switch kind {
	case "subscribe":
		name = "WSSubscribe"
	case "submit":
		name = "WSSubmit"
	default:
		return kind, requestID, problem(400, "invalid", "unknown WebSocket command type")
	}
	if e := h.spec.Components.Schemas[name].Value.VisitJSON(raw); e != nil {
		return kind, requestID, problem(400, "invalid", e.Error())
	}
	return
}

func newWSSubscription(command api.WSSubscribe) (*wsSubscription, error) {
	s := &wsSubscription{all: value(command.SubscribeAll), explicit: map[string]bool{}, known: map[string]bool{}, cursors: map[string]uint64{}, inventory: map[string][]byte{}}
	for _, id := range value(command.AgentIds) {
		s.explicit[id] = true
	}
	for id, cursor := range value(command.Cursors) {
		if !s.all && !s.explicit[id] {
			return nil, problem(400, "invalid", "cursor supplied for an unsubscribed agent: "+id)
		}
		// Include resumed ids in the first atomic snapshot, even if they settled
		// while the client was offline. This delivers their terminal events.
		s.known[id] = true
		s.cursors[id] = uint64(cursor)
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
	return h.service.SnapshotEvents(s.cursors, s.all, ids)
}

func summaryWS(a Agent) (api.AgentSummary, error) {
	// Runtime data stays detached. The generated wire type intentionally excludes
	// conversation/queue history so inventory notifications remain bounded.
	wire := wireAgent(a)
	wire.Messages = nil
	wire.Queue = nil
	data, err := json.Marshal(wire)
	if err != nil {
		return api.AgentSummary{}, err
	}
	var summary api.AgentSummary
	err = json.Unmarshal(data, &summary)
	return summary, err
}

func (h *httpAPI) deliverWS(c *websocket.Conn, s *wsSubscription, agents []Agent, events []Event) error {
	for _, a := range agents {
		summary, err := summaryWS(a)
		if err != nil {
			return err
		}
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

package remote

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

type frame struct {
	Type    string   `json:"type"`
	Agent   Snapshot `json:"agent"`
	Event   Event    `json:"event"`
	AgentID string   `json:"agent_id"`
	Cursor  uint64   `json:"cursor"`
	Code    string   `json:"code"`
	Message string   `json:"message"`
}

func (a *Agent) dial(ctx context.Context) (*websocket.Conn, error) {
	u, err := url.Parse(a.client.BaseURL + "/v1/ws")
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	conn, res, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if res != nil && res.Body != nil {
		res.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	a.mu.RLock()
	id, cursor := a.snapshot.ID, a.cursor
	a.mu.RUnlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(map[string]any{"type": "subscribe", "request_id": newKey(), "agent_ids": []string{id}, "cursors": map[string]uint64{id: cursor}}); err != nil {
		conn.Close()
		return nil, err
	}
	// Wait for acknowledgement before initial prompt submission. Errors (for
	// example invalid cursors) must not masquerade as a live subscription.
	conn.SetReadLimit(4 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ack frame
	if err := conn.ReadJSON(&ack); err != nil {
		conn.Close()
		return nil, err
	}
	if ack.Type != "subscribed" {
		conn.Close()
		return nil, fmt.Errorf("subscribe: %s: %s", ack.Code, ack.Message)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn, nil
}

// Subscribe opens the initial connection synchronously, then replays from the
// last consumed cursor after disconnects. Inventory cursor never skips events.
// Cancel ctx to close the socket; server ownership is intentionally separate.
func (a *Agent) Subscribe(ctx context.Context, notify func(Update)) error {
	conn, err := a.dial(ctx)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	go func() {
		for {
			err := a.read(ctx, conn, notify)
			conn.Close()
			if ctx.Err() != nil {
				return
			}
			notify(Update{Err: fmt.Errorf("event connection lost: %w; reconnecting", err)})
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				conn, err = a.dial(ctx)
				if err == nil {
					break
				}
			}
		}
	}()
	return nil
}
func (a *Agent) read(ctx context.Context, conn *websocket.Conn, notify func(Update)) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	conn.SetReadLimit(4 << 20)
	for {
		var f frame
		if err := conn.ReadJSON(&f); err != nil {
			return err
		}
		switch f.Type {
		case "error":
			return fmt.Errorf("%s: %s", f.Code, f.Message)
		case "inventory":
			a.mu.Lock()
			if f.Agent.ID != a.snapshot.ID {
				a.mu.Unlock()
				continue
			}
			if f.Agent.Cursor >= a.snapshot.Cursor {
				f.Agent.Messages, f.Agent.Queue = a.snapshot.Messages, a.snapshot.Queue
				a.snapshot = f.Agent
			}
			busy := a.snapshot.State != "idle"
			a.mu.Unlock()
			notify(Update{Busy: &busy})
		case "event":
			if err := a.consume(f.Event, notify); err != nil {
				return err
			}
		case "event_ref":
			// Construct our own same-origin path; never follow an untrusted frame URL.
			var e Event
			if err := a.client.request(ctx, "GET", agentPath(f.AgentID)+"/events/"+strconv.FormatUint(f.Cursor, 10), nil, &e, ""); err != nil {
				return err
			}
			if err := a.consume(e, notify); err != nil {
				return err
			}
		}
	}
}

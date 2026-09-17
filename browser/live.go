package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"sync"
	"time"
)

// LiveCommand is a typed, tab-scoped human browser operation.
type LiveCommand struct {
	Type       string  `json:"type"`
	TabID      string  `json:"tab_id,omitempty"`
	URL        string  `json:"url,omitempty"`
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	DeltaX     float64 `json:"delta_x,omitempty"`
	DeltaY     float64 `json:"delta_y,omitempty"`
	Event      string  `json:"event,omitempty"`
	Button     string  `json:"button,omitempty"`
	Buttons    int64   `json:"buttons,omitempty"`
	ClickCount int64   `json:"click_count,omitempty"`
	Key        string  `json:"key,omitempty"`
	Code       string  `json:"code,omitempty"`
	Text       string  `json:"text,omitempty"`
	Modifiers  int64   `json:"modifiers,omitempty"`
	KeyCode    int64   `json:"key_code,omitempty"`
}

type LiveTab struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

type LiveEvent struct {
	Type     string    `json:"type"`
	Tabs     []LiveTab `json:"tabs,omitempty"`
	Selected string    `json:"selected,omitempty"`
	Pinned   string    `json:"pinned,omitempty"`
	TabID    string    `json:"tab_id,omitempty"`
	Data     string    `json:"data,omitempty"`
	Width    float64   `json:"width,omitempty"`
	Height   float64   `json:"height,omitempty"`
	X        float64   `json:"x,omitempty"`
	Y        float64   `json:"y,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Message  string    `json:"message,omitempty"`
	Code     string    `json:"code,omitempty"`
}

// LiveClient keeps one daemon subscription. Send and Receive may run concurrently.
// Canceling the OpenLive context also closes the connection.
type LiveClient struct {
	conn   net.Conn
	dec    *json.Decoder
	sendMu sync.Mutex
	once   sync.Once
	stop   func() bool
}

func OpenLive(ctx context.Context, req Request) (*LiveClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateThread(req.Thread); err != nil {
		return nil, err
	}
	path, err := socketPathForHome(req.Home)
	if err != nil {
		return nil, err
	}
	conn, err := dialDaemon(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err = startDaemonInHome(path, req.Home); err != nil {
			return nil, err
		}
		conn, err = waitForDaemon(ctx, path)
		if err != nil {
			return nil, err
		}
	}
	c := &LiveClient{conn: conn, dec: json.NewDecoder(bufio.NewReader(conn))}
	c.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	req.Action = "live"
	req.Params = nil
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err = json.NewEncoder(conn).Encode(req); err == nil {
		var response Response
		err = c.dec.Decode(&response)
		if err == nil && !response.OK {
			if response.Error != nil {
				err = response.Error
			} else {
				err = fmt.Errorf("browser live subscription refused")
			}
		}
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return c, nil
}

func (c *LiveClient) Send(command LiveCommand) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return json.NewEncoder(c.conn).Encode(command)
}
func (c *LiveClient) Receive() (LiveEvent, error) {
	var event LiveEvent
	err := c.dec.Decode(&event)
	return event, err
}
func (c *LiveClient) Close() error {
	var err error
	c.once.Do(func() {
		if c.stop != nil {
			c.stop()
		}
		err = c.conn.Close()
	})
	return err
}

func validateLiveCommand(c LiveCommand) error {
	invalid := func(message string) error { return fail("invalid_params", "%s", message) }
	if len(c.TabID) > 256 {
		return invalid("tab_id is too long")
	}
	for _, v := range []float64{c.X, c.Y, c.DeltaX, c.DeltaY} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e7 {
			return invalid("input coordinates must be finite and bounded")
		}
	}
	if c.X < 0 || c.Y < 0 {
		return invalid("input coordinates must be nonnegative")
	}
	if c.Modifiers < 0 || c.Modifiers > 15 || c.Buttons < 0 || c.Buttons > 7 || c.ClickCount < 0 || c.ClickCount > 3 || c.KeyCode < 0 || c.KeyCode > 65535 {
		return invalid("input flags are out of range")
	}
	if len(c.Key) > 128 || len(c.Code) > 128 || len(c.Text) > 64<<10 || len(c.URL) > 64<<10 {
		return invalid("input string is too long")
	}
	switch c.Type {
	case "watch":
	case "new":
		if c.TabID != "" {
			return invalid("new does not accept tab_id")
		}
		if c.URL != "" {
			return validateURL(c.URL)
		}
	case "navigate":
		if c.URL == "" {
			return invalid("navigate requires url")
		}
		return validateURL(c.URL)
	case "back", "forward", "reload", "close", "release":
		if c.TabID == "" {
			return invalid("command requires explicit tab_id")
		}
	case "mouse":
		if c.TabID == "" {
			return invalid("mouse requires explicit tab_id")
		}
		switch c.Event {
		case "mouseMoved", "mousePressed", "mouseReleased", "mouseWheel":
		default:
			return invalid("unsupported mouse event")
		}
		switch c.Button {
		case "", "none", "left", "middle", "right":
		default:
			return invalid("unsupported mouse button")
		}
		if (c.Event == "mousePressed" || c.Event == "mouseReleased") && (c.Button == "" || c.Button == "none") {
			return invalid("mouse button is required")
		}
	case "key":
		if c.TabID == "" {
			return invalid("key requires explicit tab_id")
		}
		if c.Event != "keyDown" && c.Event != "keyUp" {
			return invalid("unsupported key event")
		}
		if c.Key == "" && c.Code == "" {
			return invalid("key or code is required")
		}
	case "text":
		if c.TabID == "" {
			return invalid("text requires explicit tab_id")
		}
	default:
		return invalid("unsupported live command")
	}
	return nil
}

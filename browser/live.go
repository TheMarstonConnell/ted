package browser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"slices"
	"sync"
	"time"
	"unicode/utf8"
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

// Mouse coordinates are required on the wire, including at the viewport origin.
func (c LiveCommand) MarshalJSON() ([]byte, error) {
	type commandJSON LiveCommand
	if c.Type == "mouse" {
		return json.Marshal(struct {
			commandJSON
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}{commandJSON(c), c.X, c.Y})
	}
	return json.Marshal(commandJSON(c))
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

// LiveClient supports one sender and one receiver concurrently.
// Canceling the OpenLive context also closes the connection.
type LiveClient struct {
	conn net.Conn
	dec  *json.Decoder
	once sync.Once
	stop func() bool
}

func OpenLive(ctx context.Context, req Request) (*LiveClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateThread(req.Thread); err != nil {
		return nil, err
	}
	conn, err := connectDaemon(ctx, req.Home)
	if err != nil {
		return nil, err
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

var liveCommandFields = map[string][]string{
	"watch":    {"tab_id"},
	"new":      {"url"},
	"navigate": {"tab_id", "url"},
	"back":     {"tab_id"}, "forward": {"tab_id"}, "reload": {"tab_id"}, "close": {"tab_id"}, "release": {"tab_id"},
	"mouse": {"tab_id", "event", "x", "y", "delta_x", "delta_y", "button", "buttons", "click_count", "modifiers"},
	"key":   {"tab_id", "event", "key", "code", "text", "modifiers", "key_code"},
	"text":  {"tab_id", "text"},
}

// ParseLiveCommand is shared by the HTTP and local-daemon boundaries.
func ParseLiveCommand(data []byte) (LiveCommand, error) {
	var command LiveCommand
	invalid := func(message string) (LiveCommand, error) {
		return command, fail("invalid_params", "%s", message)
	}
	if !utf8.Valid(data) {
		return invalid("invalid UTF-8 command")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
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
		if err := decoder.Decode(&value); err != nil || value == nil {
			return invalid("invalid command value")
		}
		raw[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return invalid("invalid JSON command")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid("expected exactly one JSON object")
	}
	if err := json.Unmarshal(data, &command); err != nil {
		return invalid("invalid typed browser command")
	}
	allowed, ok := liveCommandFields[command.Type]
	if !ok {
		return invalid("unsupported live command")
	}
	for key := range raw {
		if key != "type" && !slices.Contains(allowed, key) {
			return invalid("field is not allowed for this command")
		}
	}
	if command.Type == "mouse" && (raw["x"] == nil || raw["y"] == nil) {
		return invalid("mouse coordinates are required")
	}
	for _, key := range []string{"url", "key", "button"} {
		if raw[key] == "" {
			return invalid(key + " must not be empty")
		}
	}
	if err := validateLiveCommand(command); err != nil {
		return command, err
	}
	return command, nil
}

func validateLiveCommand(c LiveCommand) error {
	invalid := func(message string) error { return fail("invalid_params", "%s", message) }
	if utf8.RuneCountInString(c.TabID) > 256 {
		return invalid("tab_id is too long")
	}
	for _, v := range []float64{c.X, c.Y, c.DeltaX, c.DeltaY} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 100000 {
			return invalid("input coordinates must be finite and bounded")
		}
	}
	if c.X < 0 || c.Y < 0 {
		return invalid("input coordinates must be nonnegative")
	}
	if c.Modifiers < 0 || c.Modifiers > 15 || c.Buttons < 0 || c.Buttons > 7 || c.ClickCount < 0 || c.ClickCount > 3 || c.KeyCode < 0 || c.KeyCode > 65535 {
		return invalid("input flags are out of range")
	}
	if utf8.RuneCountInString(c.Key) > 128 || utf8.RuneCountInString(c.Code) > 128 || utf8.RuneCountInString(c.Text) > 16384 || utf8.RuneCountInString(c.URL) > 8192 {
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
		if c.Text == "" {
			return invalid("text is required")
		}
		if c.TabID == "" {
			return invalid("text requires explicit tab_id")
		}
	default:
		return invalid("unsupported live command")
	}
	return nil
}

// Package browser provides a persistent, project-scoped browser automation
// service. Call is the client entry point and Serve runs the local daemon.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const defaultTimeout = 30 * time.Second

// Request is one browser operation. Timeout is encoded as a Go duration (in
// nanoseconds by encoding/json); a non-positive timeout defaults to 30s.
type Request struct {
	Project string         `json:"project"`
	Thread  string         `json:"thread"`
	Action  string         `json:"action"`
	Params  map[string]any `json:"params"`
	Timeout time.Duration  `json:"timeout"`
}

// Response is returned for both successful operations and expected operation
// failures. A transport/startup failure is instead returned as Call's error.
type Response struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// Error is a stable, machine-readable service error.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

type serviceError struct {
	code string
	err  error
}

func (e *serviceError) Error() string { return e.err.Error() }
func (e *serviceError) Unwrap() error { return e.err }

func fail(code, format string, args ...any) error {
	return &serviceError{code: code, err: fmt.Errorf(format, args...)}
}

func errorResponse(err error) Response {
	if err == nil {
		return Response{OK: true}
	}
	code := "internal"
	var se *serviceError
	if errors.As(err, &se) {
		code = se.code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	}
	return Response{OK: false, Error: &Error{Code: code, Message: err.Error()}}
}

func decodeParams(v any, dst any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

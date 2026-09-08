package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRetryableCompletionErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"peer reset", errors.New("could not read responses stream: stream error: stream ID 13; INTERNAL_ERROR; received from peer"), true},
		{"refused stream", errors.New("stream error: stream ID 1; REFUSED_STREAM; received from peer"), true},
		{"truncated", fmt.Errorf("reading: %w", io.ErrUnexpectedEOF), true},
		{"eof", io.EOF, true},
		{"cancelled", context.Canceled, false},
		{"bad input", errors.New("invalid request"), false},
		{"internal API prose", errors.New("INTERNAL_ERROR"), false},
		{"auth", &statusError{401, errors.New("unauthorized")}, false},
		{"bad request", &statusError{400, errors.New("bad")}, false},
		{"rate limit", &statusError{429, errors.New("busy")}, true},
		{"upstream", &statusError{502, errors.New("gateway")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableCompletionError(tc.err); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestCompletionRetryBoundsAndBackoff(t *testing.T) {
	for _, transient := range []bool{true, false} {
		calls := 0
		problem := errors.New("invalid request")
		if transient {
			problem = io.ErrUnexpectedEOF
		}
		p := &fakeProvider{complete: func(CompletionRequest) (*Response, error) { calls++; return nil, problem }}
		var delays []time.Duration
		_, err := retryCompletion(zap.NewNop(), p, CompletionRequest{}, func(d time.Duration) { delays = append(delays, d) })
		if !errors.Is(err, problem) {
			t.Fatal(err)
		}
		want := 1
		if transient {
			want = completionAttempts
		}
		if calls != want || len(delays) != want-1 {
			t.Fatalf("calls=%d delays=%v", calls, delays)
		}
		for i, d := range delays {
			base := time.Second * time.Duration(1<<i)
			if d < base || d >= base+base/2 {
				t.Fatalf("delay=%v", d)
			}
		}
	}
}

func TestCompletionRetryPreservesRequest(t *testing.T) {
	request := CompletionRequest{Model: "test/model", Effort: EffortHigh, Messages: []Message{{Role: "tool", ToolCallId: "done", Content: TextContent("already executed")}}}
	calls := 0
	p := &fakeProvider{complete: func(got CompletionRequest) (*Response, error) {
		calls++
		if !reflect.DeepEqual(got, request) {
			t.Fatalf("request changed: %#v", got)
		}
		got.Messages[0].Role = "mutated"
		if calls < 3 {
			return &Response{}, io.ErrUnexpectedEOF
		}
		return &Response{Id: "success"}, nil
	}}
	res, err := retryCompletion(zap.NewNop(), p, request, func(time.Duration) {})
	if err != nil || res.Id != "success" || calls != 3 {
		t.Fatalf("%v %v %d", res, err, calls)
	}
}

func TestTurnRetryDoesNotReplayBash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "count")
	calls := 0
	p := settingsProvider()
	p.complete = func(req CompletionRequest) (*Response, error) {
		calls++
		switch calls {
		case 1:
			return &Response{Choices: []Choice{{FinishReason: "tool_calls", Message: Message{Role: "assistant", ToolCalls: []ToolCall{{Id: "once", Function: FunctionCall{Name: "bash", Arguments: fmt.Sprintf(`{"command":%q}`, "echo executed >> "+path)}}}}}}}, nil
		case 2:
			return nil, io.ErrUnexpectedEOF
		default:
			found := false
			for _, m := range req.Messages {
				if m.Role == "tool" && m.ToolCallId == "once" {
					found = true
				}
			}
			if !found {
				t.Fatal("lost executed tool result")
			}
			return &Response{Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant", Content: TextContent("done")}}}}, nil
		}
	}
	a := NewAgent(nil, []Provider{p})
	if err := a.Turn("run once"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "executed\n" || calls != 3 {
		t.Fatalf("%q %v calls=%d", data, err, calls)
	}
}

package agent

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

type failedStreamReader struct{ err error }

func (r failedStreamReader) Read([]byte) (int, error) { return 0, r.err }

func TestRetryDiscardsPartialStreamToolCalls(t *testing.T) {
	reset := errors.New("stream error: stream ID 13; INTERNAL_ERROR; received from peer")
	partial := `data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_partial","call_id":"partial","name":"bash","arguments":"{\"command\":\"exit 1\"}"}}` + "\n\n"
	attempts := 0
	p := &fakeProvider{complete: func(CompletionRequest) (*Response, error) {
		attempts++
		var reader io.Reader = io.MultiReader(strings.NewReader(partial), failedStreamReader{reset})
		if attempts == 2 {
			reader = strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"finished\",\"status\":\"completed\",\"output\":[]}}\n\n")
		}
		response, err := readResponsesStream(zap.NewNop(), reader)
		if err != nil {
			return nil, err
		}
		return buildChatResponse(response)
	}}
	notices := 0
	res, err := retryCompletion(zap.NewNop(), p, CompletionRequest{}, func(time.Duration) {}, func(int, time.Duration) { notices++ })
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || notices != 1 {
		t.Fatalf("attempts=%d notices=%d", attempts, notices)
	}
	for _, choice := range res.Choices {
		if len(choice.Message.ToolCalls) != 0 {
			t.Fatal("partial tool leaked", choice)
		}
	}
}

func TestUnfinishedResponsesStreamIsRetryable(t *testing.T) {
	response, err := readResponsesStream(zap.NewNop(), strings.NewReader("data: {\"type\":\"response.created\"}\n\n"))
	if response != nil || !errors.Is(err, io.ErrUnexpectedEOF) || !retryableCompletionError(err) {
		t.Fatalf("%v %v", response, err)
	}
}

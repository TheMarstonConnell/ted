package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
)

const completionAttempts = 4 // initial request plus three retries

// statusError keeps HTTP status classification independent of provider prose.
type statusError struct {
	status int
	err    error
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

func retryableCompletionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var status *statusError
	if errors.As(err, &status) {
		return status.status == 408 || status.status == 429 || status.status == 500 || status.status == 502 || status.status == 503 || status.status == 504
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return true
	}
	// net/http's HTTP/2 implementation uses an unexported stream error type.
	// Match its specific transport signature, not arbitrary API error messages.
	text := err.Error()
	return strings.Contains(text, "stream error: stream ID ") &&
		(strings.Contains(text, "; INTERNAL_ERROR; received from peer") || strings.Contains(text, "; REFUSED_STREAM; received from peer"))
}

// Retry only Complete, never Turn: previous tool results stay in the request
// and no shell command is replayed. Providers buffer partial streams, so an
// interrupted attempt cannot expose partial tool calls or assistant output.
func completeWithRetry(logger *zap.Logger, provider Provider, request CompletionRequest, notify ...func(int, time.Duration)) (*Response, error) {
	return retryCompletion(logger, provider, request, time.Sleep, notify...)
}

func retryCompletion(logger *zap.Logger, provider Provider, request CompletionRequest, sleep func(time.Duration), notify ...func(int, time.Duration)) (*Response, error) {
	for attempt := 1; ; attempt++ {
		// Give every attempt a detached copy, even if a provider mutates its input.
		current := request
		current.Messages = cloneMessages(request.Messages)
		result, err := provider.Complete(logger, current)
		if err == nil {
			return result, nil
		}
		if !retryableCompletionError(err) {
			return nil, err
		}
		if attempt == completionAttempts {
			return nil, fmt.Errorf("after %d attempts: %w", attempt, err)
		}
		base := time.Second * time.Duration(1<<(attempt-1))
		delay := base + time.Duration(rand.Int64N(int64(base/2)))
		logger.Warn("completion interrupted; retrying", zap.String("provider", provider.Name()), zap.Int("attempt", attempt), zap.Int("max_attempts", completionAttempts), zap.Duration("retry_delay", delay))
		for _, callback := range notify {
			if callback != nil {
				callback(attempt, delay)
			}
		}
		sleep(delay)
	}
}

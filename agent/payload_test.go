package agent

import (
	"go.uber.org/zap"
	"testing"
)

func TestRequestSizeLimit(t *testing.T) {
	for _, size := range []int{0, MaxRequestBytes - 1, MaxRequestBytes} {
		if err := checkRequestSize(zap.NewNop(), size); err != nil {
			t.Fatal(err)
		}
	}
	err := checkRequestSize(zap.NewNop(), MaxRequestBytes+1)
	if err == nil {
		t.Fatal("oversized request accepted")
	}
	if retryableCompletionError(err) {
		t.Fatal("oversized request must not be retried")
	}
}

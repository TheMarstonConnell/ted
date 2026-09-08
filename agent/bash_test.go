package agent

import (
	"strings"
	"testing"
	"time"
)

func TestBashTimeout(t *testing.T) {
	for _, command := range []string{
		"echo started; sleep 30",
		"echo started; sleep 30 & wait",
		"echo started; sleep 30 & exit 0",
	} {
		t.Run(command, func(t *testing.T) {
			start := time.Now()
			result := runBash(command, 200*time.Millisecond)
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("timeout took %s", elapsed)
			}
			if !strings.Contains(result, "started") || !strings.Contains(result, "timed out after 200ms") {
				t.Fatalf("expected partial output and timeout: %q", result)
			}
		})
	}
}

func TestBashCompletesBeforeTimeout(t *testing.T) {
	if result := runBash("printf hello", 5*time.Second); result != "hello" {
		t.Fatalf("unexpected result: %q", result)
	}
	result := runBash("echo oops >&2; exit 3", 5*time.Second)
	if !strings.Contains(result, "oops") || !strings.Contains(result, "exit status 3") || strings.Contains(result, "timed out") {
		t.Fatalf("unexpected failure result: %q", result)
	}
}

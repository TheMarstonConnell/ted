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

func TestBashOutputLimit(t *testing.T) {
	result := runBash("head -c 200000 /dev/zero | tr '\\0' x; printf stderr-marker >&2; exit 3", 5*time.Second)
	if !strings.HasPrefix(result, strings.Repeat("x", MaxToolOutputBytes)) {
		t.Fatal("did not preserve the output prefix")
	}
	if len(result) > MaxToolOutputBytes+256 || !strings.Contains(result, "Tool output truncated") || !strings.Contains(result, "exit status 3") {
		t.Fatalf("unexpected bounded output: length=%d suffix=%q", len(result), result[MaxToolOutputBytes:])
	}
}

func TestLimitedToolOutputBoundary(t *testing.T) {
	out := &limitedToolOutput{}
	data := strings.Repeat("a", MaxToolOutputBytes)
	if n, err := out.Write([]byte(data)); n != len(data) || err != nil {
		t.Fatal(n, err)
	}
	if out.String() != data {
		t.Fatal("exact limit should not truncate")
	}
	if n, err := out.Write([]byte("extra")); n != 5 || err != nil {
		t.Fatal(n, err)
	}
	if out.buf.Len() != MaxToolOutputBytes || !strings.Contains(out.String(), "discarded 5 bytes") {
		t.Fatal("overflow was not bounded")
	}
}

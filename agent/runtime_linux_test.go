//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTurnCancellationKillsBashProcessGroup(t *testing.T) {
	dir := t.TempDir()
	p := settingsProvider()
	p.complete = func(CompletionRequest) (*Response, error) {
		return toolResponse(bashCall("running", "echo $$ > parent; sleep 30 & echo $! > child; echo partial-output; wait"), bashCall("never", "touch executed")), nil
	}
	a, err := NewAgentIn(nil, []Provider{p}, dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.TurnContext(ctx, "run a long command") }()
	var pids []string
	for _, name := range []string{"parent", "child"} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil && strings.TrimSpace(string(data)) != "" {
				pids = append(pids, strings.TrimSpace(string(data)))
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("bash did not start")
			}
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := awaitTurn(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A killed orphan may remain a zombie until init reaps it. It must not be
	// running when TurnContext returns, nor may the direct shell remain unreaped.
	for i, pid := range pids {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%s/stat", pid))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		_, tail, _ := strings.Cut(string(data), ") ")
		if i == 0 || !strings.HasPrefix(tail, "Z ") {
			t.Fatalf("process %s still running after cancellation: %s", pid, data)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "executed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexecuted tool was run")
	}
	history := a.Messages()
	if err := validateConversation(history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 5 || !strings.Contains(history[3].Content.Text(), "cancelled") || !strings.Contains(history[4].Content.Text(), "cancelled before execution") {
		t.Fatalf("incomplete cancelled tool results: %+v", history)
	}
}

func TestBashContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	capped, full := runBashInContext(ctx, "echo started; sleep 30", time.Minute, "", nil, true)
	if time.Since(start) > time.Second || !strings.Contains(capped, "context deadline exceeded") || !strings.Contains(full, "started") {
		t.Fatalf("%q / %q", capped, full)
	}
}

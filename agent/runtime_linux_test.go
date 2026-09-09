//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
		if procStatProcessGone(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !procStatProcessExited(string(data), i > 0) {
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

// /proc entries are not stable across open and read. A process reaped before
// open yields ENOENT; one reaped after open may yield ESRCH from read. Both mean
// the process is gone, not that cancellation failed. Other I/O errors must fail.
func procStatProcessGone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func TestProcStatProcessGone(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		gone bool
	}{
		{"read succeeded", nil, false},
		{"missing entry", os.ErrNotExist, true},
		{"reaped before open", &os.PathError{Op: "open", Path: "/proc/123/stat", Err: syscall.ENOENT}, true},
		{"reaped after open", &os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.ESRCH}, true},
		{"no such process", syscall.ESRCH, true},
		{"permission denied", &os.PathError{Op: "open", Path: "/proc/123/stat", Err: syscall.EACCES}, false},
		{"read error", &os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.EIO}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := procStatProcessGone(tc.err); got != tc.gone {
				t.Fatalf("procStatProcessGone(%v) = %v, want %v", tc.err, got, tc.gone)
			}
		})
	}
}

// EXIT_DEAD (X) can briefly be observed while procfs races with reaping. A
// zombie is acceptable only for the orphaned child, never for our direct shell.
func procStatProcessExited(stat string, allowZombie bool) bool {
	end := strings.LastIndex(stat, ") ")
	if end < 0 {
		return false
	}
	state := stat[end+2:]
	return strings.HasPrefix(state, "X ") || (allowZombie && strings.HasPrefix(state, "Z "))
}

func TestProcStatProcessExited(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stat        string
		allowZombie bool
		exited      bool
	}{
		{"dead parent", "123 (bash) X 0 0", false, true},
		{"dead child", "123 (sleep) X 0 0", true, true},
		{"zombie orphan", "123 (sleep) Z 1 0", true, true},
		{"unreaped parent", "123 (bash) Z 1 0", false, false},
		{"running child", "123 (sleep) R 1 0", true, false},
		{"sleeping child", "123 (sleep) S 1 0", true, false},
		{"uninterruptible child", "123 (sleep) D 1 0", true, false},
		{"stopped child", "123 (sleep) T 1 0", true, false},
		{"name containing delimiter", "123 (name ) Z more) S 1 0", true, false},
		{"invalid stat", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := procStatProcessExited(tc.stat, tc.allowZombie); got != tc.exited {
				t.Fatalf("procStatProcessExited(%q, %v) = %v, want %v", tc.stat, tc.allowZombie, got, tc.exited)
			}
		})
	}
}

package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultToolTimeout is the maximum runtime of each bash tool call.
const DefaultToolTimeout = 2 * time.Minute

func runBash(command string, timeout time.Duration) string {
	return runBashIn(command, timeout, "", nil)
}

func runBashIn(command string, timeout time.Duration, dir string, environment []string) string {
	result, _ := runBashInContext(context.Background(), command, timeout, dir, environment, false)
	return result
}

// runBashInContext waits for the command and its output readers before returning.
// Full output is retained only for runtime events; provider context stays capped.
func runBashInContext(parent context.Context, command string, timeout time.Duration, dir string, environment []string, retainFull bool) (string, string) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	if dir != "" {
		cmd.Dir = dir
	}
	if environment != nil {
		cmd.Env = environment
	}
	configureToolProcess(cmd)
	// Bound pipe draining even if a descendant escapes the process group and
	// keeps stdout/stderr open after cancellation.
	cmd.WaitDelay = time.Second
	out := &limitedToolOutput{}
	var full bytes.Buffer
	var writer io.Writer = out
	if retainFull {
		writer = io.MultiWriter(out, &full)
	}
	// Using the same writer for both streams makes os/exec serialize writes.
	cmd.Stdout = writer
	cmd.Stderr = writer
	err := cmd.Run()
	result, fullResult := out.String(), strings.ToValidUTF8(full.String(), "?")
	suffix := ""
	if parent.Err() != nil {
		suffix = fmt.Sprintf("\nerror: bash tool call cancelled: %v", parent.Err())
	} else if ctx.Err() == context.DeadlineExceeded {
		suffix = fmt.Sprintf("\nerror: bash tool call timed out after %s", timeout)
	} else if err != nil {
		suffix = fmt.Sprintf("\n%s", err)
	}
	return result + suffix, fullResult + suffix
}

// MaxToolOutputBytes bounds provider-context stdout and stderr together. Continue
// draining after the cap so verbose commands cannot block on a full pipe.
const MaxToolOutputBytes = 64 << 10

type limitedToolOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	discarded int64
}

func (b *limitedToolOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	keep := min(n, MaxToolOutputBytes-b.buf.Len())
	b.buf.Write(p[:keep])
	b.discarded += int64(n - keep)
	return n, nil
}

func (b *limitedToolOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := strings.ToValidUTF8(b.buf.String(), "?")
	if b.discarded > 0 {
		result += fmt.Sprintf("\n[Tool output truncated at %d bytes; discarded %d bytes. Redirect large output to a file and inspect it with head, tail, or grep.]", MaxToolOutputBytes, b.discarded)
	}
	return result
}

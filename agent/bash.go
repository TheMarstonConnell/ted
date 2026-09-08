package agent

import (
	"bytes"
	"context"
	"fmt"
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	result := out.String()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("%s\nerror: bash tool call timed out after %s", result, timeout)
	}
	if err != nil {
		return fmt.Sprintf("%s\n%s", result, err)
	}
	return result
}

// MaxToolOutputBytes bounds retained stdout and stderr together. Continue
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

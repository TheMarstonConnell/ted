package agent

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// DefaultToolTimeout is the maximum runtime of each bash tool call.
const DefaultToolTimeout = 2 * time.Minute

func runBash(command string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	configureToolProcess(cmd)
	// Bound pipe draining even if a descendant escapes the process group and
	// keeps stdout/stderr open after cancellation.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	result := string(out)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("%s\nerror: bash tool call timed out after %s", result, timeout)
	}
	if err != nil {
		return fmt.Sprintf("%s\n%s", result, err)
	}
	return result
}

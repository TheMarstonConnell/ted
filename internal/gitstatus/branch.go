package gitstatus

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

func Branch(directory string) string {
	if directory == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = directory
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if branch := run("symbolic-ref", "--quiet", "--short", "HEAD"); branch != "" {
		return branch
	}
	if commit := run("rev-parse", "--short", "HEAD"); commit != "" {
		return "detached " + commit
	}
	return ""
}

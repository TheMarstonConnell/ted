//go:build !unix

package agent

import "os/exec"

// CommandContext kills the direct process on platforms without Unix groups.
func configureToolProcess(cmd *exec.Cmd) {}

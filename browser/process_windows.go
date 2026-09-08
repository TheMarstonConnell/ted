//go:build windows

package browser

import "os/exec"

func setDetached(cmd *exec.Cmd) {}

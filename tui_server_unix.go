//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func detachOwnedProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

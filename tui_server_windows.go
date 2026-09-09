//go:build windows

package main

import "os/exec"

func detachOwnedProcess(cmd *exec.Cmd) {}

//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Invoked in isolated subprocesses by a tiny executable wrapper. The server
// branch uses the same stdin-EOF cancellation watcher as `ted serve --owned`.
func TestOwnedServerHelperProcess(t *testing.T) {
	role := os.Getenv("TED_TEST_HELPER_ROLE")
	if role == "" {
		return
	}
	dir := os.Getenv("TED_TEST_HELPER_DIR")
	if role == "owner" {
		_ = os.Setenv("TED_TEST_HELPER_ROLE", "server")
		owner, err := startOwnedServer(os.Getenv("TED_TEST_HELPER_EXEC"), dir, filepath.Join(dir, "server.log"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_ = owner // intentionally leaked: abrupt exit must still close the OS pipe
		os.Exit(0)
	}
	if !strings.Contains(strings.Join(os.Args, " "), "serve --owned --data-dir") {
		os.Exit(3)
	}
	fmt.Println("server output redirected")
	ctx, cancel := context.WithCancel(context.Background())
	go cancelOnEOF(os.Stdin, cancel)
	_ = os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600)
	<-ctx.Done()
	_ = os.WriteFile(filepath.Join(dir, "closed"), []byte("EOF"), 0600)
	os.Exit(0)
}

func ownedTestExecutable(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "ted-helper")
	script := "#!/bin/sh\nexec \"$TED_TEST_HELPER_BINARY\" -test.run='^TestOwnedServerHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TED_TEST_HELPER_BINARY", binary)
	t.Setenv("TED_TEST_HELPER_DIR", dir)
	t.Setenv("TED_TEST_HELPER_EXEC", wrapper)
	t.Setenv("TED_TEST_HELPER_ROLE", "server")
	return wrapper, dir
}
func waitOwnedFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("owner pipe lifecycle timed out: %s", path)
}
func TestOwnedServerCloseAndOutput(t *testing.T) {
	executable, dir := ownedTestExecutable(t)
	log := filepath.Join(dir, "server.log")
	owner, err := startOwnedServer(executable, dir, log)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	waitOwnedFile(t, filepath.Join(dir, "ready"))
	select {
	case <-owner.done:
		t.Fatal("server quit while owner still held pipe")
	default:
	}
	owner.Close()
	owner.Close() // cleanup is idempotent
	waitOwnedFile(t, filepath.Join(dir, "closed"))
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), "server output redirected") {
		t.Fatal(string(data), err)
	}
}
func TestOwnedServerExitsWhenOwnerCrashes(t *testing.T) {
	executable, dir := ownedTestExecutable(t)
	t.Setenv("TED_TEST_HELPER_ROLE", "owner")
	cmd := exec.Command(executable)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	waitOwnedFile(t, filepath.Join(dir, "closed"))
}

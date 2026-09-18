package browser

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type displayChild struct {
	PID  int
	Args []string
}

func TestHeadedDisplayChild(t *testing.T) {
	mode := os.Getenv("TED_DISPLAY_TEST_CHILD")
	if mode == "" {
		return
	}
	if mode == "ignore-term" {
		signal.Ignore(syscall.SIGTERM)
	}
	data, _ := json.Marshal(displayChild{os.Getpid(), os.Args})
	if err := os.WriteFile(os.Getenv("TED_DISPLAY_TEST_PID"), data, 0600); err != nil {
		os.Exit(20)
	}
	pipe := os.NewFile(3, "displayfd")
	switch mode {
	case "ready":
		_, _ = fmt.Fprintln(pipe, "123")
	case "exit":
		os.Exit(23)
	case "malformed":
		_, _ = fmt.Fprintln(pipe, "not-a-display-secret")
	case "closed-pipe":
		_ = pipe.Close()
	}
	for {
		time.Sleep(time.Hour)
	}
}

func fakeDisplay(t *testing.T, mode string) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := "#!/bin/sh\nexec '" + strings.ReplaceAll(bin, "'", "'\\''") + "' -test.run=^TestHeadedDisplayChild$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "Xvfb"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	pid := filepath.Join(dir, "child.json")
	t.Setenv("PATH", dir)
	t.Setenv("DISPLAY", "")
	t.Setenv("TED_DISPLAY_TEST_CHILD", mode)
	t.Setenv("TED_DISPLAY_TEST_PID", pid)
	return pid
}

func readDisplayChild(t *testing.T, path string) displayChild {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var child displayChild
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &child) == nil {
			return child
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fake Xvfb did not start")
	return displayChild{}
}

func assertDisplayReaped(t *testing.T, child displayChild) {
	t.Helper()
	if err := syscall.Kill(child.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child still exists: %v", err)
	}
	if _, err := syscall.Wait4(child.PID, nil, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("child was not reaped: %v", err)
	}
}

func assertDisplayDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("display files remain: %v, %v", entries, err)
	}
}

func displayEnvValue(env []string, key string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, key+"="); ok {
			return value
		}
	}
	return ""
}

func readDisplayCookie(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)
	var family uint16
	if err := binary.Read(r, binary.BigEndian, &family); err != nil || family != 65535 {
		t.Fatalf("authority is not FamilyWild: %d, %v", family, err)
	}
	fields := make([][]byte, 4)
	for i := range fields {
		var size uint16
		if err := binary.Read(r, binary.BigEndian, &size); err != nil {
			t.Fatal(err)
		}
		fields[i] = make([]byte, size)
		if _, err := io.ReadFull(r, fields[i]); err != nil {
			t.Fatal(err)
		}
	}
	if len(fields[0]) != 0 || len(fields[1]) != 0 || string(fields[2]) != "MIT-MAGIC-COOKIE-1" || len(fields[3]) != 16 || r.Len() != 0 {
		t.Fatal("authority record is not a complete wildcard MIT cookie record")
	}
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {filepath.Dir(path), 0700}} {
		info, err := os.Stat(check.path)
		if err != nil || info.Mode().Perm() != check.mode {
			t.Fatalf("insecure authority permissions for %s: %v, %v", check.path, info, err)
		}
	}
	return fields[3]
}

func TestHeadedDisplayInherited(t *testing.T) {
	t.Setenv("DISPLAY", ":inherited")
	t.Setenv("XAUTHORITY", "/inherited/auth")
	t.Setenv("PATH", t.TempDir())
	env, cleanup, err := startHeadedDisplay(context.Background(), "/does/not/exist")
	if err != nil || env != nil {
		t.Fatalf("inherited display replaced: %v, %v", env, err)
	}
	cleanup()
	cleanup()
	if os.Getenv("DISPLAY") != ":inherited" || os.Getenv("XAUTHORITY") != "/inherited/auth" {
		t.Fatal("inherited environment changed")
	}
}

func TestHeadedDisplayMissingXvfb(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("PATH", t.TempDir())
	_, _, err := startHeadedDisplay(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "install Xvfb") || !strings.Contains(err.Error(), "DISPLAY") {
		t.Fatalf("missing actionable prerequisite error: %v", err)
	}
}

func TestHeadedDisplayAlreadyCanceled(t *testing.T) {
	pidPath := fakeDisplay(t, "ready")
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := startHeadedDisplay(ctx, parent)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled startup launched a child: %v", err)
	}
	assertDisplayDirEmpty(t, parent)
}

func TestHeadedDisplayExecFailure(t *testing.T) {
	t.Setenv("DISPLAY", "")
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(bin, "Xvfb"), []byte("#!/nonexistent/interpreter\n"), 0700); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if _, _, err := startHeadedDisplay(context.Background(), parent); err == nil {
		t.Fatal("broken executable did not fail startup")
	}
	assertDisplayDirEmpty(t, parent)
}

func TestHeadedDisplayLifecycle(t *testing.T) {
	pidPath := fakeDisplay(t, "ready")
	parent := t.TempDir()
	sibling := filepath.Join(parent, "unrelated")
	if err := os.WriteFile(sibling, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env, cleanup, err := startHeadedDisplay(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	child := readDisplayChild(t, pidPath)
	auth := displayEnvValue(env, "XAUTHORITY")
	cookie := readDisplayCookie(t, auth)
	if displayEnvValue(env, "DISPLAY") != ":123" {
		t.Fatalf("displayfd result not used: %v", env)
	}
	args := strings.Join(child.Args, " ")
	for _, want := range []string{"-displayfd 3", "-auth " + auth, "-nolisten tcp", "-screen 0 1920x1080x24", "-noreset"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing server option %q", want)
		}
	}
	if strings.Contains(args, "-ac") || bytes.Contains([]byte(args), cookie) {
		t.Fatal("insecure server arguments")
	}
	if os.Getenv("DISPLAY") != "" || os.Getenv("XAUTHORITY") == auth {
		t.Fatal("global environment modified")
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	if err := syscall.Kill(child.PID, 0); err != nil {
		t.Fatalf("display died with request: %v", err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(cleanup)
	}
	wg.Wait()
	assertDisplayReaped(t, child)
	if _, err := os.Stat(filepath.Dir(auth)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private directory remains: %v", err)
	}
	if data, err := os.ReadFile(sibling); err != nil || string(data) != "keep" {
		t.Fatalf("cleanup touched unrelated file: %v", err)
	}
}

func TestHeadedDisplayAuthorityUnique(t *testing.T) {
	fakeDisplay(t, "ready")
	parent := t.TempDir()
	first, cleanFirst, err := startHeadedDisplay(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanFirst()
	second, cleanSecond, err := startHeadedDisplay(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanSecond()
	a, b := displayEnvValue(first, "XAUTHORITY"), displayEnvValue(second, "XAUTHORITY")
	if a == b || bytes.Equal(readDisplayCookie(t, a), readDisplayCookie(t, b)) {
		t.Fatal("managed displays reused authority credentials")
	}
}

func TestHeadedDisplayStartupFailure(t *testing.T) {
	for _, mode := range []string{"exit", "malformed", "closed-pipe"} {
		t.Run(mode, func(t *testing.T) {
			pidPath := fakeDisplay(t, mode)
			parent := t.TempDir()
			env, _, err := startHeadedDisplay(context.Background(), parent)
			if err == nil || env != nil || strings.Contains(err.Error(), "not-a-display-secret") {
				t.Fatalf("unexpected startup result: %v, %v", env, err)
			}
			assertDisplayReaped(t, readDisplayChild(t, pidPath))
			assertDisplayDirEmpty(t, parent)
		})
	}
}

func TestHeadedDisplayStartupCancellation(t *testing.T) {
	for _, mode := range []string{"hang", "ignore-term"} {
		t.Run(mode, func(t *testing.T) {
			pidPath := fakeDisplay(t, mode)
			parent := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, cleanup, err := startHeadedDisplay(ctx, parent)
				cleanup()
				done <- err
			}()
			child := readDisplayChild(t, pidPath)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("startup cancellation blocked")
			}
			assertDisplayReaped(t, child)
			assertDisplayDirEmpty(t, parent)
		})
	}
}

func TestHeadedDisplayStartupDeadline(t *testing.T) {
	pidPath := fakeDisplay(t, "hang")
	parent := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, _, err := startHeadedDisplay(ctx, parent)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup deadline lost: %v", err)
	}
	assertDisplayReaped(t, readDisplayChild(t, pidPath))
	assertDisplayDirEmpty(t, parent)
}

func TestHeadedDisplayXvfbIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 with Xvfb and xdpyinfo installed")
	}
	info, err := exec.LookPath("xdpyinfo")
	if err != nil {
		t.Fatal("real display validation requires xdpyinfo")
	}
	t.Setenv("DISPLAY", "")
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env, cleanup, err := startHeadedDisplay(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cancel()
	readDisplayCookie(t, displayEnvValue(env, "XAUTHORITY"))
	query := func(auth string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, info)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "DISPLAY=") && !strings.HasPrefix(entry, "XAUTHORITY=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "DISPLAY="+displayEnvValue(env, "DISPLAY"), "XAUTHORITY="+auth)
		return cmd.CombinedOutput()
	}
	for range 2 {
		output, err := query(displayEnvValue(env, "XAUTHORITY"))
		if err != nil || !strings.Contains(string(output), "dimensions:    1920x1080 pixels") || !strings.Contains(string(output), "depth of root window:    24 planes") {
			t.Fatalf("authorized display query failed: %v\n%s", err, output)
		}
	}
	if output, err := query(filepath.Join(parent, "missing-authority")); err == nil {
		t.Fatalf("unauthorized X access was allowed: %s", output)
	}
	wrongAuth := filepath.Join(parent, "wrong-authority")
	if err := writeDisplayAuthority(wrongAuth); err != nil {
		t.Fatal(err)
	}
	if output, err := query(wrongAuth); err == nil {
		t.Fatalf("wrong-cookie X access was allowed: %s", output)
	}
	if output, err := query(displayEnvValue(env, "XAUTHORITY")); err != nil {
		t.Fatalf("server unavailable after unauthorized probes: %v\n%s", err, output)
	}
	_ = os.Remove(wrongAuth)
	display, err := strconv.Atoi(strings.TrimPrefix(displayEnvValue(env, "DISPLAY"), ":"))
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	assertDisplayDirEmpty(t, parent)
	if _, err := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", display)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned X socket remains after cleanup: %v", err)
	}
}

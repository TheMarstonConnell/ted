//go:build !windows

package browser

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func startTestServer(t *testing.T) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx) }()
	path, err := socketPath()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := dialDaemon(context.Background(), path)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server did not listen: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cancel, done
}

func TestServeStatusDoesNotLaunchChrome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ted")
	t.Setenv("TED_HOME", home)
	cancel, done := startTestServer(t)

	resp, err := Call(context.Background(), Request{Action: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Error != nil {
		t.Fatalf("response = %#v", resp)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type = %T", resp.Data)
	}
	if data["daemon"] != true {
		t.Fatalf("status data = %#v", data)
	}
	if _, err := os.Stat(filepath.Join(home, "browser", "projects")); !os.IsNotExist(err) {
		t.Fatalf("status created project state: %v", err)
	}

	resp, err = Call(context.Background(), Request{Project: t.TempDir(), Thread: "../escape", Action: "session-close"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != "invalid_thread" {
		t.Fatalf("invalid thread response = %#v", resp)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeEnforcesSingleDaemonAndPrivateSocket(t *testing.T) {
	t.Setenv("TED_HOME", filepath.Join(t.TempDir(), "ted"))
	cancel, done := startTestServer(t)
	path, _ := socketPath()
	if mode := mustStat(t, path).Mode().Perm(); mode != 0o600 {
		t.Fatalf("socket mode = %o", mode)
	}

	ctx, cancelSecond := context.WithTimeout(context.Background(), time.Second)
	defer cancelSecond()
	if err := Serve(ctx); err == nil {
		t.Fatal("second Serve unexpectedly succeeded")
	}
	cancel()
	<-done
}

func TestErrorJSONUsesStableLowercaseFields(t *testing.T) {
	b, err := json.Marshal(Response{OK: false, Error: &Error{Code: "invalid_params", Message: "bad"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ok":false,"error":{"code":"invalid_params","message":"bad"}}`
	if string(b) != want {
		t.Fatalf("JSON = %s, want %s", b, want)
	}
}

func TestManifestOnlyRegistersScreenshotRecord(t *testing.T) {
	dir := t.TempDir()
	s := &session{dir: dir}
	path := filepath.Join(dir, "artifacts", "shot.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.registerScreenshot(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "artifacts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]string
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	if record["type"] != "screenshot" || record["path"] != path {
		t.Fatalf("manifest record = %#v", record)
	}
	if mode := mustStat(t, filepath.Join(dir, "artifacts.jsonl")).Mode().Perm(); mode != 0o600 {
		t.Fatalf("manifest mode = %o", mode)
	}
}

func TestCloseSessionIfRunningDoesNotCreateOrStart(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("TED_HOME", home)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := CloseSessionIfRunning(ctx, t.TempDir(), "thread-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("cleanup API created TED_HOME: %v", err)
	}
}

func TestCloseSessionIfRunningUsesExistingDaemon(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ted")
	t.Setenv("TED_HOME", home)
	cancel, done := startTestServer(t)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err := CloseSessionIfRunning(ctx, t.TempDir(), "thread-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "browser", "projects")); !os.IsNotExist(err) {
		t.Fatalf("close of absent session launched Chrome/project: %v", err)
	}
	cancel()
	<-done
}

func TestServeShutdownUnblocksIdleConnection(t *testing.T) {
	t.Setenv("TED_HOME", filepath.Join(t.TempDir(), "ted"))
	cancel, done := startTestServer(t)
	path, _ := socketPath()
	conn, err := dialDaemon(context.Background(), path)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle connection blocked shutdown")
	}
}

func TestClientCancellationUnblocksRead(t *testing.T) {
	t.Setenv("TED_HOME", filepath.Join(t.TempDir(), "ted"))
	path, err := socketPath()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := Call(ctx, Request{Action: "status", Timeout: time.Minute}); done <- err }()
	conn := <-accepted
	defer conn.Close()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not unblock client read")
	}
}

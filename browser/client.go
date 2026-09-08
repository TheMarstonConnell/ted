package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var executablePath = os.Executable

// Call sends req to the local browser daemon, starting it with the current
// executable's "browser serve" command when necessary.
func Call(ctx context.Context, req Request) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := socketPath()
	if err != nil {
		return Response{}, err
	}
	conn, err := dialDaemon(ctx, path)
	if err != nil {
		if err := startDaemon(path); err != nil {
			return Response{}, err
		}
		conn, err = waitForDaemon(ctx, path)
		if err != nil {
			return Response{}, fmt.Errorf("connect to browser daemon: %w", err)
		}
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	deadline := time.Now().Add(defaultTimeout + 5*time.Second)
	if req.Timeout > 0 {
		deadline = time.Now().Add(req.Timeout + 5*time.Second)
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("send browser request: %w", err)
	}
	var resp Response
	dec := json.NewDecoder(bufio.NewReader(conn))
	if err := dec.Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("read browser response: %w", err)
	}
	return resp, nil
}

func dialDaemon(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

func waitForDaemon(ctx context.Context, path string) (net.Conn, error) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		conn, err := dialDaemon(ctx, path)
		if err == nil {
			return conn, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, last
		case <-ticker.C:
		}
	}
}

func startDaemon(socket string) error {
	exe, err := executablePath()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	h, err := Home()
	if err != nil {
		return err
	}
	logPath := filepath.Join(h, "browser", "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open browser daemon log: %w", err)
	}
	_ = os.Chmod(logPath, 0o600)
	cmd := exec.Command(exe, "browser", "serve")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start browser daemon: %w", err)
	}
	logFile.Close()
	_ = cmd.Process.Release()
	return nil
}

// CloseSessionIfRunning asks an already-running daemon to close all tabs owned
// by thread. It never starts the daemon and never creates TED_HOME; a missing
// or refused daemon socket is treated as success. This makes it safe to call
// unconditionally from host/agent cleanup paths.
func CloseSessionIfRunning(ctx context.Context, project, thread string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateThread(thread); err != nil {
		return err
	}
	path, err := existingSocketPath()
	if err != nil {
		return err
	}
	conn, err := dialDaemon(ctx, path)
	if err != nil {
		if isDaemonAbsent(err) {
			return nil
		}
		return fmt.Errorf("connect to browser daemon: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	req := Request{Project: project, Thread: thread, Action: "session-close", Timeout: 10 * time.Second}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send browser close request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return fmt.Errorf("read browser close response: %w", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			return resp.Error
		}
		return fmt.Errorf("browser session close failed")
	}
	return nil
}

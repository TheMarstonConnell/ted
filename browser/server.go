package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

const maxRequestBytes = 8 << 20

// Serve runs the single per-user browser daemon until ctx is cancelled.
func Serve(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := socketPath()
	if err != nil {
		return err
	}
	lockPath := filepath.Join(filepath.Dir(path), "daemon.lock")
	lock, err := acquireDaemonLock(lockPath)
	if err != nil {
		return err
	}
	defer releaseDaemonLock(lock)

	// We hold the exclusive lock, so an existing pathname is stale.
	if err := os.Remove(path); err != nil && !isNotExist(err) {
		return fmt.Errorf("remove stale browser socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen on browser socket: %w", err)
	}
	defer func() {
		ln.Close()
		os.Remove(path)
	}()
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("make browser socket private: %w", err)
	}

	home, err := Home()
	if err != nil {
		return err
	}
	mgr := newManager(ctx, home)
	defer mgr.close()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				time.Sleep(25 * time.Millisecond)
				continue
			}
			return fmt.Errorf("accept browser request: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			handleConnection(ctx, mgr, conn)
		}()
	}
}

func handleConnection(serverCtx context.Context, mgr *manager, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(serverCtx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Minute))
	dec := json.NewDecoder(bufio.NewReader(io.LimitReader(conn, maxRequestBytes)))
	dec.UseNumber()
	var req Request
	if err := dec.Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(errorResponse(fail("invalid_request", "decode request: %v", err)))
		return
	}
	// A connection carries one request. If the CLI exits or cancels while an
	// operation is waiting, cancel that operation, not its durable browser tab.
	requestCtx, cancel := context.WithCancel(serverCtx)
	defer cancel()
	go func() {
		var extra [1]byte
		_, _ = conn.Read(extra[:])
		cancel()
	}()
	resp := dispatchSafely(requestCtx, mgr, req)
	_ = json.NewEncoder(conn).Encode(resp)
}

func dispatchSafely(ctx context.Context, mgr *manager, req Request) (resp Response) {
	defer func() {
		if v := recover(); v != nil {
			resp = errorResponse(fail("internal", "browser service panic: %v", v))
			_, _ = os.Stderr.WriteString("browser service panic: " + fmt.Sprint(v) + "\n" + string(debug.Stack()))
		}
	}()
	data, err := mgr.dispatch(ctx, req)
	if err != nil {
		return errorResponse(err)
	}
	return Response{OK: true, Data: data}
}

func normalizeAction(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

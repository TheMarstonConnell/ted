package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/TheMarstonConnell/ted/controlplane"
	"github.com/TheMarstonConnell/ted/web"
	"github.com/spf13/cobra"
)

const defaultServerAddress = "0.0.0.0:8281"

func defaultDataDir() (string, error) {
	home := os.Getenv("TED_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(userHome, ".ted")
	}
	if home == "~" || strings.HasPrefix(home, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(userHome, strings.TrimPrefix(home, "~/"))
		if os.Getenv("TED_HOME") == "~" {
			home = userHome
		}
	}
	return filepath.Join(home, "controlplane"), nil
}

func newServeCommand() *cobra.Command {
	var addr, dir string
	var owned bool
	cmd := &cobra.Command{Use: "serve", Short: "Run the agent API server", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&addr, "addr", defaultServerAddress, "HTTP listen address")
	defaultDir, _ := defaultDataDir()
	cmd.Flags().StringVar(&dir, "data-dir", defaultDir, "Persistent control plane data directory")
	cmd.Flags().BoolVar(&owned, "owned", false, "Shut down when the owning client's stdin pipe closes")
	_ = cmd.Flags().MarkHidden("owned")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if dir == "" {
			var err error
			dir, err = defaultDataDir()
			if err != nil {
				return err
			}
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if owned {
			go cancelOnEOF(os.Stdin, cancel)
		}
		return runServe(ctx, addr, dir)
	}
	return cmd
}

func runServe(ctx context.Context, addr, dir string) error {
	// Bind first: a competing startup must fail without opening the same store.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer listener.Close()
	logger, err := newLogger()
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()
	providers, err := configuredProviders(logger)
	if err != nil {
		return err
	}
	service, err := controlplane.NewService(dir, logger, providers)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Ted control plane listening on %s (API version 1). WARNING: no authentication; use a trusted network or protected proxy.\n", listener.Addr())
	return serveControlPlane(ctx, listener, service)
}

func serveControlPlane(ctx context.Context, listener net.Listener, service *controlplane.Service) error {
	var err error
	// net/http does not close hijacked (WebSocket) connections in Close.
	// Keep all accepted sockets so owner shutdown intentionally disconnects every client.
	tracked := &trackedListener{Listener: listener}
	server := &http.Server{Handler: web.Handler(controlplane.NewHandler(service)), ReadHeaderTimeout: 10 * time.Second}

	done := make(chan error, 1)
	go func() { done <- server.Serve(tracked) }()
	select {
	case <-ctx.Done():
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	// Cancel all agent work, including clients other than the owner. HTTP Close
	// deliberately terminates ordinary connections; the service wakes WS peers.
	_ = service.BeginShutdown()
	_ = server.Close()
	tracked.connections.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	closeErr := service.Close(closeCtx)
	if err != nil {
		return err
	}
	return closeErr
}

func cancelOnEOF(reader io.Reader, cancel context.CancelFunc) {
	_, _ = io.Copy(io.Discard, reader)
	cancel()
}

// Wrapping accepted sockets also unregisters hijacked sockets when WebSocket
// clients disconnect normally; ConnState alone never reports those as closed.
type trackedListener struct {
	net.Listener
	connections sync.Map
}
type trackedConn struct {
	net.Conn
	owner *trackedListener
}

func (l *trackedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, owner: l}
	l.connections.Store(tracked, struct{}{})
	return tracked, nil
}
func (c *trackedConn) Close() error {
	c.owner.connections.Delete(c)
	return c.Conn.Close()
}

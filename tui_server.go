package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultServerURL = "http://localhost:8281"

// ownedServer holds the sole write end of the child's stdin pipe. The child
// sees EOF even if this process is killed (including SIGKILL), unlike a deferred
// signal alone. Pass an *os.File, not an io.Pipe, to avoid exec's copying goroutine.
type ownedServer struct {
	cmd  *exec.Cmd
	pipe *os.File
	done chan struct{}
	err  error // published by closing done
	once sync.Once
}

func (s *ownedServer) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		_ = s.pipe.Close()
		select {
		case <-s.done:
		case <-time.After(12 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.done
		}
	})
}

func checkServer(ctx context.Context, base string) error {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid server URL %q: expected http(s)://host[:port]", base)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/health", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("server health: HTTP %d", res.StatusCode)
	}
	var health struct {
		APIVersion json.RawMessage `json:"api_version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		return fmt.Errorf("server health: %w", err)
	}
	if string(health.APIVersion) != `"1"` && string(health.APIVersion) != "1" {
		return fmt.Errorf("incompatible server API version %s (expected 1)", health.APIVersion)
	}
	return nil
}

// resolveTUIServer never starts a local server for an explicit URL.
func resolveTUIServer(ctx context.Context, explicit string) (string, *ownedServer, error) {
	if explicit != "" {
		if err := checkServer(ctx, explicit); err != nil {
			return "", nil, fmt.Errorf("connect to --server %s: %w", explicit, err)
		}
		return strings.TrimRight(explicit, "/"), nil, nil
	}
	if err := checkServer(ctx, defaultServerURL); err == nil {
		return defaultServerURL, nil, nil
	} else if !serverUnavailable(err) {
		return "", nil, fmt.Errorf("default server %s is occupied but incompatible or unhealthy: %w", defaultServerURL, err)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	dir, err := defaultDataDir()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, err
	}
	fmt.Fprintln(os.Stderr, "Starting Ted API on 0.0.0.0:8281 without authentication. Reachable clients can execute tools as your user. This TUI owns the server lifetime.")
	logPath := filepath.Join(dir, "server.log")
	server, err := startOwnedServer(executable, dir, logPath)
	if err != nil {
		return "", nil, err
	}
	deadline, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-server.done:
			server.Close()
			// Another TUI may have won the bind. We do not own that server.
			if err := checkServer(deadline, defaultServerURL); err == nil {
				return defaultServerURL, nil, nil
			}
			return "", nil, fmt.Errorf("background server exited (%v); see %s", server.err, logPath)
		case <-deadline.Done():
			server.Close()
			return "", nil, fmt.Errorf("waiting for background server: %w; see %s", deadline.Err(), logPath)
		case <-ticker.C:
			if err := checkServer(deadline, defaultServerURL); err == nil {
				// Detect a bind loser before claiming ownership of the winner.
				select {
				case <-server.done:
					server.Close()
					return defaultServerURL, nil, nil
				default:
					return defaultServerURL, server, nil
				}
			}
		}
	}
}

func startOwnedServer(executable, dir, logPath string) (*ownedServer, error) {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer read.Close()
	cmd := exec.Command(executable, "serve", "--owned", "--data-dir", dir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = read, log, log
	cmd.Env = append(os.Environ(), LOG_FILE_VARIABLE+"="+logPath)
	detachOwnedProcess(cmd)
	if err := cmd.Start(); err != nil {
		write.Close()
		return nil, err
	}
	owned := &ownedServer{cmd: cmd, pipe: write, done: make(chan struct{})}
	go func() { owned.err = cmd.Wait(); close(owned.done) }()
	return owned, nil
}

// Only a failed dial means no server is listening. HTTP errors, a wrong API
// version, and a hung health handler are not permission to start another daemon.
func serverUnavailable(err error) bool {
	var network *net.OpError
	return errors.As(err, &network) && network.Op == "dial"
}

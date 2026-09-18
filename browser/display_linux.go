package browser

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func startHeadedDisplay(request context.Context, parentDir string) ([]string, func(), error) {
	noop := func() {}
	if os.Getenv("DISPLAY") != "" {
		return nil, noop, nil
	}
	startup, cancel := context.WithTimeout(request, 10*time.Second)
	defer cancel()
	fail := func(err error) ([]string, func(), error) {
		return nil, noop, fmt.Errorf("start private headed display: %w; install Xvfb and its runtime dependencies or set DISPLAY to an accessible X server", err)
	}
	if err := startup.Err(); err != nil {
		return fail(err)
	}
	xvfb, err := exec.LookPath("Xvfb")
	if err != nil {
		return fail(err)
	}
	dir, err := os.MkdirTemp(parentDir, "headed-display-")
	if err != nil {
		return fail(err)
	}
	owned := true
	defer func() {
		if owned {
			_ = os.RemoveAll(dir)
		}
	}()
	authority := filepath.Join(dir, "Xauthority")
	if err := writeDisplayAuthority(authority); err != nil {
		return fail(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	defer reader.Close()
	defer writer.Close()
	// Only startup observes the request; the display belongs to the browser.
	cmd := exec.Command(xvfb, "-displayfd", "3", "-auth", authority, "-nolisten", "tcp", "-screen", "0", "1920x1080x24", "-noreset")
	cmd.ExtraFiles = []*os.File{writer}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	_ = writer.Close()
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = reader.Close()
			select {
			case <-done:
			default:
				_ = cmd.Process.Signal(syscall.SIGTERM)
				timer := time.NewTimer(2 * time.Second)
				select {
				case <-done:
				case <-timer.C:
					_ = cmd.Process.Kill()
					<-done
				}
				timer.Stop()
			}
			_ = os.RemoveAll(dir)
		})
	}
	owned = false
	succeeded := false
	defer func() {
		if !succeeded {
			cleanup()
		}
	}()
	type readyResult struct {
		display string
		err     error
	}
	ready := make(chan readyResult, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(reader, 32)).ReadString('\n')
		ready <- readyResult{strings.TrimSuffix(line, "\n"), err}
	}()
	exited := func() error {
		if waitErr != nil {
			return fmt.Errorf("Xvfb exited before becoming ready: %w", waitErr)
		}
		return fmt.Errorf("Xvfb exited before becoming ready")
	}
	select {
	case <-startup.Done():
		return fail(startup.Err())
	case <-done:
		return fail(exited())
	case result := <-ready:
		if result.err != nil {
			return fail(fmt.Errorf("Xvfb did not report a display number: %w", result.err))
		}
		number, err := strconv.ParseUint(result.display, 10, 16)
		if err != nil || result.display != strconv.FormatUint(number, 10) {
			return fail(fmt.Errorf("Xvfb reported an invalid display number"))
		}
		if err := startup.Err(); err != nil {
			return fail(err)
		}
		select {
		case <-done:
			return fail(exited())
		default:
		}
		succeeded = true
		return []string{"DISPLAY=:" + result.display, "XAUTHORITY=" + authority}, cleanup, nil
	}
}

func writeDisplayAuthority(path string) error {
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		return err
	}
	// FamilyWild and an empty display number allow -displayfd allocation.
	data := binary.BigEndian.AppendUint16(nil, 65535)
	for _, field := range [][]byte{nil, nil, []byte("MIT-MAGIC-COOKIE-1"), cookie} {
		data = binary.BigEndian.AppendUint16(data, uint16(len(field)))
		data = append(data, field...)
	}
	return os.WriteFile(path, data, 0600)
}

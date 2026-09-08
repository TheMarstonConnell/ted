package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// encoderOutput bounds diagnostics from a long-running encoder. Read it only
// after cmd.Wait has completed, when os/exec has finished copying stderr.
type encoderOutput struct{ buffer bytes.Buffer }

func (b *encoderOutput) Len() int       { return b.buffer.Len() }
func (b *encoderOutput) String() string { return b.buffer.String() }

func (b *encoderOutput) Write(p []byte) (int, error) {
	const limit = 8192
	n := len(p)
	if remaining := limit - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return n, nil
}

type recording struct {
	tab    *browserTab
	path   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan error
	stop   chan struct{}
	fps    float64
	stderr *encoderOutput

	mu            sync.Mutex
	latestFrame   string
	framesWritten int
}

func (t *browserTab) handleRecordingEvent(ev any) {
	frame, ok := ev.(*page.EventScreencastFrame)
	if !ok {
		return
	}
	// Acknowledgement must not run synchronously in a chromedp listener.
	go func(id int64) {
		ackCtx, cancel := context.WithTimeout(t.ctx, 2*time.Second)
		defer cancel()
		_ = chromedp.Run(ackCtx, page.ScreencastFrameAck(id))
	}(frame.SessionID)

	t.recMu.Lock()
	defer t.recMu.Unlock()
	if t.rec == nil {
		return
	}
	// Screencast frames are event-driven and may stop while the page is idle.
	// Keep only the newest frame; encodeFrames samples and repeats it at the
	// requested fixed rate so video time tracks wall-clock time.
	t.rec.mu.Lock()
	t.rec.latestFrame = frame.Data
	t.rec.mu.Unlock()
}

func (s *session) startRecordingLocked(ctx context.Context, params map[string]any) (any, error) {
	if s.recording != nil {
		return nil, fail("conflict", "recording is already active")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fail("unsupported", "ffmpeg is not installed; recording is unavailable")
	}
	t, err := s.selectedTab()
	if err != nil {
		return nil, err
	}
	fps, err := numberParam(params, "fps", 10)
	if err != nil {
		return nil, err
	}
	if fps < 1 || fps > 60 {
		return nil, fail("invalid_params", "fps must be between 1 and 60")
	}
	artifactDir := filepath.Join(s.dir, "artifacts")
	if err := mkdirPrivate(artifactDir); err != nil {
		return nil, fail("internal", "create recording directory: %v", err)
	}
	path := filepath.Join(artifactDir, fmt.Sprintf("recording-%d.mp4", time.Now().UnixNano()))
	// Pre-create privately; ffmpeg truncates this inode rather than creating a
	// process-umask-dependent world-readable file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fail("internal", "create recording: %v", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, fail("internal", "create recording: %v", err)
	}

	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "image2pipe", "-framerate", strconv.FormatFloat(fps, 'f', -1, 64),
		"-vcodec", "mjpeg", "-i", "pipe:0", "-an",
		"-c:v", "libx264", "-preset", "veryfast", "-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart", path,
	)
	cmd.Stdout = io.Discard
	var stderr encoderOutput
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.Remove(path)
		return nil, fail("internal", "prepare ffmpeg: %v", err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		os.Remove(path)
		return nil, fail("unsupported", "start ffmpeg: %v", err)
	}
	r := &recording{tab: t, path: path, cmd: cmd, stdin: stdin, done: make(chan error, 1), stop: make(chan struct{}), fps: fps, stderr: &stderr}
	t.recMu.Lock()
	t.rec = r
	t.recMu.Unlock()
	s.recording = r
	go r.encodeFrames()

	// Background tabs may acknowledge StartScreencast without producing frames.
	// Activate this tab before requesting the stream.
	start := page.StartScreencast().WithFormat(page.ScreencastFormatJpeg).WithQuality(80).WithEveryNthFrame(1)
	if err := s.runTab(ctx, t, page.BringToFront(), start); err != nil {
		s.detachRecording(r)
		close(r.stop)
		_ = r.stdin.Close()
		_ = r.cmd.Process.Kill()
		<-r.done
		os.Remove(path)
		return nil, err
	}
	return map[string]any{"recording": true, "path": path, "fps": fps, "silent": true}, nil
}

func (r *recording) encodeFrames() {
	interval := time.Duration(float64(time.Second) / r.fps)
	if interval <= 0 {
		interval = time.Second / 10
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var firstErr error
	writeLatest := func() {
		r.mu.Lock()
		encoded := r.latestFrame
		r.mu.Unlock()
		if encoded == "" || firstErr != nil {
			return
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			firstErr = err
			return
		}
		if _, err := r.stdin.Write(data); err != nil {
			firstErr = err
			return
		}
		r.mu.Lock()
		r.framesWritten++
		r.mu.Unlock()
	}
	for {
		select {
		case <-ticker.C:
			writeLatest()
		case <-r.stop:
			// Very short recordings should still contain an initial frame.
			r.mu.Lock()
			count := r.framesWritten
			r.mu.Unlock()
			if count == 0 {
				writeLatest()
			}
			_ = r.stdin.Close()
			if err := r.cmd.Wait(); firstErr == nil {
				firstErr = err
			}
			r.done <- firstErr
			return
		}
	}
}

func (s *session) detachRecording(r *recording) {
	r.tab.recMu.Lock()
	if r.tab.rec == r {
		r.tab.rec = nil
	}
	r.tab.recMu.Unlock()
	if s.recording == r {
		s.recording = nil
	}
}

func (s *session) stopRecordingLocked(ctx context.Context) (any, error) {
	r := s.recording
	if r == nil {
		return nil, fail("not_found", "no active recording")
	}
	// Stop production first, then detach and close the channel while holding
	// recMu so a listener cannot race with close.
	stopErr := s.runTab(ctx, r.tab, page.StopScreencast())
	r.tab.recMu.Lock()
	if r.tab.rec == r {
		r.tab.rec = nil
	}
	close(r.stop)
	r.tab.recMu.Unlock()
	s.recording = nil

	var encodeErr error
	select {
	case encodeErr = <-r.done:
	case <-ctx.Done():
		_ = r.cmd.Process.Kill()
		encodeErr = <-r.done
	case <-time.After(10 * time.Second):
		_ = r.cmd.Process.Kill()
		encodeErr = <-r.done
		if encodeErr == nil {
			encodeErr = fmt.Errorf("ffmpeg did not exit promptly")
		}
	}
	_ = os.Chmod(r.path, 0o600)
	r.mu.Lock()
	frames := r.framesWritten
	r.mu.Unlock()
	if stopErr != nil {
		return nil, stopErr
	}
	if encodeErr != nil {
		return nil, fail("action_failed", "encode recording: %v: %s", encodeErr, strings.TrimSpace(r.stderr.String()))
	}
	return map[string]any{"recording": false, "path": r.path, "frames": frames, "silent": true}, nil
}

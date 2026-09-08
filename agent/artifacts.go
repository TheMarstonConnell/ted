package agent

import (
	"bytes"
	"encoding/json"
	"go.uber.org/zap"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxScreenshotsPerTurn = 4
	maxScreenshotBytes    = 1 << 20
	maxManifestReadBytes  = 1 << 20
)

type artifactRecord struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

// newScreenshots reads only complete records appended since the previous bash
// call. The manifest is the authority: command output and unregistered files
// are never interpreted as image paths.
func (a *Agent) newScreenshots(limit int) []screenshotImage {
	if limit <= 0 {
		return nil
	}
	a.artifactMu.Lock()
	defer a.artifactMu.Unlock()

	threadDir := filepath.Join(a.home, "threads", a.threadID)
	manifestPath := filepath.Join(threadDir, "artifacts.jsonl")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() {
		return nil
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(manifestInfo, info) {
		return nil
	}
	if info.Size() < a.manifestOffset {
		// The service replaced/truncated its manifest. Its current records are
		// new from the agent's perspective.
		a.manifestOffset = 0
	}
	startOffset := a.manifestOffset
	if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
		return nil
	}
	overflow := info.Size()-startOffset > maxManifestReadBytes
	chunk, err := io.ReadAll(io.LimitReader(file, maxManifestReadBytes))
	if err != nil {
		return nil
	}
	lastNewline := bytes.LastIndexByte(chunk, '\n')
	if lastNewline < 0 {
		return nil // do not consume a record while the service is writing it
	}
	complete := chunk[:lastNewline+1]
	if overflow {
		// Do not carry an unbounded backlog into future tool calls. The
		// service writes tiny records, so this branch indicates far more
		// artifacts than one turn is allowed to attach.
		a.manifestOffset = info.Size()
	} else {
		a.manifestOffset = startOffset + int64(len(complete))
	}

	var images []screenshotImage
	for _, line := range bytes.Split(complete, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record artifactRecord
		if json.Unmarshal(line, &record) != nil || record.Type != "screenshot" {
			continue
		}
		if len(images) >= limit {
			// Records are consumed even when this turn reached its cap. This
			// keeps a large manifest from leaking images into later turns.
			continue
		}
		if image, ok := loadRegisteredScreenshot(threadDir, record.Path); ok {
			images = append(images, image)
		} else {
			a.logger.Warn("screenshot skipped: invalid image or exceeds size limit", zap.Int("limit_bytes", maxScreenshotBytes))
			a.emit(AgentResponse{ResponseType: "status", Content: "Screenshot skipped: invalid image or larger than 1 MiB. Capture a smaller image."})
		}
	}
	return images
}

func loadRegisteredScreenshot(threadDir, path string) (screenshotImage, bool) {
	if !filepath.IsAbs(path) {
		return screenshotImage{}, false
	}
	artifactsDir := filepath.Join(threadDir, "artifacts")
	cleanPath := filepath.Clean(path)
	if !pathWithin(artifactsDir, cleanPath) {
		return screenshotImage{}, false
	}

	canonicalThreads, err := filepath.EvalSymlinks(filepath.Dir(threadDir))
	if err != nil {
		return screenshotImage{}, false
	}
	canonicalThread, err := filepath.EvalSymlinks(threadDir)
	if err != nil || filepath.Clean(canonicalThread) != filepath.Join(canonicalThreads, filepath.Base(threadDir)) {
		return screenshotImage{}, false
	}
	canonicalRoot, err := filepath.EvalSymlinks(artifactsDir)
	if err != nil || filepath.Clean(canonicalRoot) != filepath.Join(canonicalThread, "artifacts") {
		// Reject an artifacts directory that itself redirects outside the
		// service-owned thread directory.
		return screenshotImage{}, false
	}
	canonicalPath, err := filepath.EvalSymlinks(cleanPath)
	if err != nil || !pathWithin(canonicalRoot, canonicalPath) {
		return screenshotImage{}, false
	}
	info, err := os.Stat(canonicalPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxScreenshotBytes {
		return screenshotImage{}, false
	}
	file, err := os.Open(canonicalPath)
	if err != nil {
		return screenshotImage{}, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxScreenshotBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxScreenshotBytes {
		return screenshotImage{}, false
	}
	mime := http.DetectContentType(data)
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return screenshotImage{MIMEType: strings.Split(mime, ";")[0], Data: data}, true
	default:
		return screenshotImage{}, false
	}
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

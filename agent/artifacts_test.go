package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
)

type screenshotProvider struct {
	mu       sync.Mutex
	requests []CompletionRequest
}

func (p *screenshotProvider) Name() string { return "screenshots" }
func (p *screenshotProvider) ListModels() []ModelInfo {
	return []ModelInfo{{ID: "model"}}
}
func (p *screenshotProvider) Complete(_ *zap.Logger, request CompletionRequest) (*Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	if len(p.requests) == 1 {
		return &Response{Choices: []Choice{{FinishReason: "tool_calls", Message: Message{
			Role:      "assistant",
			ToolCalls: []ToolCall{{Id: "browser", Function: FunctionCall{Name: "bash", Arguments: `{"command":"printf tool-output"}`}}},
		}}}}, nil
	}
	return &Response{Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant", Content: TextContent("seen")}}}}, nil
}

func writeArtifactRecord(t *testing.T, a *Agent, name string, data []byte) string {
	t.Helper()
	artifactDir := filepath.Join(a.home, "threads", a.threadID, "artifacts")
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(artifactDir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(artifactRecord{Type: "screenshot", Path: path})
	manifest := filepath.Join(a.home, "threads", a.threadID, "artifacts.jsonl")
	file, err := os.OpenFile(manifest, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(record, '\n')); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRegisteredScreenshotBecomesUserImageAfterToolOutput(t *testing.T) {
	provider := &screenshotProvider{}
	t.Setenv("TED_HOME", t.TempDir())
	a := NewAgent(zap.NewNop(), []Provider{provider})
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	path := writeArtifactRecord(t, a, "page.png", png)

	if err := a.Turn("look at the page"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d", len(provider.requests))
	}
	messages := provider.requests[1].Messages
	if len(messages) < 3 {
		t.Fatalf("second request messages = %+v", messages)
	}
	tool := messages[len(messages)-2]
	image := messages[len(messages)-1]
	if tool.Role != "tool" || tool.Content.Text() != "tool-output" {
		t.Fatalf("screenshot contaminated tool output: role=%q output=%q", tool.Role, tool.Content.Text())
	}
	if strings.Contains(tool.Content.Text(), path) {
		t.Fatal("artifact path appeared in tool output")
	}
	if image.Role != "user" || len(image.Content.imageURLs()) != 1 {
		t.Fatalf("registered image was not attached as user content: role=%q json=%s", image.Role, image.Content.raw)
	}
	if !strings.HasPrefix(image.Content.imageURLs()[0], "data:image/png;base64,") || strings.Contains(string(image.Content.raw), path) {
		t.Fatalf("image was not embedded safely: %s", image.Content.raw)
	}
}

func TestManifestIsIncrementalAndRejectsUntrustedPaths(t *testing.T) {
	a := &Agent{logger: zap.NewNop(), threadID: "safe-thread", home: t.TempDir(), projectRoot: t.TempDir(), workingDir: t.TempDir()}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	writeArtifactRecord(t, a, "one.png", png)

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, png, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(a.home, "threads", a.threadID, "artifacts.jsonl")
	record, _ := json.Marshal(artifactRecord{Type: "screenshot", Path: outside})
	file, err := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(append(record, '\n'))
	_ = file.Close()

	first := a.newScreenshots(4)
	if len(first) != 1 {
		t.Fatalf("accepted outside path or missed valid image: %d", len(first))
	}
	if again := a.newScreenshots(4); len(again) != 0 {
		t.Fatalf("re-ingested old manifest records: %d", len(again))
	}
}

func TestScreenshotCountAndSizeAreBounded(t *testing.T) {
	a := &Agent{logger: zap.NewNop(), threadID: "bounded", home: t.TempDir()}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	for i := range maxScreenshotsPerTurn + 2 {
		writeArtifactRecord(t, a, string(rune('a'+i))+".png", png)
	}
	writeArtifactRecord(t, a, "large.png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxScreenshotBytes)...))
	if got := len(a.newScreenshots(maxScreenshotsPerTurn)); got != maxScreenshotsPerTurn {
		t.Fatalf("attached %d screenshots, want cap %d", got, maxScreenshotsPerTurn)
	}
	if got := len(a.newScreenshots(maxScreenshotsPerTurn)); got != 0 {
		t.Fatalf("deferred over-limit or oversized images to next call: %d", got)
	}
}

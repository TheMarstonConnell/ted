package browser

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBrowserRecordingIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") == "" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 with Chrome available")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := newManager(ctx, t.TempDir())
	defer m.close()
	project := t.TempDir()
	call := func(action string, params map[string]any) any {
		t.Helper()
		res := dispatchSafely(ctx, m, Request{Project: project, Thread: "recording", Action: action, Params: params, Timeout: 10 * time.Second})
		if !res.OK {
			t.Fatalf("%s: %v", action, res.Error)
		}
		return res.Data
	}
	call("open", map[string]any{"url": "data:text/html,<title>Recording</title><h1>Record this</h1>"})
	call("record-start", nil)
	time.Sleep(1200 * time.Millisecond)
	call("cdp", map[string]any{"method": "Runtime.evaluate", "params": map[string]any{"expression": "document.body.style.background='red'"}})
	time.Sleep(1100 * time.Millisecond)
	stop := call("record-stop", nil).(map[string]any)
	path := stop["path"].(string)
	st, err := os.Stat(path)
	if err != nil || st.Size() < 100 {
		t.Fatalf("invalid video %v %v", st, err)
	}
	output, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &probe); err != nil {
		t.Fatal(err)
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || duration < 1.8 || duration > 4 {
		t.Fatalf("recording must preserve elapsed time (about 2.3s), got %q: %v", probe.Format.Duration, err)
	}
	if len(probe.Streams) != 1 || probe.Streams[0].CodecType != "video" {
		t.Fatalf("expected silent video: %s", output)
	}
	if !strings.HasPrefix(path, m.home) {
		t.Fatal("artifact outside home", path)
	}
}

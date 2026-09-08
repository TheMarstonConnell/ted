package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run with TED_BROWSER_INTEGRATION=1 on a machine with Chrome/Chromium and
// ffmpeg available. It intentionally stays opt-in for headless CI systems.
func TestChromeIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 to run Chrome integration")
	}
	t.Setenv("TED_HOME", filepath.Join(t.TempDir(), "ted"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<title>Browser test</title><label for="who">Who</label><input id="who"><button onclick="console.log('hello',document.querySelector('#who').value)">Go</button><div style="height:1200px">page</div>`))
	}))
	defer server.Close()
	project := t.TempDir()
	mgr := newManager(context.Background(), mustHome(t))
	defer mgr.close()
	call := func(action string, params map[string]any) any {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		data, err := mgr.dispatch(ctx, Request{Project: project, Thread: "integration-1", Action: action, Params: params, Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return data
	}
	call("open", map[string]any{"url": server.URL})
	snap := call("snapshot", nil)
	encoded, _ := json.Marshal(snap)
	if !strings.Contains(string(encoded), "Browser test") || !strings.Contains(string(encoded), "Who") {
		t.Fatalf("snapshot missing content: %.500s", encoded)
	}
	call("fill", map[string]any{"label": "Who", "value": "Ted"})
	call("click", map[string]any{"role": "button", "name": "Go"})
	console := call("console", nil)
	cb, _ := json.Marshal(console)
	if !strings.Contains(string(cb), "Ted") {
		t.Fatalf("console missing click output: %s", cb)
	}
	shot := call("screenshot", map[string]any{"full-page": true}).(map[string]any)
	path := shot["path"].(string)
	if st, err := os.Stat(path); err != nil || st.Size() < 100 {
		t.Fatalf("screenshot stat = %v, %v", st, err)
	}
	manifest, err := os.ReadFile(filepath.Join(mustHome(t), "threads", "integration-1", "artifacts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), path) {
		t.Fatalf("manifest does not register screenshot: %s", manifest)
	}
	closed := mgr.closeSession(projectKey(canonicalPath(project)), "integration-1")
	if closed["closed"] != true {
		t.Fatalf("session close = %#v", closed)
	}
}

func mustHome(t *testing.T) string {
	t.Helper()
	h, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

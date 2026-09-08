package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRequiredValueAllowsClearing(t *testing.T) {
	if value, err := requiredValue(map[string]any{"value": ""}); err != nil || value != "" {
		t.Fatalf("%q %v", value, err)
	}
	if _, err := requiredValue(map[string]any{}); err == nil {
		t.Fatal("missing value accepted")
	}
}

// Opt in with TED_BROWSER_INTEGRATION=1 and Chrome/Chromium on PATH.
func TestBrowserTargetsIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") == "" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 with Chrome available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	m := newManager(ctx, t.TempDir())
	defer m.close()
	project := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Fixture</title></head><body>
<label>Email<input id="email" oninput="document.title='input:'+this.value"></label>
<label>Password<input id="password" type="password" value="secret-value"></label>
<button onclick="document.title='submitted'">Submit</button>
<button>Duplicate</button><button>Duplicate</button>
<button id="swap">Before</button>
<select aria-label="Color"><option value="">None</option><option value="red">Red</option></select>
</body></html>`))
	}))
	defer server.Close()
	call := func(action string, params map[string]any) Response {
		return dispatchSafely(ctx, m, Request{Project: project, Thread: "targets", Action: action, Params: params, Timeout: 5 * time.Second})
	}
	must := func(action string, params map[string]any) any {
		t.Helper()
		r := call(action, params)
		if !r.OK {
			t.Fatalf("%s: %+v", action, r.Error)
		}
		return r.Data
	}
	must("open", map[string]any{"url": server.URL})
	snapshot := must("snapshot", nil)
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "secret-value") {
		t.Fatal("snapshot exposed password", string(encoded))
	}
	var view struct {
		Elements []struct {
			Ref  string `json:"ref"`
			Name string `json:"name"`
		}
	}
	_ = json.Unmarshal(encoded, &view)
	var submitRef, swapRef string
	for _, e := range view.Elements {
		if e.Name == "Submit" {
			submitRef = e.Ref
		}
		if e.Name == "Before" {
			swapRef = e.Ref
		}
	}
	if submitRef == "" || swapRef == "" {
		t.Fatal(string(encoded))
	}
	must("fill", map[string]any{"label": "Email", "value": "hello"})
	title := must("cdp", map[string]any{"method": "Runtime.evaluate", "params": map[string]any{"expression": "document.title", "returnByValue": true}})
	titleJSON, _ := json.Marshal(title)
	if !strings.Contains(string(titleJSON), "input:hello") {
		t.Fatal("fill did not emit input event", string(titleJSON))
	}
	must("fill", map[string]any{"label": "Email", "value": ""})
	must("select", map[string]any{"label": "Color", "value": "red"})
	duplicate := call("click", map[string]any{"role": "button", "name": "Duplicate"})
	if duplicate.OK || duplicate.Error.Code != "target_ambiguous" {
		t.Fatalf("ambiguous target: %+v", duplicate)
	}
	cssDuplicate := call("click", map[string]any{"selector": "button"})
	if cssDuplicate.OK || cssDuplicate.Error.Code != "target_ambiguous" {
		t.Fatalf("ambiguous CSS: %+v", cssDuplicate)
	}
	must("click", map[string]any{"ref": submitRef})
	must("cdp", map[string]any{"method": "Runtime.evaluate", "params": map[string]any{"expression": "document.getElementById('swap').textContent='After'"}})
	stale := call("click", map[string]any{"ref": swapRef})
	if stale.OK || stale.Error.Code != "stale_ref" {
		t.Fatalf("changed ref: %+v", stale)
	}
	must("open", map[string]any{"url": server.URL + "/next"})
	stale = call("click", map[string]any{"ref": submitRef})
	if stale.OK || stale.Error.Code != "stale_ref" {
		t.Fatalf("navigated ref: %+v", stale)
	}
	must("snapshot", nil)
	stale = call("click", map[string]any{"ref": submitRef})
	if stale.OK || stale.Error.Code != "stale_ref" {
		t.Fatalf("rebound ref: %+v", stale)
	}
	must("cdp", map[string]any{"method": "Runtime.evaluate", "params": map[string]any{"expression": "setTimeout(()=>{const b=document.createElement('button');b.textContent='Later';document.body.appendChild(b)},150)"}})
	must("click", map[string]any{"role": "button", "name": "Later"})
}

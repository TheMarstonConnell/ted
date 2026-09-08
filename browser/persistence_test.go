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

func TestBrowserPersistenceAndThreadIsolationIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") == "" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 with Chrome available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	home, project := t.TempDir(), t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<title>Persist</title><button>OK</button>`))
	}))
	defer server.Close()
	m := newManager(ctx, home)
	defer func() { m.close() }()
	call := func(thread, action string, params map[string]any) Response {
		return dispatchSafely(ctx, m, Request{Project: project, Thread: thread, Action: action, Params: params, Timeout: 5 * time.Second})
	}
	must := func(thread, action string, params map[string]any) any {
		t.Helper()
		r := call(thread, action, params)
		if !r.OK {
			t.Fatalf("%s/%s: %+v", thread, action, r.Error)
		}
		return r.Data
	}
	eval := func(thread, expression string) string {
		t.Helper()
		data := must(thread, "cdp", map[string]any{"method": "Runtime.evaluate", "params": map[string]any{"expression": expression, "returnByValue": true}})
		encoded, _ := json.Marshal(data)
		return string(encoded)
	}
	first := must("first", "open", map[string]any{"url": server.URL}).(map[string]any)
	eval("first", `document.cookie="ted_session=remembered;max-age=3600;path=/";localStorage.setItem("ted_identity","project")`)
	second := must("second", "open", map[string]any{"url": server.URL}).(map[string]any)
	if first["id"] == second["id"] {
		t.Fatal("threads share tab")
	}
	if got := eval("second", "document.cookie"); !strings.Contains(got, "ted_session=remembered") {
		t.Fatal("threads did not share auth", got)
	}
	stolen := call("second", "tab-select", map[string]any{"id": first["id"]})
	if stolen.OK {
		t.Fatal("thread selected another thread's tab")
	}
	must("isolated", "open", map[string]any{"url": server.URL, "isolated": true})
	if got := eval("isolated", "document.cookie"); strings.Contains(got, "ted_session=remembered") {
		t.Fatal("isolated session inherited auth", got)
	}
	// An individual operation timing out must not kill the browser or tab.
	timed := dispatchSafely(ctx, m, Request{Project: project, Thread: "second", Action: "wait", Params: map[string]any{"selector": "#never"}, Timeout: 100 * time.Millisecond})
	if timed.OK || timed.Error.Code != "timeout" {
		t.Fatal(timed)
	}
	must("second", "snapshot", nil)
	m.close()
	m = newManager(ctx, home)
	must("resumed", "open", map[string]any{"url": server.URL})
	if got := eval("resumed", "document.cookie"); !strings.Contains(got, "ted_session=remembered") {
		t.Fatal("lost persistent cookie", got)
	}
	if got := eval("resumed", "localStorage.getItem('ted_identity')"); !strings.Contains(got, "project") {
		t.Fatal("lost local storage", got)
	}
	otherProject := t.TempDir()
	other := dispatchSafely(ctx, m, Request{Project: otherProject, Thread: "different-project", Action: "open", Params: map[string]any{"url": server.URL}, Timeout: 5 * time.Second})
	if !other.OK {
		t.Fatal(other.Error)
	}
	state := dispatchSafely(ctx, m, Request{Project: otherProject, Thread: "different-project", Action: "cdp", Params: map[string]any{
		"method": "Runtime.evaluate", "params": map[string]any{"expression": `document.cookie + "|" + localStorage.getItem("ted_identity")`, "returnByValue": true},
	}, Timeout: 5 * time.Second})
	if !state.OK {
		t.Fatal(state.Error)
	}
	encoded, err := json.Marshal(state.Data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "remembered") || strings.Contains(string(encoded), `|project`) {
		t.Fatalf("another project inherited browser identity: %s", encoded)
	}
}

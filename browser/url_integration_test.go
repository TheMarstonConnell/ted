//go:build !windows

package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Exercise the CLI action dispatcher and the live wire parser against Chrome,
// rather than just asserting that a helper produces the expected string.
func TestAddressNormalizationIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 to run Chrome integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mgr := newManager(ctx, t.TempDir())
	defer mgr.close()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<title>Normalized address</title>`))
	}))
	defer fixture.Close()
	address := strings.TrimPrefix(fixture.URL, "http://")
	req := Request{Project: t.TempDir(), Thread: "address-normalization", Timeout: 20 * time.Second}
	call := func(action, raw string) map[string]any {
		t.Helper()
		r := req
		r.Action, r.Params = action, map[string]any{"url": raw}
		result, err := mgr.dispatch(ctx, r)
		if err != nil {
			t.Fatalf("%s(%q): %v", action, raw, err)
		}
		return result.(map[string]any)
	}
	for _, tc := range []struct{ action, raw, path string }{
		{"open", " \t" + address + "/open?q=yes#part \n", "/open?q=yes#part"},
		{"tab-new", "//" + address + "/tab-new", "/tab-new"},
		{"open", strings.Replace(address, "127.0.0.1", "localhost", 1) + "/local", "/local"},
	} {
		result := call(tc.action, tc.raw)
		want := fixture.URL + tc.path
		if tc.path == "/local" {
			want = strings.Replace(want, "127.0.0.1", "localhost", 1)
		}
		if result["url"] != want {
			t.Fatalf("%s URL = %v, want %s", tc.action, result["url"], want)
		}
	}
	blank := call("tab-new", "")
	if blank["url"] != "about:blank" {
		t.Fatalf("empty tab-new URL = %v", blank["url"])
	}
	viewer := liveTestClient(t, ctx, mgr, req)
	initial := receiveLive(t, viewer, func(e LiveEvent) bool { return e.Type == "state" && len(e.Tabs) == 3 })
	for _, tc := range []struct{ kind, raw, path string }{
		{"navigate", " " + address + "/live-navigate ", "/live-navigate"},
		{"new", "//" + address + "/live-new", "/live-new"},
	} {
		command := LiveCommand{Type: tc.kind, URL: tc.raw}
		if tc.kind == "navigate" {
			command.TabID = initial.Selected
		}
		if err := viewer.Send(command); err != nil {
			t.Fatal(err)
		}
		receiveLive(t, viewer, func(e LiveEvent) bool {
			if e.Type != "state" {
				return false
			}
			for _, tab := range e.Tabs {
				if tab.URL == fixture.URL+tc.path {
					return true
				}
			}
			return false
		})
	}
	// Unsafe input must fail at both boundaries, without executing in Chrome.
	for _, action := range []string{"open", "tab-new"} {
		r := req
		r.Action, r.Params = action, map[string]any{"url": "javascript:document.title='unsafe'"}
		_, err := mgr.dispatch(ctx, r)
		if err == nil || errorResponse(err).Error.Code != "invalid_params" {
			t.Fatalf("%s unsafe URL error = %v", action, err)
		}
	}
	if err := viewer.Send(LiveCommand{Type: "navigate", TabID: initial.Selected, URL: "javascript:document.title='unsafe'"}); err != nil {
		t.Fatal(err)
	}
	_ = viewer.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		event, err := viewer.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			if event.Code != "invalid_params" {
				t.Fatalf("unsafe live URL error = %+v", event)
			}
			break
		}
	}
}

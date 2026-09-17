package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

func TestNetworkDiagnosticsIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 to run Chrome integration")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		case "/rate-limit":
			w.WriteHeader(http.StatusTooManyRequests)
		}
		_, _ = w.Write([]byte("<title>Diagnostics fixture</title>"))
	}))
	defer server.Close()
	crossOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("cross-origin response without CORS permission"))
	}))
	defer crossOrigin.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("untrusted certificate"))
	}))
	defer tlsServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mgr := newManager(ctx, t.TempDir())
	defer mgr.close()
	project := t.TempDir()
	call := func(action string, params map[string]any) (any, error) {
		return mgr.dispatch(ctx, Request{Project: project, Thread: "diagnostics", Action: action, Params: params})
	}
	mustCall := func(action string, params map[string]any) any {
		t.Helper()
		data, err := call(action, params)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return data
	}
	waitForDiagnostic := func(match func(map[string]any) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			data := mustCall("errors", nil).(map[string]any)
			for _, raw := range data["errors"].([]any) {
				if item, ok := raw.(map[string]any); ok && item["source"] == "network" && match(item) {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("missing diagnostic: %#v", data)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// HTTP denials are still navigable pages, not transport failures.
	for _, test := range []struct {
		path   string
		status int64
	}{{"/forbidden", 403}, {"/rate-limit", 429}} {
		mustCall("open", map[string]any{"url": server.URL + test.path})
		waitForDiagnostic(func(item map[string]any) bool {
			return item["type"] == "http_error" && item["status"] == test.status && item["resource_type"] == network.ResourceTypeDocument
		})
		mustCall("errors", map[string]any{"clear": true})
	}
	mustCall("cdp", map[string]any{"method": "Runtime.evaluate", "params": map[string]any{
		"expression": fmt.Sprintf("fetch(%q).catch(() => {})", crossOrigin.URL),
	}})
	waitForDiagnostic(func(item map[string]any) bool {
		return item["type"] == "loading_failed" && item["cors_error"] == network.CorsErrorMissingAllowOriginHeader
	})
	mustCall("errors", map[string]any{"clear": true})
	if _, err := call("open", map[string]any{"url": tlsServer.URL}); err == nil || !strings.Contains(err.Error(), "ERR_CERT_AUTHORITY_INVALID") {
		t.Fatalf("untrusted TLS navigation = %v", err)
	}
	waitForDiagnostic(func(item map[string]any) bool {
		return item["type"] == "loading_failed" && item["text"] == "net::ERR_CERT_AUTHORITY_INVALID"
	})
	mustCall("open", map[string]any{"url": server.URL})
	data := mustCall("cdp", map[string]any{"browser": true, "method": "Browser.getBrowserCommandLine"}).(map[string]any)
	args := data["result"].(map[string]any)["arguments"].([]any)
	automation := false
	for _, raw := range args {
		arg := raw.(string)
		if arg == "--enable-automation" {
			automation = true
		}
		for _, prohibited := range []string{"--no-sandbox", "--disable-web-security", "--ignore-certificate-errors", "--disable-features="} {
			if strings.HasPrefix(arg, prohibited) {
				t.Errorf("unsafe or legacy launch override: %s", arg)
			}
		}
	}
	if !automation {
		t.Fatal("Chrome no longer exposes its automation launch flag")
	}
}

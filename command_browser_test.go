package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/browser"
)

func TestBrowserCommandDispatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TED_PROJECT_ROOT", dir)
	t.Setenv("TED_THREAD_ID", "thread-test")
	cases := []struct {
		args   []string
		action string
		params map[string]any
	}{
		{[]string{"open", "https://example.com"}, "open", map[string]any{"url": "https://example.com"}},
		{[]string{"fill", "--label", "Email", "--value", ""}, "fill", map[string]any{"label": "Email", "value": ""}},
		{[]string{"click", "--ref", "e1"}, "click", map[string]any{"ref": "e1"}},
		{[]string{"wait", "--url-pattern", "*/dashboard"}, "wait", map[string]any{"url-pattern": "*/dashboard"}},
		{[]string{"screenshot", "--full-page"}, "screenshot", map[string]any{"full-page": true}},
		{[]string{"tab", "new", "about:blank"}, "tab-new", map[string]any{"url": "about:blank"}},
		{[]string{"tab", "select", "tab123"}, "tab-select", map[string]any{"id": "tab123"}},
		{[]string{"record", "start"}, "record-start", map[string]any{}},
		{[]string{"cdp", "Page.getLayoutMetrics"}, "cdp", map[string]any{"method": "Page.getLayoutMetrics", "params": map[string]any{}, "browser": false}},
		{[]string{"cdp", "listen", "Network.responseReceived", "--limit", "5"}, "cdp-listen", map[string]any{"event": "Network.responseReceived", "limit": 5}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			called := false
			cmd := newBrowserCommand(func(_ context.Context, req browser.Request) (browser.Response, error) {
				called = true
				if req.Project != dir || req.Thread != "thread-test" || req.Action != tc.action || req.Timeout != 30*time.Second {
					t.Fatalf("request: %#v", req)
				}
				got, _ := json.Marshal(req.Params)
				want, _ := json.Marshal(tc.params)
				if !bytes.Equal(got, want) {
					t.Fatalf("params %s want %s", got, want)
				}
				return browser.Response{OK: true, Data: "ok"}, nil
			})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !called || !strings.Contains(out.String(), `"ok":true`) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestBrowserCommandRejectsInvalidArgumentsBeforeDispatch(t *testing.T) {
	for _, args := range [][]string{
		{"click"}, {"fill", "--ref", "e1"}, {"click", "--ref", "e1", "--selector", "button"},
		{"wait"}, {"press"}, {"open"}, {"cdp", "X.y", "--params", "[]"}, {"cdp", "X.y", "--params", "null"},
		{"cdp", "X.y", "--params", "{}", "--params-file", "-"}, {"cdp", "listen", "X.y", "--limit", "0"},
		{"status", "--timeout", "0s"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newBrowserCommand(func(context.Context, browser.Request) (browser.Response, error) {
				t.Fatal("unexpected dispatch")
				return browser.Response{}, nil
			})
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestBrowserCDPStdinAndExplicitIdentity(t *testing.T) {
	dir := t.TempDir()
	cmd := newBrowserCommand(func(_ context.Context, req browser.Request) (browser.Response, error) {
		if req.Project != dir || req.Thread != "explicit" || req.Params["isolated"] != true || req.Params["browser"] != true {
			t.Fatal(req)
		}
		params := req.Params["params"].(map[string]any)
		if params["expression"] != "document.title" {
			t.Fatal(params)
		}
		return browser.Response{OK: true}, nil
	})
	cmd.SetIn(strings.NewReader(`{"expression":"document.title"}`))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"cdp", "Runtime.evaluate", "--params-file", "-", "--project", dir, "--thread", "explicit", "--isolated", "--browser"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserCommandServiceErrorIsJSONAndFailure(t *testing.T) {
	t.Setenv("TED_PROJECT_ROOT", t.TempDir())
	cmd := newBrowserCommand(func(context.Context, browser.Request) (browser.Response, error) {
		return browser.Response{Error: &browser.Error{Code: "target_missing", Message: "not found"}}, nil
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"click", "--ref", "e1"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected nonzero exit")
	}
	if !strings.Contains(out.String(), `"code":"target_missing"`) {
		t.Fatal(out.String())
	}
}

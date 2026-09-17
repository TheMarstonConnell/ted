package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/browser"
	"github.com/spf13/cobra"
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

func TestBrowserListPagination(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TED_PROJECT_ROOT", dir)
	for _, tc := range []struct {
		action, name             string
		flags                    []string
		count, first, last, page int
		more                     bool
	}{
		{"tabs", "default", nil, 25, 0, 24, 1, true},
		{"tabs", "second", []string{"--page", "2", "--page-size", "10"}, 10, 10, 19, 2, true},
		{"tabs", "last", []string{"--page", "4", "--page-size", "10"}, 2, 30, 31, 4, false},
		{"tabs", "past end", []string{"--page", "9"}, 0, 0, 0, 9, false},
		{"tabs", "all", []string{"--all"}, 32, 0, 31, 0, false},
		{"status", "default", nil, 25, 0, 24, 1, true},
		{"console", "default", nil, 25, 0, 24, 1, true},
		{"errors", "default", nil, 25, 0, 24, 1, true},
	} {
		action, key := tc.action, tc.action
		if action == "status" {
			key = "projects"
		}
		t.Run(action+"/"+tc.name, func(t *testing.T) {
			cmd := newBrowserCommand(func(_ context.Context, req browser.Request) (browser.Response, error) {
				if req.Action != action || req.Thread != "thread 'one'" {
					t.Fatal(req)
				}
				items := make([]any, 32)
				for i := range items {
					items[i] = map[string]any{"id": i, "text": "preserved"}
				}
				return browser.Response{OK: true, Data: map[string]any{key: items, "extra": "unchanged", "daemon": true}}, nil
			})
			root := &cobra.Command{Use: "ted"}
			root.AddCommand(cmd)
			var out, notices bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&notices)
			root.SetArgs(append([]string{"browser", action, "--project", dir, "--thread", "thread 'one'", "--timeout", "15s"}, tc.flags...))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			var response struct {
				OK   bool                       `json:"ok"`
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			var items []struct {
				ID   int    `json:"id"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(response.Data[key], &items); err != nil {
				t.Fatal(err)
			}
			if !response.OK || len(items) != tc.count || string(response.Data["extra"]) != `"unchanged"` || string(response.Data["daemon"]) != "true" || notices.Len() != 0 {
				t.Fatalf("stdout=%s stderr=%s", out.String(), notices.String())
			}
			if tc.count > 0 && (items[0].ID != tc.first || items[len(items)-1].ID != tc.last || items[0].Text != "preserved") {
				t.Fatal(items)
			}
			if tc.page == 0 {
				if _, ok := response.Data["pagination"]; ok {
					t.Fatal("--all unexpectedly changed response shape")
				}
				return
			}
			var pagination struct {
				Page, Total int
				HasMore     bool   `json:"has_more"`
				NextPage    int    `json:"next_page"`
				NextCommand string `json:"next_command"`
			}
			if err := json.Unmarshal(response.Data["pagination"], &pagination); err != nil {
				t.Fatal(err)
			}
			if pagination.Page != tc.page || pagination.Total != 32 || pagination.HasMore != tc.more {
				t.Fatal(pagination)
			}
			if tc.more {
				if pagination.NextPage != tc.page+1 || !strings.Contains(pagination.NextCommand, "ted browser "+action) || !strings.Contains(pagination.NextCommand, "--thread 'thread '\"'\"'one'\"'\"''") || !strings.Contains(pagination.NextCommand, "--project "+dir) || !strings.Contains(pagination.NextCommand, "--timeout 15s") {
					t.Fatal(pagination)
				}
			} else if pagination.NextPage != 0 || pagination.NextCommand != "" {
				t.Fatal(pagination)
			}
		})
	}
}

func TestBrowserEmptyListPagination(t *testing.T) {
	t.Setenv("TED_PROJECT_ROOT", t.TempDir())
	cmd := newBrowserCommand(func(context.Context, browser.Request) (browser.Response, error) {
		return browser.Response{OK: true, Data: map[string]any{"console": nil}}, nil
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"console"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"console":[]`) || !strings.Contains(out.String(), `"total":0`) {
		t.Fatal(out.String())
	}
}

func TestBrowserCloseDispatchesMissingProjectIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "removed")
	called := false
	cmd := newBrowserCommand(func(_ context.Context, req browser.Request) (browser.Response, error) {
		called = true
		if req.Project != dir || req.Thread != "cleanup" || req.Action != "session-close" {
			t.Fatalf("cleanup identity changed: %+v", req)
		}
		return browser.Response{OK: true}, nil
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"close", "--project", dir, "--thread", "cleanup"})
	if err := cmd.Execute(); err != nil || !called {
		t.Fatalf("missing root blocked cleanup: called=%v err=%v", called, err)
	}
}

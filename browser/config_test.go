package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBrowserConfig(t *testing.T, home, text string) {
	t.Helper()
	dir := filepath.Join(home, "browser")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserLaunchConfig(t *testing.T) {
	for _, tt := range []struct {
		name, file, wantMode, wantError string
	}{
		{"absent", "", modeHeadless, ""},
		{"empty object", `{}`, modeHeadless, ""},
		{"headed", `{"mode":"headed"}`, modeHeaded, ""},
		{"headless", `{"mode":"headless"}`, modeHeadless, ""},
		{"typo", `{"mode":"headful"}`, "", "mode must be"},
		{"empty mode", `{"mode":""}`, "", "mode must be"},
		{"null mode", `{"mode":null}`, "", "mode must be"},
		{"unknown option", `{"mode":"headed","disable_security":true}`, "", "unknown field"},
		{"wrong type", `{"mode":false}`, "", "cannot unmarshal"},
		{"null object", `null`, "", "JSON object"},
		{"empty file", " ", "", "JSON object"},
		{"multiple objects", `{"mode":"headed"}{}`, "", "exactly one"},
		{"trailing junk", `{"mode":"headed"}oops`, "", "exactly one"},
		{"malformed", `{"mode":"headed"`, "", "unexpected EOF"},
		{"relative executable", `{"mode":"headed","executable":"./chrome"}`, "", "absolute"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.file != "" {
				writeBrowserConfig(t, home, tt.file)
			}
			config, err := readLaunchConfig(home)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || !strings.Contains(err.Error(), filepath.Join(home, "browser", "config.json")) {
					t.Fatalf("missing actionable configuration error: %v", err)
				}
			} else if err != nil || config.Mode != tt.wantMode {
				t.Fatalf("configuration = %+v, %v", config, err)
			}
		})
	}
}

func TestBrowserLaunchConfigIOError(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "browser", "config.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readLaunchConfig(home); err == nil {
		t.Fatal("unreadable configuration silently selected headless mode")
	}
}

func TestBrowserConfigIsStableUntilDaemonRestart(t *testing.T) {
	home := t.TempDir()
	m := newManager(context.Background(), home)
	defer m.close()
	writeBrowserConfig(t, home, `{"mode":"headed"}`)
	first, err := m.launchConfig()
	if err != nil {
		t.Fatal(err)
	}
	writeBrowserConfig(t, home, `{"mode":"headless"}`)
	unchanged, err := m.launchConfig()
	if err != nil || unchanged != first {
		t.Fatalf("configuration changed within one daemon: %+v, %v", unchanged, err)
	}
	restarted := newManager(context.Background(), home)
	defer restarted.close()
	next, err := restarted.launchConfig()
	if err != nil || next.Mode != modeHeadless {
		t.Fatalf("new daemon did not read saved configuration: %+v, %v", next, err)
	}
}

func TestInvalidBrowserConfigFailsBeforeChromeLaunch(t *testing.T) {
	home := t.TempDir()
	writeBrowserConfig(t, home, `{"mode":"headful"}`)
	m := newManager(context.Background(), home)
	defer m.close()
	response := dispatchSafely(t.Context(), m, Request{Project: t.TempDir(), Thread: "bad-config", Action: "open", Params: map[string]any{"url": "about:blank"}})
	if response.OK || response.Error == nil || response.Error.Code != "browser_unavailable" || !strings.Contains(response.Error.Message, "mode must be") {
		t.Fatalf("configuration failure was not surfaced: %+v", response)
	}
	if _, err := os.Stat(filepath.Join(home, "browser", "projects")); !os.IsNotExist(err) {
		t.Fatalf("invalid configuration touched Chrome profiles: %v", err)
	}
}

//go:build linux

package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/chromedp/chromedp"
	"strings"
	"testing"
	"time"
)

func TestBrowserModePersistenceIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 with Chrome and Xvfb available")
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb is required for managed headed integration")
	}
	t.Setenv("DISPLAY", "")
	var chrome string
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			chrome = path
			break
		}
	}
	if chrome == "" {
		t.Fatal("Chrome/Chromium is required")
	}
	home, project := t.TempDir(), t.TempDir()
	marker := filepath.Join(t.TempDir(), "launches")
	launcher := filepath.Join(t.TempDir(), "configured chrome")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nprintf 'started\\n' >> " + quote(marker) + "\nexec " + quote(chrome) + " \"$@\"\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<title>Mode persistence</title>"))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for i, mode := range []string{modeHeadless, modeHeaded, modeHeadless} {
		config, _ := json.Marshal(launchConfig{Mode: mode, Executable: launcher})
		writeBrowserConfig(t, home, string(config))
		m := newManager(ctx, home)
		func() {
			defer m.close()
			call := func(action string, params map[string]any) any {
				t.Helper()
				data, err := m.dispatch(ctx, Request{Project: project, Thread: "mode", Action: action, Params: params})
				if err != nil {
					t.Fatalf("%s/%s: %v", mode, action, err)
				}
				return data
			}
			call("open", map[string]any{"url": fixture.URL})
			cdp := func(method string, params map[string]any, browser bool) map[string]any {
				t.Helper()
				data := call("cdp", map[string]any{"method": method, "params": params, "browser": browser})
				return data.(map[string]any)["result"].(map[string]any)
			}
			version := cdp("Browser.getVersion", nil, true)
			headless := strings.Contains(version["userAgent"].(string), "HeadlessChrome")
			if headless != (mode == modeHeadless) {
				t.Fatalf("mode was not applied to real Chrome: %s %+v", mode, version)
			}
			arguments := cdp("Browser.getBrowserCommandLine", nil, true)["arguments"].([]any)
			automation := false
			for _, raw := range arguments {
				arg := raw.(string)
				automation = automation || arg == "--enable-automation"
				for _, forbidden := range []string{"--no-sandbox", "--disable-web-security", "--ignore-certificate-errors", "--user-agent="} {
					if strings.HasPrefix(arg, forbidden) {
						t.Fatalf("mode change added a security/fingerprint override: %s", arg)
					}
				}
			}
			if !automation {
				t.Fatal("headed mode removed automation disclosure")
			}
			evaluate := func(expression string) any {
				t.Helper()
				return cdp("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, false)["result"].(map[string]any)["value"]
			}
			if i == 0 {
				evaluate(`document.cookie="mode_test=preserved;max-age=3600;path=/";localStorage.setItem("mode_test","preserved")`)
			} else if evaluate(`document.cookie.includes("mode_test=preserved") && localStorage.getItem("mode_test")==="preserved"`) != true {
				t.Fatalf("switching to %s lost project cookies/storage", mode)
			}
			if evaluate(`navigator.webdriver`) != true {
				t.Fatal("automation property was hidden")
			}
		}()
		leftovers, err := filepath.Glob(filepath.Join(home, "browser", "projects", "*", "headed-display-*"))
		if err != nil || len(leftovers) != 0 {
			t.Fatalf("daemon shutdown leaked display credentials: %v %v", leftovers, err)
		}
	}
	launches, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(launches), "started\n") != 3 {
		t.Fatalf("configured executable was not used on every restart: %q %v", launches, err)
	}
}

func TestHeadedChromeStartupFailureReleasesDisplay(t *testing.T) {
	pidPath := fakeDisplay(t, "ready")
	home, project := t.TempDir(), t.TempDir()
	missingChrome := filepath.Join(t.TempDir(), "chrome-does-not-exist")
	config, _ := json.Marshal(launchConfig{Mode: modeHeaded, Executable: missingChrome})
	writeBrowserConfig(t, home, string(config))
	profile := filepath.Join(home, "browser", "projects", projectKey(canonicalPath(project)), "profile")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(profile, "existing-profile-data")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newManager(t.Context(), home)
	defer m.close()
	response := dispatchSafely(t.Context(), m, Request{Project: project, Thread: "failed-launch", Action: "open", Params: map[string]any{"url": "about:blank"}})
	if response.OK || response.Error == nil || response.Error.Code != "browser_unavailable" || !strings.Contains(response.Error.Message, "chrome-does-not-exist") {
		t.Fatalf("missing Chrome failure = %+v", response)
	}
	assertDisplayReaped(t, readDisplayChild(t, pidPath))
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(profile), "headed-display-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("failed Chrome startup leaked display credentials: %v %v", leftovers, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("display cleanup changed the existing Chrome profile: %q %v", data, err)
	}
}

func TestHeadedChromeExitReleasesDisplayIntegration(t *testing.T) {
	if os.Getenv("TED_BROWSER_INTEGRATION") != "1" {
		t.Skip("set TED_BROWSER_INTEGRATION=1 with Chrome and Xvfb available")
	}
	xvfb, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Fatal(err)
	}
	for _, exit := range []string{"crash", "graceful"} {
		t.Run(exit, func(t *testing.T) {
			t.Setenv("DISPLAY", "")
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "display.pid")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			script := "#!/bin/sh\necho $$ > " + quote(pidFile) + "\nexec " + quote(xvfb) + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(dir, "Xvfb"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			home, project := t.TempDir(), t.TempDir()
			config, _ := json.Marshal(launchConfig{Mode: modeHeaded, Executable: os.Getenv("TED_BROWSER_HEADED_TEST_EXECUTABLE")})
			writeBrowserConfig(t, home, string(config))
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			m := newManager(ctx, home)
			defer m.close()
			if _, err := m.dispatch(ctx, Request{Project: project, Thread: "exit", Action: "open", Params: map[string]any{"url": "about:blank"}}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			p := m.projects[projectKey(canonicalPath(project))]
			chrome := chromedp.FromContext(p.browserCtx).Browser.Process()
			if exit == "crash" {
				if err := chrome.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := chromedp.Cancel(p.browserCtx); err != nil {
					t.Fatal(err)
				}
			}
			for {
				leftovers, err := filepath.Glob(filepath.Join(home, "browser", "projects", "*", "headed-display-*"))
				if err != nil {
					t.Fatal(err)
				}
				if len(leftovers) == 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("Chrome exit leaked its display while daemon remained alive")
				case <-time.After(20 * time.Millisecond):
				}
			}
			assertDisplayReaped(t, displayChild{PID: pid})
			assertDisplayReaped(t, displayChild{PID: chrome.Pid})
			if err := m.ctx.Err(); err != nil {
				t.Fatalf("daemon stopped: %v", err)
			}
		})
	}
}

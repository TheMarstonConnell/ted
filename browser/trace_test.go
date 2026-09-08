package browser

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTraceIsSanitizedPrivateAndBounded(t *testing.T) {
	dir := t.TempDir()
	m := newManager(t.Context(), dir)
	s := &session{dir: dir, project: &projectBrowser{manager: m}}
	s.writeTrace("fill", time.Now().Add(-time.Millisecond), fail("action_failed", "secret-value-that-must-not-be-written"))
	path := filepath.Join(dir, "browser-trace.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-value") {
		t.Fatalf("trace leaked error/parameter data: %s", data)
	}
	if !strings.Contains(string(data), `"action":"fill"`) || !strings.Contains(string(data), `"code":"action_failed"`) {
		t.Fatalf("trace = %s", data)
	}
	if mode := mustStat(t, path).Mode().Perm(); mode != 0o600 {
		t.Fatalf("trace mode = %o", mode)
	}
	if err := os.WriteFile(path, make([]byte, maxTraceBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	s.writeTrace("cdp", time.Now(), nil)
	if _, err := os.Stat(filepath.Join(dir, "browser-trace.previous.jsonl")); err != nil {
		t.Fatal("trace was not rotated", err)
	}
	if st := mustStat(t, path); st.Size() >= maxTraceBytes {
		t.Fatalf("new trace was not bounded: %d", st.Size())
	}
}

func TestErrorCode(t *testing.T) {
	if got := errorCode(errors.New("plain")); got != "internal" {
		t.Fatal(got)
	}
}

package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHomeAndThreadPathPrivacy(t *testing.T) {
	t.Setenv("TED_HOME", filepath.Join(t.TempDir(), "state"))
	home, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(home) {
		t.Fatalf("Home() = %q, want absolute", home)
	}
	if mode := mustStat(t, home).Mode().Perm(); mode != 0o700 {
		t.Fatalf("home mode = %o", mode)
	}

	dir, err := threadDir(home, "thread_01.ok")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != filepath.Join(home, "threads") {
		t.Fatalf("unexpected thread dir %q", dir)
	}
	if mode := mustStat(t, dir).Mode().Perm(); mode != 0o700 {
		t.Fatalf("thread mode = %o", mode)
	}
}

func TestValidateThreadRejectsTraversal(t *testing.T) {
	bad := []string{"", ".", "..", "../x", "x/y", `x\\y`, "/tmp/x", " x", "x\n", string(make([]byte, 129))}
	for _, id := range bad {
		if err := validateThread(id); err == nil {
			t.Errorf("validateThread(%q) unexpectedly succeeded", id)
		}
	}
	for _, id := range []string{"a", "01HZY-abc_def.9"} {
		if err := validateThread(id); err != nil {
			t.Errorf("validateThread(%q): %v", id, err)
		}
	}
}

func TestProjectRootGitAndFallback(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command("git", "-C", root, "init", "-q")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
		got, err := ProjectRoot(nested)
		if err != nil {
			t.Fatal(err)
		}
		if got != canonicalPath(root) {
			t.Fatalf("ProjectRoot = %q, want %q", got, canonicalPath(root))
		}
	}

	plain := t.TempDir()
	got, err := ProjectRoot(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got != canonicalPath(plain) {
		t.Fatalf("fallback ProjectRoot = %q", got)
	}
}

func TestSocketPathFallsBackForLongHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets")
	}
	long := t.TempDir()
	for i := 0; i < 8; i++ {
		long = filepath.Join(long, "abcdefghijklmnop")
		if err := os.Mkdir(long, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TED_HOME", long)
	p, err := socketPath()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) >= 100 {
		t.Fatalf("socket path still too long: %d %q", len(p), p)
	}
	if mode := mustStat(t, filepath.Dir(p)).Mode().Perm(); mode != 0o700 {
		t.Fatalf("socket parent mode = %o", mode)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

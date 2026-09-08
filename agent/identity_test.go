package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
)

func TestAgentIdentityIsFreshStableAndDoesNotMutateEnvironment(t *testing.T) {
	t.Setenv(threadIDEnvironment, "parent-thread")
	t.Setenv(projectRootEnvironment, "/parent/root")
	t.Setenv("TED_HOME", t.TempDir())

	first := NewAgent(zap.NewNop(), nil)
	second := NewAgent(zap.NewNop(), nil)
	if first.ThreadID() == "" || first.ThreadID() == "parent-thread" {
		t.Fatalf("agent inherited or omitted its thread ID: %q", first.ThreadID())
	}
	if first.ThreadID() == second.ThreadID() {
		t.Fatalf("agents shared thread ID %q", first.ThreadID())
	}
	stable := first.ThreadID()
	for range 10 {
		if got := first.ThreadID(); got != stable {
			t.Fatalf("thread ID changed from %q to %q", stable, got)
		}
	}
	if os.Getenv(threadIDEnvironment) != "parent-thread" || os.Getenv(projectRootEnvironment) != "/parent/root" {
		t.Fatal("NewAgent mutated the process environment")
	}
}

func TestAgentConstructionDoesNotCreateThreadData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TED_HOME", home)
	a := NewAgent(zap.NewNop(), nil)
	if _, err := os.Stat(filepath.Join(home, "threads", a.ThreadID())); !os.IsNotExist(err) {
		t.Fatalf("thread data was eagerly created: %v", err)
	}
}

func TestResolveProjectRootFromSubdirectory(t *testing.T) {
	repository := t.TempDir()
	if output, err := exec.Command("git", "-C", repository, "init", "--quiet").CombinedOutput(); err != nil {
		t.Skipf("git is unavailable: %v: %s", err, output)
	}
	subdirectory := filepath.Join(repository, "frontend", "src")
	if err := os.MkdirAll(subdirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveProjectRoot(subdirectory); got != want {
		t.Fatalf("project root = %q, want %q", got, want)
	}
}

func TestBashUsesAgentRootAndScopedEnvironment(t *testing.T) {
	root := t.TempDir()
	workingDir := filepath.Join(root, "frontend")
	if err := os.Mkdir(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		logger:      zap.NewNop(),
		threadID:    "thread-one",
		projectRoot: root,
		workingDir:  workingDir,
		home:        t.TempDir(),
	}
	result := a.runToolCall(ToolCall{Function: FunctionCall{
		Name:      "bash",
		Arguments: `{"command":"printf '%s\\n%s\\n%s' \"$TED_THREAD_ID\" \"$TED_PROJECT_ROOT\" \"$PWD\""}`,
	}})
	lines := strings.Split(result, "\n")
	if len(lines) != 3 || lines[0] != "thread-one" || lines[1] != root || lines[2] != workingDir {
		t.Fatalf("unexpected scoped bash context: %q", result)
	}
}

func TestThreadIdentityConcurrentAccess(t *testing.T) {
	a := NewAgent(zap.NewNop(), nil)
	wantID, wantRoot := a.ThreadID(), a.ProjectRoot()
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				if a.ThreadID() != wantID || a.ProjectRoot() != wantRoot {
					t.Errorf("identity changed during concurrent access")
					return
				}
			}
		}()
	}
	wait.Wait()
}

func TestBashPinsRelativeHomeAcrossDirectoryChanges(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TED_HOME", "relative-home")
	a := NewAgent(zap.NewNop(), nil)
	result := a.runToolCall(ToolCall{Function: FunctionCall{
		Name:      "bash",
		Arguments: `{"command":"cd /; printf '%s' \"$TED_HOME\""}`,
	}})
	if result != a.home || !filepath.IsAbs(result) {
		t.Fatalf("tool home = %q, agent reads artifacts from %q", result, a.home)
	}
	if os.Getenv("TED_HOME") != "relative-home" {
		t.Fatal("tool environment changed host environment")
	}
}

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCloseDoesNotStartDaemonOrCreateStorage(t *testing.T) {
	home := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv("TED_HOME", home)
	a := &Agent{threadID: "thread", projectRoot: t.TempDir(), home: home}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("Close created browser storage: %v", err)
	}
}

func TestCloseUsesStableAgentIdentity(t *testing.T) {
	original := closeBrowserSessionIfRunning
	defer func() { closeBrowserSessionIfRunning = original }()

	var project, thread string
	closeBrowserSessionIfRunning = func(_ context.Context, gotProject, gotThread string) error {
		project, thread = gotProject, gotThread
		return nil
	}
	a := &Agent{threadID: "thread-123", projectRoot: "/project/root"}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if project != "/project/root" || thread != "thread-123" {
		t.Fatalf("close identity = project %q thread %q", project, thread)
	}
}

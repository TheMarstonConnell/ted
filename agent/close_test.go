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

	var home, project, thread string
	closeBrowserSessionIfRunning = func(_ context.Context, gotHome, gotProject, gotThread string) error {
		home, project, thread = gotHome, gotProject, gotThread
		return nil
	}
	a := &Agent{threadID: "thread-123", projectRoot: "/project/root", home: "/runtime/home"}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if home != "/runtime/home" || project != "/project/root" || thread != "thread-123" {
		t.Fatalf("close identity = home %q project %q thread %q", home, project, thread)
	}
}

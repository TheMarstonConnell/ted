package controlplane

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	sqliteDriver "modernc.org/sqlite"
)

func TestSQLiteFreshOpenCrashBeforeSchema(t *testing.T) {
	const environment = "TED_TEST_SQLITE_FRESH_CRASH"
	if dir := os.Getenv(environment); dir != "" {
		sqliteDriver.RegisterConnectionHook(func(sqliteDriver.ExecQuerierContext, string) error {
			guard, _, err := readGuard(filepath.Join(dir, "state.json"))
			if err != nil || guard == nil || guard.Phase != "pending" || guard.HasLegacy == nil || *guard.HasLegacy {
				os.Exit(74)
			}
			os.Exit(73)
			return nil
		})
		_, _ = NewService(dir, nil, nil)
		os.Exit(75)
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("exclusive-file store lock needs manual removal after crash on this platform")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteFreshOpenCrashBeforeSchema$")
	cmd.Env = append(os.Environ(), environment+"="+dir)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("crash checkpoint: %v\n%s", err, output)
	}
	s, err := NewService(dir, nil, nil)
	if err != nil {
		t.Fatalf("empty store did not recover from SQLite-open crash: %v", err)
	}
	defer s.Close(context.Background())
	if len(s.state.Projects) != 0 || len(s.state.Agents) != 0 {
		t.Fatal("fresh recovery imported unexpected data")
	}
	guard, _, err := readGuard(filepath.Join(dir, "state.json"))
	if err != nil || guard == nil || guard.Phase != "active" {
		t.Fatalf("recovered guard: %+v %v", guard, err)
	}
	if _, err := os.Stat(filepath.Join(dir, sqliteBackupName)); !os.IsNotExist(err) {
		t.Fatalf("invented a legacy backup: %v", err)
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerWritesAtDefaultLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.log")
	t.Setenv(LOG_FILE_VARIABLE, path)
	t.Setenv(LOG_LEVEL_VARIABLE, "")
	logger, err := newLogger()
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("request diagnostic")
	logger.Error("failure diagnostic")
	if err := logger.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"logger initialized", "request diagnostic", "failure diagnostic"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q in log", want)
		}
	}
}

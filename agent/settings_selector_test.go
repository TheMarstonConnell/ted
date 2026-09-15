package agent

import (
	"os"
	"testing"
)

func TestSettingsSelector(t *testing.T) {
	// No git executable or usable session directory is needed for selection.
	t.Setenv("PATH", "")
	home := t.TempDir()
	t.Setenv("TED_HOME", home)
	providers := []Provider{settingsProvider()}
	selector := NewSettingsSelector(providers)
	providers[0] = nil
	if got := selector.Settings(); got.Model != "test/a" || got.Effort != EffortMedium {
		t.Fatal(got)
	}
	if models := selector.ListModels(); len(models) != 3 || models[0].ID != "test/a" {
		t.Fatal(models)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("settings wrote session files", entries, err)
	}
}

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
	if _, err := selector.SetEffort(EffortLow); err != nil {
		t.Fatal(err)
	}
	change, err := selector.SetModel("test/b")
	if err != nil || !change.EffortAdjusted || change.After.Effort != EffortMedium {
		t.Fatal(change, err)
	}
	before := selector.Settings()
	if _, err := selector.SetModel("missing"); err == nil || selector.Settings() != before {
		t.Fatal("invalid model changed selection")
	}
	if _, err := selector.SetEffort("invalid"); err == nil || selector.Settings() != before {
		t.Fatal("invalid effort changed selection")
	}
	if _, err := selector.SetModel("test/plain"); err != nil || selector.Settings().Effort != "" {
		t.Fatal("plain model retained effort", err)
	}
	if _, err := selector.SetEffort(EffortHigh); err == nil {
		t.Fatal("plain model accepted effort")
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("settings wrote session files", entries, err)
	}
	if got := NewSettingsSelector(nil).Settings(); got != (Settings{}) {
		t.Fatal(got)
	}
}

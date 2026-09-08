package main

import (
	"github.com/TheMarstonConnell/ted/agent"
	"strings"
	"testing"
)

func TestTUISettingsFlags(t *testing.T) {
	cmd := newTUICommand()
	if err := cmd.ParseFlags([]string{"--effort", "high", "--model", "codex/gpt-5.6-terra", "--prompt", "hello"}); err != nil {
		t.Fatal(err)
	}
	for flag, want := range map[string]string{"model": "codex/gpt-5.6-terra", "effort": "high", "prompt": "hello"} {
		if got, err := cmd.Flags().GetString(flag); err != nil || got != want {
			t.Fatalf("%s = %q, err = %v", flag, got, err)
		}
	}
}

func TestApplyTUISettings(t *testing.T) {
	for _, tt := range []struct{ name, model, effort, errorFlag string }{
		{name: "defaults"},
		{name: "model only", model: "codex/gpt-5.6-terra"},
		{name: "effort only", effort: "high"},
		{name: "both", model: "codex/gpt-5.6-terra", effort: "low"},
		{name: "unknown model", model: "missing/model", errorFlag: "--model"},
		{name: "invalid effort", effort: "extreme", errorFlag: "--effort"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := agent.NewAgent(nil, []agent.Provider{agent.NewCodexProvider(nil)})
			want := a.Settings()
			if tt.model != "" {
				want.Model = tt.model
			}
			if tt.effort != "" {
				want.Effort = agent.Effort(tt.effort)
			}
			err := applyTUISettings(a, tt.model, tt.effort)
			if tt.errorFlag != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errorFlag) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := a.Settings(); got != want {
				t.Fatalf("settings = %+v, want %+v", got, want)
			}
		})
	}
}

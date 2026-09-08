package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/spf13/cobra"
)

func TestCatalogCommands(t *testing.T) {
	load := func() (*agent.Agent, error) {
		return agent.NewAgent(nil, []agent.Provider{agent.NewOpenRouterProvider("")}), nil
	}
	for _, tt := range []struct {
		name    string
		command func(catalogAgentLoader) *cobra.Command
		args    []string
		want    string
		wantErr bool
	}{
		{"models", newModelsCommand, nil, "openrouter/meta/muse-spark-1.3-contributor\nopenrouter/deepseek/deepseek-v4-flash-0731\nopenrouter/openai/gpt-5.6-luna\nopenrouter/z-ai/glm-5.3-flash\n", false},
		{"filtered models", newModelsCommand, []string{"--provider", "openrouter"}, "openrouter/meta/muse-spark-1.3-contributor\nopenrouter/deepseek/deepseek-v4-flash-0731\nopenrouter/openai/gpt-5.6-luna\nopenrouter/z-ai/glm-5.3-flash\n", false},
		{"unknown provider", newModelsCommand, []string{"--provider", "missing"}, "", true},
		{"unconfigured provider", newModelsCommand, []string{"--provider", "codex"}, "", true},
		{"providers", newProvidersCommand, nil, "openrouter\n", false},
		{"providers arguments", newProvidersCommand, []string{"unexpected"}, "", true},
		{"efforts", newEffortsCommand, []string{"openrouter/openai/gpt-5.6-luna"}, "low\nmedium\nhigh\n", false},
		{"unsupported", newEffortsCommand, []string{"openrouter/meta/muse-spark-1.3-contributor"}, "Model \"openrouter/meta/muse-spark-1.3-contributor\" does not expose configurable effort.\n", false},
		{"glm flash unsupported effort", newEffortsCommand, []string{"openrouter/z-ai/glm-5.3-flash"}, "Model \"openrouter/z-ai/glm-5.3-flash\" does not expose configurable effort.\n", false},
		{"unknown", newEffortsCommand, []string{"missing/model"}, "", true},
		{"missing id", newEffortsCommand, nil, "", true},
		{"extra id", newEffortsCommand, []string{"a", "b"}, "", true},
		{"models arguments", newModelsCommand, []string{"unexpected"}, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.command(load)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&bytes.Buffer{})
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v", err)
			}
			if out.String() != tt.want {
				t.Fatalf("output = %q, want %q", out.String(), tt.want)
			}
		})
	}
}

func TestCatalogLoadErrors(t *testing.T) {
	want := errors.New("no providers available")
	for _, cmd := range []*cobra.Command{
		newProvidersCommand(func() (*agent.Agent, error) { return nil, want }),
		newModelsCommand(func() (*agent.Agent, error) { return nil, want }),
		newEffortsCommand(func() (*agent.Agent, error) { return nil, want }),
	} {
		if err := cmd.RunE(cmd, []string{"any/model"}); !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestCatalogCommandsRegistered(t *testing.T) {
	for _, name := range []string{"models", "efforts", "providers"} {
		cmd, _, err := newRootCommand().Find([]string{name})
		if err != nil || !strings.HasPrefix(cmd.Use, name) {
			t.Fatalf("command %s not registered", name)
		}
	}
}

func TestCatalogMultipleProviders(t *testing.T) {
	load := func() (*agent.Agent, error) {
		return agent.NewAgent(nil, []agent.Provider{agent.NewCodexProvider(nil), agent.NewOpenRouterProvider("")}), nil
	}
	cmd := newProvidersCommand(load)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "codex\nopenrouter\n" {
		t.Fatalf("providers = %q", out.String())
	}
	for _, provider := range []string{"codex", "openrouter", ""} {
		out.Reset()
		cmd = newModelsCommand(load)
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--provider", provider})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		a, _ := load()
		var want strings.Builder
		for _, model := range a.ListModels() {
			if provider == "" || model.Provider == provider {
				want.WriteString(model.ID + "\n")
			}
		}
		if out.String() != want.String() {
			t.Fatalf("models for %q = %q, want %q", provider, out.String(), want.String())
		}
	}
}

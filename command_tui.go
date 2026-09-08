package main

import (
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/TheMarstonConnell/ted/agent"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

const openRouterKeyVariable = "OPENROUTER_API_KEY"

func newTUICommand() *cobra.Command {
	var prompt string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Start the interactive terminal user interface",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(prompt)
		},
	}
	cmd.Flags().StringVar(&prompt, "prompt", "", "Send an initial user prompt when the TUI starts")
	return cmd
}

func runTUI(prompt string) error {
	logger, err := newLogger()
	if err != nil {
		return fmt.Errorf("could not build logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not load .env file: %w", err)
	}

	var providers []agent.Provider

	codexAuthPath, err := agent.DefaultCodexAuthPath()
	if err != nil {
		return err
	}
	codexAuth := agent.NewCodexAuthStore(codexAuthPath)
	if codexAuth.Exists() {
		providers = append(providers, agent.NewCodexProvider(codexAuth))
	} else {
		logger.Debug("no codex login found", zap.String("path", codexAuthPath))
	}

	if openRouterKey := os.Getenv(openRouterKeyVariable); openRouterKey != "" {
		providers = append(providers, agent.NewOpenRouterProvider(openRouterKey))
	} else {
		logger.Debug("no openrouter key found", zap.String("variable", openRouterKeyVariable))
	}

	if len(providers) == 0 {
		return fmt.Errorf("no providers available: log in with `codex login` or set %s", openRouterKeyVariable)
	}

	instance := agent.NewAgent(logger, providers)

	m := initialModel(instance)
	m.initialPrompt = prompt
	p := tea.NewProgram(m)
	instance.SetOutput(func(res agent.AgentResponse) {
		p.Send(res)
	})

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("could not run tea program: %w", err)
	}
	return nil
}

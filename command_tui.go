package main

import (
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

const openRouterKeyVariable = "OPENROUTER_API_KEY"

func newTUICommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Start the interactive terminal user interface",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI()
		},
	}
}

func runTUI() error {
	logger, err := newLogger()
	if err != nil {
		return fmt.Errorf("could not build logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not load .env file: %w", err)
	}

	var providers []Provider

	codexAuthPath, err := DefaultCodexAuthPath()
	if err != nil {
		return err
	}
	codexAuth := NewCodexAuthStore(codexAuthPath)
	if codexAuth.Exists() {
		providers = append(providers, NewCodexProvider(codexAuth))
	} else {
		logger.Debug("no codex login found", zap.String("path", codexAuthPath))
	}

	if openRouterKey := os.Getenv(openRouterKeyVariable); openRouterKey != "" {
		providers = append(providers, NewOpenRouterProvider(openRouterKey))
	} else {
		logger.Debug("no openrouter key found", zap.String("variable", openRouterKeyVariable))
	}

	if len(providers) == 0 {
		return fmt.Errorf("no providers available: log in with `codex login` or set %s", openRouterKeyVariable)
	}

	agent := NewAgent(logger, providers)

	p := tea.NewProgram(initialModel(agent))
	agent.SetOutput(func(res AgentResponse) {
		p.Send(res)
	})

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("could not run tea program: %w", err)
	}
	return nil
}

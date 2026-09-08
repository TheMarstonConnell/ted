package main

import (
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
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

	openRouterKey := os.Getenv(openRouterKeyVariable)
	if openRouterKey == "" {
		return fmt.Errorf("no openrouter key found in %s", openRouterKeyVariable)
	}

	agent := NewAgent(logger, openRouterKey)

	p := tea.NewProgram(initialModel(agent))
	agent.SetOutput(func(res AgentResponse) {
		p.Send(res)
	})

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("could not run tea program: %w", err)
	}
	return nil
}

package main

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/TheMarstonConnell/ted/agent"
	"github.com/spf13/cobra"
)

const openRouterKeyVariable = "OPENROUTER_API_KEY"

func newTUICommand() *cobra.Command {
	var prompt, modelID, effort string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Start the interactive terminal user interface",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(prompt, modelID, effort)
		},
	}
	cmd.Flags().StringVar(&prompt, "prompt", "", "Send an initial user prompt when the TUI starts")
	cmd.Flags().StringVar(&modelID, "model", "", "Select an initial model (provider/model-id)")
	cmd.Flags().StringVar(&effort, "effort", "", "Set supported reasoning effort (low, medium, high)")
	return cmd
}

func runTUI(prompt, modelID, effort string) error {
	logger, err := newLogger()
	if err != nil {
		return fmt.Errorf("could not build logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	providers, err := configuredProviders(logger)
	if err != nil {
		return err
	}

	instance := agent.NewAgent(logger, providers)
	defer func() { _ = instance.Close() }()

	if err := applyTUISettings(instance, modelID, effort); err != nil {
		return err
	}
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

// Select the model first so effort is validated against the startup model.
func applyTUISettings(instance *agent.Agent, modelID, effort string) error {
	if modelID != "" {
		if _, err := instance.SetModel(modelID); err != nil {
			return fmt.Errorf("invalid --model: %w", err)
		}
	}
	if effort != "" {
		if _, err := instance.SetEffort(agent.Effort(effort)); err != nil {
			return fmt.Errorf("invalid --effort: %w", err)
		}
	}
	return nil
}

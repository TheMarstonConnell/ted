package main

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/TheMarstonConnell/ted/agent"
	"github.com/spf13/cobra"
)

const openRouterKeyVariable = "OPENROUTER_API_KEY"

func newTUICommand() *cobra.Command {
	cmd := &cobra.Command{Use: "tui", Short: "Start the interactive terminal user interface", Args: cobra.NoArgs}
	configureTUICommand(cmd)
	return cmd
}

func configureTUICommand(cmd *cobra.Command) {
	var prompt, modelID, effort, resume string
	var continueSession bool
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runTUISession(prompt, modelID, effort, resume, continueSession)
	}
	cmd.Flags().StringVar(&prompt, "prompt", "", "Send an initial user prompt when the TUI starts")
	cmd.Flags().StringVar(&modelID, "model", "", "Select a model (provider/model-id); overrides saved settings")
	cmd.Flags().StringVar(&effort, "effort", "", "Set supported reasoning effort (low, medium, high)")
	cmd.Flags().StringVar(&resume, "resume", "", "Resume a saved session by ID")
	cmd.Flags().BoolVar(&continueSession, "continue", false, "Resume the latest session for this project")
	cmd.MarkFlagsMutuallyExclusive("resume", "continue")
}

func runTUI(prompt, modelID, effort string) error {
	return runTUISession(prompt, modelID, effort, "", false)
}

func runTUISession(prompt, modelID, effort, resume string, continueSession bool) error {
	if continueSession {
		var err error
		resume, err = agent.LatestSessionID()
		if err != nil {
			return err
		}
	}

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

	if resume != "" {
		if err := instance.RestoreSession(resume, modelID, agent.Effort(effort)); err != nil {
			return err
		}
	} else if err := applyTUISettings(instance, modelID, effort); err != nil {
		return err
	}
	if err := instance.EnablePersistence(); err != nil {
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

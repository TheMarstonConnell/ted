package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/remote"
	"github.com/spf13/cobra"
)

const openRouterKeyVariable = "OPENROUTER_API_KEY"

func newTUICommand() *cobra.Command {
	cmd := &cobra.Command{Use: "tui", Short: "Start the interactive terminal user interface", Args: cobra.NoArgs}
	configureTUICommand(cmd)
	return cmd
}
func configureTUICommand(cmd *cobra.Command) {
	var prompt, modelID, effort, resume, server string
	var continueSession bool
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("server") && strings.TrimSpace(server) == "" {
			return fmt.Errorf("--server requires a URL")
		}
		return runRemoteTUISession(cmd.Context(), server, prompt, modelID, effort, resume, continueSession)
	}
	cmd.Flags().StringVar(&server, "server", "", "Connect to an existing API server (never starts a local server)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Send an initial user prompt when the TUI starts")
	cmd.Flags().StringVar(&modelID, "model", "", "Select a model (provider/model-id); overrides saved settings")
	cmd.Flags().StringVar(&effort, "effort", "", "Set supported reasoning effort (low, medium, high)")
	cmd.Flags().StringVar(&resume, "resume", "", "Resume a server agent by ID")
	cmd.Flags().BoolVar(&continueSession, "continue", false, "Resume the latest server agent for this project")
	cmd.MarkFlagsMutuallyExclusive("resume", "continue")
}
func runTUI(prompt, modelID, effort string) error {
	return runTUISession(prompt, modelID, effort, "", false)
}
func runTUISession(prompt, modelID, effort, resume string, continueSession bool) error {
	return runRemoteTUISession(context.Background(), "", prompt, modelID, effort, resume, continueSession)
}
func runRemoteTUISession(ctx context.Context, server, prompt, modelID, effort, resume string, continueSession bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	base, owner, err := resolveTUIServer(ctx, server)
	if err != nil {
		return err
	}
	defer func() { cancel(); owner.Close() }()
	client := remote.New(base)
	instance, err := prepareRemoteAgent(ctx, client, modelID, effort, resume, continueSession)
	if err != nil {
		return err
	}
	m := initialModel(instance)
	// Creation is deliberately idle. Only initialPromptMsg submits this prompt,
	// after the selected agent's snapshot cursor has been subscribed.
	m.initialPrompt = prompt
	p := tea.NewProgram(m, tea.WithContext(ctx))
	if err := instance.Subscribe(ctx, func(update remote.Update) {
		if update.Output != nil {
			p.Send(*update.Output)
		}
		if update.Busy != nil {
			p.Send(remoteStateMsg(*update.Busy))
		}
		if update.Err != nil {
			p.Send(agent.AgentResponse{ResponseType: "status", Content: update.Err.Error()})
		}
	}); err != nil {
		return err
	}
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("could not run tea program: %w", err)
	}
	return nil
}

func prepareRemoteAgent(ctx context.Context, c *remote.Client, modelID, effort, resume string, latest bool) (*remote.Agent, error) {
	models, err := c.Models(ctx)
	if err != nil {
		return nil, err
	}
	var snapshot remote.Snapshot
	var project remote.Project
	if resume != "" {
		snapshot, err = c.GetAgent(ctx, resume)
		if err != nil {
			return nil, fmt.Errorf("resume server agent %q: %w", resume, err)
		}
		project, err = c.Project(ctx, snapshot.ProjectID)
		if err != nil {
			return nil, err
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		if canonical, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = canonical
		}
		projects, err := c.Projects(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			if filepath.Clean(p.Root) == filepath.Clean(cwd) {
				project = p
				break
			}
		}
		if project.ID == "" {
			if latest {
				return nil, fmt.Errorf("no server sessions for directory %s", cwd)
			}
			if len(models) == 0 {
				return nil, fmt.Errorf("server has no models available")
			}
			defaults := remote.Settings{Model: models[0].ID, Effort: string(models[0].DefaultEffort)}
			project, err = c.CreateProject(ctx, filepath.Base(cwd), cwd, defaults)
			if err != nil {
				return nil, fmt.Errorf("create project (root must exist on server filesystem): %w", err)
			}
		}
		if latest {
			agents, err := c.Agents(ctx, project.ID)
			if err != nil {
				return nil, err
			}
			for _, a := range agents {
				if a.Settled {
					continue
				}
				if snapshot.ID == "" || a.UpdatedAt.After(snapshot.UpdatedAt) {
					snapshot = a
				}
			}
			if snapshot.ID == "" {
				return nil, fmt.Errorf("no server sessions for project %s", project.Name)
			}
			snapshot, err = c.GetAgent(ctx, snapshot.ID)
			if err != nil {
				return nil, err
			}
		} else {
			snapshot, err = c.CreateAgent(ctx, project.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	instance := remote.NewAgent(ctx, c, snapshot, project.Root, models)
	if err := applyTUISettings(instance, modelID, effort); err != nil {
		return nil, err
	}
	return instance, nil
}

// Select the model first so effort is validated against the startup model.
func applyTUISettings(instance interface {
	SetModel(string) (agent.SettingsChange, error)
	SetEffort(agent.Effort) (agent.SettingsChange, error)
}, modelID, effort string) error {
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

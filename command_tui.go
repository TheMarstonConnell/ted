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
	var prompt, modelID, effort, resume, server, workspaceMode, baseBranch, cwd, parentAgentID string
	var continueSession bool
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("server") && strings.TrimSpace(server) == "" {
			return fmt.Errorf("--server requires a URL")
		}
		workspace, err := tuiWorkspaceSelection(cmd, workspaceMode, baseBranch, resume, continueSession)
		if err != nil {
			return err
		}
		options := tuiStartupOptions{CWD: cwd, ParentAgentID: parentAgentID, Workspace: workspace}
		for _, flag := range []string{"cwd", "parent-agent"} {
			value, _ := cmd.Flags().GetString(flag)
			if cmd.Flags().Changed(flag) && strings.TrimSpace(value) == "" {
				return fmt.Errorf("--%s requires a nonempty value", flag)
			}
		}
		if err := options.validate(resume, continueSession); err != nil {
			return err
		}
		return runRemoteTUISession(cmd.Context(), server, prompt, modelID, effort, resume, continueSession, options)
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "Start in this directory (absolute paths refer to the server filesystem)")
	cmd.Flags().StringVar(&parentAgentID, "parent-agent", "", "Create a child of this server agent; inherits its established worktree unless overridden")
	cmd.Flags().StringVar(&server, "server", "", "Connect to an existing API server (never starts a local server)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Send an initial user prompt when the TUI starts")
	cmd.Flags().StringVar(&modelID, "model", "", "Select a model (provider/model-id); overrides saved settings")
	cmd.Flags().StringVar(&effort, "effort", "", "Set supported reasoning effort (low, medium, high)")
	cmd.Flags().StringVar(&resume, "resume", "", "Resume a server agent by ID")
	cmd.Flags().BoolVar(&continueSession, "continue", false, "Resume the latest server agent for this project")
	cmd.Flags().StringVar(&workspaceMode, "workspace", "", "Workspace for a new thread: current_checkout or worktree (defaults to project setting)")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", "Remote base branch for a new worktree (for example origin/main)")
	cmd.MarkFlagsMutuallyExclusive("resume", "continue")
}

func tuiWorkspaceSelection(cmd *cobra.Command, mode, baseBranch, resume string, latest bool) (*remote.WorkspaceSelection, error) {
	workspaceChanged := cmd.Flags().Changed("workspace")
	baseChanged := cmd.Flags().Changed("base-branch")
	if !workspaceChanged && !baseChanged {
		return nil, nil // omitting workspace from creation inherits project defaults
	}
	if resume != "" || latest {
		return nil, fmt.Errorf("--workspace and --base-branch cannot be used with --resume or --continue; resumed workspaces are locked after the first message")
	}
	mode = strings.TrimSpace(mode)
	baseBranch = strings.TrimSpace(baseBranch)
	if !workspaceChanged && baseChanged {
		mode = "worktree"
	}
	if mode != "current_checkout" && mode != "worktree" {
		return nil, fmt.Errorf("--workspace must be current_checkout or worktree")
	}
	if mode == "current_checkout" && baseChanged {
		return nil, fmt.Errorf("--base-branch requires --workspace worktree")
	}
	if baseChanged && baseBranch == "" {
		return nil, fmt.Errorf("--base-branch requires a remote branch such as origin/main")
	}
	return &remote.WorkspaceSelection{Mode: mode, BaseBranch: baseBranch}, nil
}
func runTUI(prompt, modelID, effort string) error {
	return runTUISession(prompt, modelID, effort, "", false)
}
func runTUISession(prompt, modelID, effort, resume string, continueSession bool) error {
	return runRemoteTUISession(context.Background(), "", prompt, modelID, effort, resume, continueSession)
}
func runRemoteTUISession(ctx context.Context, server, prompt, modelID, effort, resume string, continueSession bool, options ...tuiStartupOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	base, owner, err := resolveTUIServer(ctx, server)
	if err != nil {
		return err
	}
	defer func() { cancel(); owner.Close() }()
	client := remote.New(base)
	instance, err := prepareRemoteAgent(ctx, client, modelID, effort, resume, continueSession, options...)
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

// Startup options select a project/location without changing the process cwd.
type tuiStartupOptions struct {
	CWD           string
	ParentAgentID string
	Workspace     *remote.WorkspaceSelection
}

func (o tuiStartupOptions) validate(resume string, latest bool) error {
	if o.ParentAgentID != "" && (resume != "" || latest) {
		return fmt.Errorf("--parent-agent is creation-only and cannot be used with --resume or --continue")
	}
	if o.CWD != "" && resume != "" {
		return fmt.Errorf("--cwd cannot be used with --resume; resumed agents reuse their recorded workspace")
	}
	if o.Workspace != nil {
		if resume != "" || latest {
			return fmt.Errorf("workspace overrides cannot be used with --resume or --continue; resumed workspaces are locked after the first message")
		}
		if o.Workspace.Mode != "current_checkout" && o.Workspace.Mode != "worktree" {
			return fmt.Errorf("workspace mode must be current_checkout or worktree")
		}
	}
	return nil
}

func prepareRemoteAgent(ctx context.Context, c *remote.Client, modelID, effort, resume string, latest bool, options ...tuiStartupOptions) (*remote.Agent, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("only one startup options value may be specified")
	}
	var opts tuiStartupOptions
	if len(options) == 1 {
		opts = options[0]
	}
	if err := opts.validate(resume, latest); err != nil {
		return nil, err
	}
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
		var parent remote.Snapshot
		if opts.ParentAgentID != "" {
			parent, err = c.GetAgent(ctx, opts.ParentAgentID)
			if err != nil {
				return nil, fmt.Errorf("parent agent %q: %w", opts.ParentAgentID, err)
			}
		}
		var directory string
		if opts.CWD == "" && opts.ParentAgentID != "" {
			project, err = c.Project(ctx, parent.ProjectID)
		} else {
			project, directory, err = tuiProjectForDirectory(ctx, c, opts.CWD, latest, models)
		}
		if err != nil {
			return nil, err
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
			snapshot, err = c.CreateAgentWithOptions(ctx, project.ID, remote.CreateAgentOptions{ParentAgentID: opts.ParentAgentID, WorkingDirectory: directory, Workspace: opts.Workspace})
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

// Managed worktrees take precedence over old duplicate projects rooted at the
// worktree path, and keep their original project identity. Do not use Git's
// common-dir alone: only server-recorded worktrees have a durable Ted owner.
func tuiProjectForDirectory(ctx context.Context, c *remote.Client, explicit string, latest bool, models []agent.ModelInfo) (remote.Project, string, error) {
	cwd := explicit
	var err error
	if cwd == "" {
		cwd, err = os.Getwd()
	}
	if err != nil {
		return remote.Project{}, "", err
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return remote.Project{}, "", err
	}
	// A remote server's absolute directory need not exist on the client.
	cwd = canonicalTUIPath(cwd)
	projects, err := c.Projects(ctx)
	if err != nil {
		return remote.Project{}, "", err
	}
	agents, err := c.Agents(ctx, "")
	if err != nil {
		return remote.Project{}, "", err
	}
	for _, a := range agents {
		if a.Workspace.Mode != "worktree" || a.Workspace.Path == "" || canonicalTUIPath(a.Workspace.Path) != filepath.Clean(cwd) {
			continue
		}
		for _, p := range projects {
			if p.ID == a.ProjectID {
				return p, cwd, nil
			}
		}
	}
	for _, p := range projects {
		if filepath.Clean(p.Root) == filepath.Clean(cwd) {
			directory := ""
			if explicit != "" {
				directory = cwd
			}
			return p, directory, nil
		}
	}
	if latest {
		return remote.Project{}, "", fmt.Errorf("no server sessions for directory %s", cwd)
	}
	if len(models) == 0 {
		return remote.Project{}, "", fmt.Errorf("server has no models available")
	}
	defaults := remote.Settings{Model: models[0].ID, Effort: string(models[0].DefaultEffort)}
	project, err := c.CreateProject(ctx, filepath.Base(cwd), cwd, defaults)
	if err != nil {
		return remote.Project{}, "", fmt.Errorf("create project (root must exist on server filesystem): %w", err)
	}
	return project, cwd, nil
}

// Best-effort local canonicalization keeps local symlink aliases identical,
// while leaving server-only paths intact when the client cannot inspect them.
func canonicalTUIPath(path string) string {
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(canonical)
	}
	return filepath.Clean(path)
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

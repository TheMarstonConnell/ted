package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/TheMarstonConnell/ted/agent"
)

// Agent is a synchronized rendering/settings adapter, not an execution engine.
// The server queues Turn input and owns tools, transcripts and credentials.
type Agent struct {
	client   *Client
	ctx      context.Context
	mu       sync.RWMutex
	snapshot Snapshot
	id       string
	root     string
	models   []agent.ModelInfo
	cursor   uint64 // consumed event cursor, never advanced by inventory snapshots
}

func NewAgent(ctx context.Context, c *Client, s Snapshot, root string, models []agent.ModelInfo) *Agent {
	return &Agent{client: c, ctx: ctx, snapshot: s, id: s.ID, root: root, models: models, cursor: s.Cursor}
}
func (a *Agent) AcceptsQueuedInput() bool { return true }

// WorkingDir reports the execution directory from the latest server snapshot.
// It deliberately performs no I/O because render and update paths call it as an
// infallible accessor; asynchronous websocket and GitBranch refreshes update the
// snapshot when worktree setup starts.
func (a *Agent) WorkingDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.snapshot.Workspace.Path != "" {
		return a.snapshot.Workspace.Path
	}
	return a.root
}
func (a *Agent) Ready() bool { a.mu.RLock(); defer a.mu.RUnlock(); return a.snapshot.State == "idle" }
func (a *Agent) Settings() agent.Settings {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return typedSettings(a.snapshot.Settings)
}
func typedSettings(s Settings) agent.Settings {
	provider, _, _ := strings.Cut(s.Model, "/")
	return agent.Settings{Model: s.Model, Provider: provider, Effort: agent.Effort(s.Effort)}
}
func (a *Agent) Messages() []agent.Message {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]agent.Message(nil), a.snapshot.Messages...)
}
func (a *Agent) ListModels() []agent.ModelInfo {
	result := append([]agent.ModelInfo(nil), a.models...)
	for i := range result {
		result[i].Efforts = append([]agent.Effort(nil), result[i].Efforts...)
	}
	return result
}
func (a *Agent) ListEfforts() []agent.Effort {
	settings := a.Settings()
	for _, m := range a.models {
		if m.ID == settings.Model {
			return append([]agent.Effort(nil), m.Efforts...)
		}
	}
	return nil
}
func (a *Agent) ContextUsage() agent.ContextUsage {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := a.snapshot.ContextUsage
	result.Model = a.snapshot.Settings.Model
	for _, m := range a.models {
		if m.ID == result.Model {
			result.ContextWindow = m.ContextWindow
		}
	}
	return result
}
func (a *Agent) path() string { return agentPath(a.id) }
func (a *Agent) updateSnapshot(s Snapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.Cursor < a.snapshot.Cursor {
		return
	}
	s.Messages = a.snapshot.Messages // only consumed conversation events advance history
	a.snapshot = s
}
func (a *Agent) setSettings(patch map[string]string) (agent.SettingsChange, error) {
	before := a.Settings()
	var s Snapshot
	if err := a.client.request(a.ctx, "PATCH", a.path()+"/settings", patch, &s, ""); err != nil {
		return agent.SettingsChange{}, err
	}
	a.updateSnapshot(s)
	after := typedSettings(s.Settings)
	return agent.SettingsChange{Before: before, After: after, EffortAdjusted: before.Effort != after.Effort}, nil
}
func (a *Agent) SetModel(id string) (agent.SettingsChange, error) {
	return a.setSettings(map[string]string{"model": id})
}
func (a *Agent) SetEffort(e agent.Effort) (agent.SettingsChange, error) {
	return a.setSettings(map[string]string{"effort": string(e)})
}
func (a *Agent) Turn(text string) error {
	err := a.client.request(a.ctx, "POST", a.path()+"/messages", map[string]string{"text": text}, nil, newKey())
	return err
}
func (a *Agent) action(method, suffix string, body any) error {
	var s Snapshot
	if err := a.client.request(a.ctx, method, a.path()+suffix, body, &s, ""); err != nil {
		return err
	}
	a.updateSnapshot(s)
	return nil
}
func (a *Agent) Stop() error {
	// Fetch current queue rather than using inventory (which deliberately omits it).
	s, err := a.client.GetAgent(a.ctx, a.id)
	if err != nil {
		return err
	}
	for _, q := range s.Queue {
		if q.Status == "running" {
			return a.action("POST", "/stop", map[string]string{"turn_id": q.ID})
		}
	}
	return fmt.Errorf("no running turn to stop")
}
func (a *Agent) Settle() error   { return a.action("PATCH", "", map[string]bool{"settled": true}) }
func (a *Agent) Unsettle() error { return a.action("PATCH", "", map[string]bool{"settled": false}) }
func (a *Agent) Continue() error {
	return a.action("POST", "/continue", nil)
}

// Update carries completed outputs (including full tool results), not deltas.
type Update struct {
	Output *agent.AgentResponse
	Busy   *bool
	Err    error
}

func (a *Agent) consume(e Event, notify func(Update)) error {
	a.mu.Lock()
	if e.AgentID != a.snapshot.ID || e.Cursor <= a.cursor {
		a.mu.Unlock()
		return nil
	}
	var output *agent.AgentResponse
	var busy *bool
	switch e.Type {
	case "agent.updated":
		var s Snapshot
		if err := json.Unmarshal(e.Data, &s); err != nil {
			a.mu.Unlock()
			return err
		}
		if e.Cursor >= a.snapshot.Cursor {
			s.Cursor = e.Cursor
			s.Messages, s.Queue = a.snapshot.Messages, a.snapshot.Queue
			a.snapshot = s
		}
		b := a.snapshot.State != "idle"
		busy = &b
	case "output":
		output = new(agent.AgentResponse)
		if err := json.Unmarshal(e.Data, output); err != nil {
			a.mu.Unlock()
			return err
		}
	case "conversation":
		var messages []agent.Message
		if err := json.Unmarshal(e.Data, &messages); err != nil {
			a.mu.Unlock()
			return err
		}
		a.snapshot.Messages = append(a.snapshot.Messages, messages...)
	case "message.queued":
		var q QueuedMessage
		if err := json.Unmarshal(e.Data, &q); err != nil {
			a.mu.Unlock()
			return err
		}
		if q.Text != "" {
			output = &agent.AgentResponse{ResponseType: "user", Content: q.Text}
		}

	case "turn.failed", "turn.interrupted", "turn.cancelled":
		var q QueuedMessage
		_ = json.Unmarshal(e.Data, &q)
		content := q.Error
		if content == "" {
			content = e.Type
		}
		output = &agent.AgentResponse{ResponseType: "status", Content: content}
	}
	a.cursor = e.Cursor
	a.mu.Unlock()
	if output != nil || busy != nil {
		notify(Update{Output: output, Busy: busy})
	}
	return nil
}

// GitBranch reads live metadata on the server, never the terminal filesystem.
// For a linked worktree it reports the generated creation branch recorded in
// workspace metadata; later manual branch switches are outside this feature.
func (a *Agent) GitBranch() (string, error) {
	a.mu.RLock()
	w := a.snapshot.Workspace
	projectID := a.snapshot.ProjectID
	a.mu.RUnlock()
	if w.Mode == "worktree" {
		if w.Branch == "" {
			s, err := a.client.GetAgent(a.ctx, a.id)
			if err != nil {
				return "", err
			}
			a.updateSnapshot(s)
			w = s.Workspace
		}
		return w.Branch, nil
	}
	p, err := a.client.Project(a.ctx, projectID)
	return p.GitBranch, err
}

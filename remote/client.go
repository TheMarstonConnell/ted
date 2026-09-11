// Package remote implements the control plane wire client. It never loads
// credentials, constructs a local agent, or owns server-side agent lifetimes.
package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

type Settings struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// WorkspaceSelection is a draft agent's requested execution location. An
// omitted selection on create means the project's workspace defaults apply.
type WorkspaceSelection struct {
	Mode       string `json:"mode"`
	BaseBranch string `json:"base_branch,omitempty"`
}

// Workspace is the server-owned, durable workspace state for an agent.
type Workspace struct {
	WorkspaceSelection
	Locked bool   `json:"locked"`
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
	// Branch is the generated worktree creation branch, not live Git status
	// after arbitrary branch changes inside the workspace.
	Branch     string `json:"branch,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	Error      string `json:"error,omitempty"`
}

type Project struct {
	WorkspaceDefaults WorkspaceSelection `json:"workspace_defaults"`
	GitBranch         string             `json:"git_branch,omitempty"`
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	Root              string             `json:"root"`
	Defaults          Settings           `json:"defaults"`
}
type QueuedMessage struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
type Snapshot struct {
	Workspace    Workspace          `json:"workspace"`
	Title        string             `json:"title"`
	ContextUsage agent.ContextUsage `json:"context_usage"`
	ID           string             `json:"id"`
	ProjectID    string             `json:"project_id"`
	Settings     Settings           `json:"settings"`
	State        string             `json:"state"`
	Settled      bool               `json:"settled"`
	Held         bool               `json:"held"`
	Queue        []QueuedMessage    `json:"queue"`
	Messages     []agent.Message    `json:"messages"`
	Cursor       uint64             `json:"cursor"`
	UpdatedAt    time.Time          `json:"updated_at"`
}
type Event struct {
	AgentID string          `json:"agent_id"`
	Cursor  uint64          `json:"cursor"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
}
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("server: %s (HTTP %d, %s)", e.Message, e.Status, e.Code)
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(base string) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 15 * time.Second}}
}
func newKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (c *Client) request(ctx context.Context, method, path string, body, out any, key string) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var envelope struct {
			Error APIError `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&envelope); err != nil {
			return fmt.Errorf("server HTTP %d", res.StatusCode)
		}
		envelope.Error.Status = res.StatusCode
		return &envelope.Error
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}
func agentPath(id string) string { return "/v1/agents/" + url.PathEscape(id) }
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var result []Project
	err := c.request(ctx, "GET", "/v1/projects", nil, &result, "")
	return result, err
}
func (c *Client) CreateProject(ctx context.Context, name, root string, defaults Settings, workspace ...WorkspaceSelection) (Project, error) {
	if len(workspace) > 1 {
		return Project{}, fmt.Errorf("only one workspace default may be specified")
	}
	body := map[string]any{"name": name, "root": root, "defaults": defaults}
	if len(workspace) > 0 {
		body["workspace_defaults"] = workspace[0]
	}
	var result Project
	err := c.request(ctx, "POST", "/v1/projects", body, &result, "")
	return result, err
}
func (c *Client) Project(ctx context.Context, id string) (Project, error) {
	var result Project
	err := c.request(ctx, "GET", "/v1/projects/"+url.PathEscape(id), nil, &result, "")
	return result, err
}
func (c *Client) Agents(ctx context.Context, project string) ([]Snapshot, error) {
	var result []Snapshot
	path := "/v1/agents?include_settled=true"
	if project != "" {
		path += "&project_id=" + url.QueryEscape(project)
	}
	err := c.request(ctx, "GET", path, nil, &result, "")
	return result, err
}
func (c *Client) GetAgent(ctx context.Context, id string) (Snapshot, error) {
	var result Snapshot
	err := c.request(ctx, "GET", agentPath(id), nil, &result, "")
	return result, err
}
func (c *Client) CreateAgent(ctx context.Context, project string, workspace ...WorkspaceSelection) (Snapshot, error) {
	if len(workspace) > 1 {
		return Snapshot{}, fmt.Errorf("only one workspace selection may be specified")
	}
	body := map[string]any{"project_id": project}
	if len(workspace) > 0 {
		body["workspace"] = workspace[0]
	}
	var result Snapshot
	err := c.request(ctx, "POST", "/v1/agents", body, &result, newKey())
	return result, err
}

// PatchAgentWorkspace changes an unlocked draft agent's workspace selection.
// Creation callers should normally pass the selection to CreateAgent so there
// is no intermediate agent with a different selection.
func (c *Client) PatchAgentWorkspace(ctx context.Context, id string, workspace WorkspaceSelection) (Snapshot, error) {
	var result Snapshot
	err := c.request(ctx, "PATCH", agentPath(id)+"/workspace", workspace, &result, "")
	return result, err
}
func (c *Client) Models(ctx context.Context) ([]agent.ModelInfo, error) {
	var wire []struct {
		ID            string         `json:"id"`
		Name          string         `json:"name"`
		Provider      string         `json:"provider"`
		ContextWindow int64          `json:"context_window"`
		Efforts       []agent.Effort `json:"efforts"`
		DefaultEffort agent.Effort   `json:"default_effort"`
	}
	if err := c.request(ctx, "GET", "/v1/models", nil, &wire, ""); err != nil {
		return nil, err
	}
	result := make([]agent.ModelInfo, 0, len(wire))
	for _, m := range wire {
		result = append(result, agent.ModelInfo{ID: m.ID, Name: m.Name, Provider: m.Provider, ContextWindow: m.ContextWindow, Efforts: m.Efforts, DefaultEffort: m.DefaultEffort})
	}
	return result, nil
}

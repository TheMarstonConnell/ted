// Package controlplane owns durable agents independently of their clients.
package controlplane

import (
	"encoding/json"
	"github.com/TheMarstonConnell/ted/agent"
	"time"
)

type Settings struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}
type SettingsPatch struct {
	Model  *string `json:"model,omitempty"`
	Effort *string `json:"effort,omitempty"`
}
type Project struct {
	GitBranch string   `json:"git_branch,omitempty"`
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Root      string   `json:"root"`
	Defaults  Settings `json:"defaults"`
}
type CreateProjectRequest struct {
	Name     string   `json:"name"`
	Root     string   `json:"root"`
	Defaults Settings `json:"defaults"`
}
type CreateAgentRequest struct {
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	Prompt    string    `json:"prompt"`
	Settings  *Settings `json:"settings,omitempty"`
}
type SubmitMessageRequest struct {
	Text string `json:"text"`
}
type QueuedMessage struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
type Agent struct {
	ID             string             `json:"id"`
	ProjectID      string             `json:"project_id"`
	Title          string             `json:"title"`
	Settings       Settings           `json:"settings"`
	ActiveSettings *Settings          `json:"active_settings,omitempty"`
	Settled        bool               `json:"settled"`
	State          string             `json:"state"`
	Held           bool               `json:"held"`
	Queue          []QueuedMessage    `json:"queue"`
	Messages       []agent.Message    `json:"messages"`
	ContextUsage   agent.ContextUsage `json:"context_usage"`
	Events         []Event            `json:"-"`
	Cursor         uint64             `json:"cursor"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}
type Event struct {
	AgentID   string          `json:"agent_id"`
	Cursor    uint64          `json:"cursor"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string                       { return e.Message }
func problem(status int, code, message string) error { return &Error{status, code, message} }

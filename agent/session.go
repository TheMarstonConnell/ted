package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const sessionVersion = 1

// SessionInfo describes a durable conversation, not a running process.
type SessionInfo struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	ProjectRoot string    `json:"project_root"`
	WorkingDir  string    `json:"working_dir"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Settings    Settings  `json:"settings"`
}

type savedMessage struct {
	Message
	SourceModel string `json:"source_model,omitempty"`
}

type sessionSnapshot struct {
	Version  int    `json:"version"`
	Revision uint64 `json:"revision"`
	SessionInfo
	Messages       []savedMessage `json:"messages"`
	ContextUsage   ContextUsage   `json:"context_usage"`
	ManifestOffset int64          `json:"manifest_offset"`
}

func validSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func readSession(home, id string) (sessionSnapshot, error) {
	var s sessionSnapshot
	if !validSessionID(id) {
		return s, fmt.Errorf("invalid session ID %q", id)
	}
	data, err := os.ReadFile(filepath.Join(home, "threads", id, "session.json"))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("session %s: %w", id, err)
	}
	if s.Version != sessionVersion {
		return s, fmt.Errorf("session %s: unsupported version %d", id, s.Version)
	}
	if s.ID != id || len(s.Messages) == 0 || s.Messages[0].Role != "system" || !filepath.IsAbs(s.WorkingDir) || !filepath.IsAbs(s.ProjectRoot) {
		return s, fmt.Errorf("session %s: invalid snapshot", id)
	}
	return s, nil
}

// ListSessions returns saved conversations newest first. Browser-only threads
// are ignored. Corrupt snapshots are reported rather than silently hidden.
func ListSessions() ([]SessionInfo, error) {
	home := tedHome()
	entries, err := os.ReadDir(filepath.Join(home, "threads"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []SessionInfo
	for _, entry := range entries {
		if !entry.IsDir() || !validSessionID(entry.Name()) {
			continue
		}
		s, err := readSession(home, entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s.SessionInfo)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

func LatestSessionID() (string, error) {
	sessions, err := ListSessions()
	if err != nil {
		return "", err
	}
	root := resolveProjectRoot("")
	for _, s := range sessions {
		if s.ProjectRoot == root {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("no saved sessions for %s", root)
}

// EnablePersistence opts this agent into automatic between-turn saves.
// Library users and catalog commands remain side-effect free by default.
func (a *Agent) EnablePersistence() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if a.persistent {
		return nil
	}
	a.persistent = true
	if err := a.saveSessionLocked(); err != nil {
		a.persistent = false
		return err
	}
	return nil
}

// RestoreSession restores committed context without executing tools. Explicit
// overrides allow recovery when a saved model is no longer configured.
func (a *Agent) RestoreSession(id, model string, effort Effort) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if a.persistent {
		return fmt.Errorf("cannot restore over a persistent agent")
	}
	s, err := readSession(a.home, id)
	if err != nil {
		return err
	}
	info, err := os.Stat(s.WorkingDir)
	if err != nil {
		return fmt.Errorf("saved working directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("saved working directory is not a directory")
	}
	settings := s.Settings
	if model != "" {
		settings.Model = model
	}
	var found *ModelInfo
	for _, m := range a.ListModels() {
		if m.ID == settings.Model {
			copy := m
			found = &copy
			break
		}
	}
	if found == nil {
		return fmt.Errorf("saved model %q is unavailable; choose a replacement with --model", settings.Model)
	}
	settings.Provider = found.Provider
	if model != "" && model != s.Settings.Model {
		settings.Effort = found.DefaultEffort
	}
	if effort != "" {
		settings.Effort = effort
	}
	supported := settings.Effort == "" && len(found.Efforts) == 0
	for _, e := range found.Efforts {
		if e == settings.Effort {
			supported = true
		}
	}
	if !supported {
		return fmt.Errorf("saved effort %q is unavailable; choose a replacement with --effort", settings.Effort)
	}
	messages := make([]Message, len(s.Messages))
	for i, m := range s.Messages {
		messages[i] = m.Message
		messages[i].sourceModel = m.SourceModel
	}
	a.messages, a.settings = messages, settings
	a.contextUsage = s.ContextUsage
	if settings.Model != s.Settings.Model {
		a.contextUsage = ContextUsage{}
	}
	a.threadID, a.projectRoot, a.workingDir = s.ID, s.ProjectRoot, s.WorkingDir
	a.createdAt, a.sessionRevision = s.CreatedAt, s.Revision
	a.manifestOffset = s.ManifestOffset
	return nil
}

func (a *Agent) WorkingDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workingDir
}

// Called under mu, only when no tool is running. A short writer lock and
// revision check prevent two resumed instances from overwriting each other.
func (a *Agent) saveSessionLocked() error {
	if !a.persistent {
		return nil
	}
	dir := filepath.Join(a.home, "threads", a.threadID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	lock := filepath.Join(dir, "session.write-lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return fmt.Errorf("save session: writer lock unavailable (%s): %w", lock, err)
	}
	defer os.Remove(lock)
	previous, err := readSession(a.home, a.threadID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if previous.Revision != a.sessionRevision {
		return fmt.Errorf("save session: conversation changed in another process; reopen it before continuing")
	}
	now := time.Now().UTC()
	created := a.createdAt
	if created.IsZero() {
		created = now
	}
	s := sessionSnapshot{Version: sessionVersion, Revision: a.sessionRevision + 1,
		SessionInfo:  SessionInfo{ID: a.threadID, ProjectRoot: a.projectRoot, WorkingDir: a.workingDir, CreatedAt: created, UpdatedAt: now, Settings: a.settings},
		ContextUsage: a.contextUsage, ManifestOffset: a.manifestOffset}
	for _, m := range a.messages {
		s.Messages = append(s.Messages, savedMessage{Message: m, SourceModel: m.sourceModel})
		if s.Title == "" && m.Role == "user" {
			s.Title = strings.Join(strings.Fields(m.Content.Text()), " ")
			r := []rune(s.Title)
			if len(r) > 80 {
				s.Title = string(r[:80]) + "…"
			}
		}
	}
	if s.Title == "" {
		s.Title = "New conversation"
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	file, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), filepath.Join(dir, "session.json"))
	}
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	a.createdAt, a.sessionRevision = created, s.Revision
	return nil
}

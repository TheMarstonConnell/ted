package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/TheMarstonConnell/ted/agent"
	"go.uber.org/zap"
)

type runningTurn struct {
	id       string
	instance *agent.Agent
	cancel   context.CancelFunc
	stopped  bool
	shutdown bool
}

type Service struct {
	mu           sync.RWMutex
	dir          string
	logger       *zap.Logger
	providers    []agent.Provider
	catalog      *agent.Agent
	state        diskState
	running      map[string]*runningTurn
	instances    map[string]*agent.Agent
	changed      chan struct{}
	closing      bool
	storageErr   error
	workers      sync.WaitGroup
	releaseLock  func() error
	save         func(string, diskState) error
	store        *sqliteStore
	pullRequests *pullRequestResolver
}

func NewService(dir string, logger *zap.Logger, providers []agent.Provider) (*Service, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	unlock, err := lockStore(dir)
	if err != nil {
		return nil, err
	}
	store, state, err := openSQLiteStore(dir)
	if err != nil {
		unlock()
		return nil, err
	}
	s := &Service{dir: dir, logger: logger, providers: append([]agent.Provider(nil), providers...), catalog: agent.NewAgent(logger, providers), state: state, running: map[string]*runningTurn{}, instances: map[string]*agent.Agent{}, changed: make(chan struct{}), releaseLock: unlock, store: store, pullRequests: newPullRequestResolver()}
	// A durable "running" record is evidence of interrupted work, never a
	// request to repeat tools. Even graceful shutdown follows this recovery rule.
	recovery := newStateChanges()
	for id, a := range s.state.Agents {
		interrupted := false
		if a.Agent.Workspace.Status == "fetching" || a.Agent.Workspace.Status == "creating" {
			recovery.agent(s.state, id)
			a.Agent.Workspace.Status = "failed"
			a.Agent.Workspace.Error = "Server stopped during workspace setup. Create a new chat to try again."
			interrupted = true
		}
		for i := range a.Agent.Queue {
			m := &a.Agent.Queue[i]
			if m.Status == "running" {
				recovery.queued(s.state, id, i)
				m.Status = "interrupted"
				m.Error = "server stopped during turn"
				interrupted = true
				s.eventLocked(a, "turn.interrupted", *m)
			}
		}
		if interrupted || a.Agent.State != "idle" {
			recovery.agent(s.state, id)
			a.Agent.Held = true
		}
		if a.Agent.ActiveSettings != nil {
			recovery.agent(s.state, id)
		}
		a.Agent.State = "idle"
		a.Agent.ActiveSettings = nil
		if interrupted {
			s.eventLocked(a, "agent.updated", s.summaryLocked(a))
		}
	}
	if err = s.store.apply(s.state, recovery); err != nil {
		store.close()
		unlock()
		return nil, err
	}
	s.mu.Lock()
	for id := range s.state.Agents {
		s.startLocked(id)
	}
	s.mu.Unlock()
	return s, nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s *Service) writableLocked() error {
	if s.storageErr != nil {
		return problem(503, "storage_failed", "durable storage unavailable: "+s.storageErr.Error())
	}
	if s.closing {
		return problem(503, "shutting_down", "server is shutting down")
	}
	return nil
}
func (s *Service) notifyLocked() { close(s.changed); s.changed = make(chan struct{}) }
func (s *Service) commitLocked(before *stateChanges) error {
	var err error
	if s.save != nil {
		err = s.save(s.dir, s.state)
	} else if s.store != nil {
		err = s.store.apply(s.state, before)
	} else {
		err = errors.New("control-plane storage is not initialized")
	}
	if err != nil {
		before.rollback(&s.state)
		return s.failStorageLocked(err)
	}
	s.notifyLocked()
	return nil
}

func (s *Service) failStorageLocked(err error) error {
	s.storageErr = err
	s.closing = true
	for _, r := range s.running {
		r.shutdown = true
		r.cancel()
	}
	s.notifyLocked()
	return problem(503, "storage_failed", "could not persist operation: "+err.Error())
}

func (s *Service) eventLocked(a *storedAgent, typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	a.Agent.Cursor++
	a.Agent.UpdatedAt = time.Now().UTC()
	a.Events = append(a.Events, Event{a.Agent.ID, a.Agent.Cursor, typ, raw, a.Agent.UpdatedAt})
}

// Updates intentionally omit transcript and queue bodies; clients fetch them
// when needed. Replay does not grow quadratically with conversation length.
func (s *Service) summaryLocked(a *storedAgent) any {
	summary := map[string]any{"id": a.Agent.ID, "project_id": a.Agent.ProjectID, "title": a.Agent.Title, "settings": a.Agent.Settings, "active_settings": a.Agent.ActiveSettings, "settled": a.Agent.Settled, "state": a.Agent.State, "held": a.Agent.Held, "context_usage": a.Agent.ContextUsage, "workspace": a.Agent.Workspace, "last_response_cursor": a.Agent.LastResponseCursor, "read_cursor": a.Agent.ReadCursor}
	if a.Agent.ParentAgentID != "" {
		summary["parent_agent_id"] = a.Agent.ParentAgentID
	}
	return summary
}
func (s *Service) recordLocked(id string) (*storedAgent, error) {
	a, ok := s.state.Agents[id]
	if !ok {
		return nil, problem(404, "not_found", "agent not found")
	}
	return a, nil
}
func (s *Service) Models() []agent.ModelInfo { return s.catalog.ListModels() }
func (s *Service) normalizeSettings(settings Settings) (Settings, error) {
	// Validate independently of the live runtime.
	a := agent.NewSettingsSelector(s.providers)
	if settings.Model != "" {
		if _, err := a.SetModel(settings.Model); err != nil {
			return Settings{}, problem(400, "invalid_settings", err.Error())
		}
	}
	if settings.Effort != "" {
		if _, err := a.SetEffort(agent.Effort(settings.Effort)); err != nil {
			return Settings{}, problem(400, "invalid_settings", err.Error())
		}
	}
	current := a.Settings()
	return Settings{current.Model, string(current.Effort)}, nil
}
func (s *Service) Projects() []Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Project, 0, len(s.state.Projects))
	for _, p := range s.state.Projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *Service) GetProject(id string) (Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.state.Projects[id]
	if !ok {
		return Project{}, problem(404, "not_found", "project not found")
	}
	return p, nil
}
func (s *Service) CreateProject(req CreateProjectRequest) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Project{}, err
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Root) == "" {
		return Project{}, problem(400, "invalid_project", "name and root are required")
	}
	if !filepath.IsAbs(req.Root) {
		return Project{}, problem(400, "invalid_project", "root must be an absolute server-local directory")
	}
	root, err := filepath.EvalSymlinks(req.Root)
	if err != nil {
		return Project{}, problem(400, "invalid_project", err.Error())
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return Project{}, problem(400, "invalid_project", "root must be an existing directory")
	}
	root = filepath.Clean(root)
	if _, exists, err := s.projectByRootLocked(root); err != nil {
		return Project{}, s.failStorageLocked(err)
	} else if exists {
		return Project{}, problem(409, "project_exists", "a project already uses this root")
	}
	defaults, err := s.normalizeSettings(req.Defaults)
	if err != nil {
		return Project{}, err
	}
	selection := WorkspaceSelection{Mode: "current_checkout"}
	if req.WorkspaceDefaults != nil {
		selection = *req.WorkspaceDefaults
	}
	selection, err = normalizeWorkspace(root, selection)
	if err != nil {
		return Project{}, err
	}
	p := Project{ID: newID(), Name: req.Name, Root: root, Defaults: defaults, WorkspaceDefaults: selection}
	before := newStateChanges()
	before.project(s.state, p.ID)
	s.state.Projects[p.ID] = p
	if err = s.commitLocked(before); err != nil {
		return Project{}, err
	}
	return p, nil
}
func (s *Service) UpdateProject(id string, name *string, defaults *Settings, workspace ...*WorkspaceSelection) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Project{}, err
	}
	p, ok := s.state.Projects[id]
	if !ok {
		return Project{}, problem(404, "not_found", "project not found")
	}
	if name != nil {
		if strings.TrimSpace(*name) == "" {
			return Project{}, problem(400, "invalid_project", "name cannot be empty")
		}
		p.Name = *name
	}
	if defaults != nil {
		v, err := s.normalizeSettings(*defaults)
		if err != nil {
			return Project{}, err
		}
		p.Defaults = v
	}
	if len(workspace) > 0 && workspace[0] != nil {
		selection, err := normalizeWorkspace(p.Root, *workspace[0])
		if err != nil {
			return Project{}, err
		}
		p.WorkspaceDefaults = selection
	}
	before := newStateChanges()
	before.project(s.state, id)
	s.state.Projects[id] = p
	if err := s.commitLocked(before); err != nil {
		return Project{}, err
	}
	return p, nil
}
func (s *Service) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	if _, ok := s.state.Projects[id]; !ok {
		return problem(404, "not_found", "project not found")
	}
	for agentID, a := range s.state.Agents {
		if a.Agent.ProjectID != id {
			if parent := s.state.Agents[a.Agent.ParentAgentID]; parent != nil && parent.Agent.ProjectID == id {
				return problem(409, "project_not_empty", "project has child agents in other projects")
			}
			continue
		}
		if !a.Agent.Settled {
			return problem(409, "project_not_empty", "project still has unsettled agents; settle all agents before deleting it")
		}
		// Cancellation is asynchronous; workers still need their final records.
		if s.running[agentID] != nil {
			return problem(409, "project_not_empty", "settled agents are still stopping; try deleting the project again shortly")
		}
	}
	before := newStateChanges()
	before.project(s.state, id)
	for agentID, a := range s.state.Agents {
		if a.Agent.ProjectID == id {
			before.agent(s.state, agentID)
			delete(s.state.Agents, agentID)
		}
	}
	for key, receipt := range s.state.Receipts {
		if a := before.agents[receipt.AgentID]; a != nil && a.Agent.ProjectID == id {
			before.receipt(s.state, key)
			delete(s.state.Receipts, key)
		}
	}
	delete(s.state.Projects, id)
	if err := s.commitLocked(before); err != nil {
		return err
	}
	for agentID, a := range before.agents {
		if a.Agent.ProjectID == id {
			delete(s.instances, agentID)
		}
	}
	return nil
}
func (s *Service) Agents(includeSettled bool, projectID string) []Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agentsLocked(includeSettled, projectID)
}
func (s *Service) agentsLocked(includeSettled bool, projectID string) []Agent {
	out := make([]Agent, 0)
	for _, a := range s.state.Agents {
		if (!includeSettled && a.Agent.Settled) || (projectID != "" && a.Agent.ProjectID != projectID) {
			continue
		}
		out = append(out, cloneAgent(a.Agent))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// AgentsPage requires positive page and pageSize values.
func (s *Service) AgentsPage(includeSettled bool, projectID string, page, pageSize int64) ([]Agent, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	candidates := make([]*storedAgent, 0)
	for _, a := range s.state.Agents {
		if (!includeSettled && a.Agent.Settled) || (projectID != "" && a.Agent.ProjectID != projectID) {
			continue
		}
		candidates = append(candidates, a)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Agent.UpdatedAt.Equal(candidates[j].Agent.UpdatedAt) {
			return candidates[i].Agent.ID < candidates[j].Agent.ID
		}
		return candidates[i].Agent.UpdatedAt.After(candidates[j].Agent.UpdatedAt)
	})

	total := len(candidates)
	// Check the page offset against the filtered count before multiplying. A
	// very large, valid page is simply out of range, not an arithmetic error.
	pageIndex := page - 1
	if pageIndex > int64(total)/pageSize {
		return []Agent{}, total
	}
	start64 := pageIndex * pageSize
	if start64 >= int64(total) {
		return []Agent{}, total
	}
	end64 := int64(total)
	if pageSize <= int64(total)-start64 {
		end64 = start64 + pageSize
	}
	start, end := int(start64), int(end64)
	result := make([]Agent, 0, end-start)
	for _, a := range candidates[start:end] {
		result = append(result, cloneAgent(a.Agent))
	}
	return result, total
}

func (s *Service) GetAgent(id string) (Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, err := s.recordLocked(id)
	if err != nil {
		return Agent{}, err
	}
	return cloneAgent(a.Agent), nil
}

// QueuedMessages returns the queue without copying conversation history.
func (s *Service) QueuedMessages(id string) ([]QueuedMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, err := s.recordLocked(id)
	if err != nil {
		return nil, err
	}
	return slices.Clone(a.Agent.Queue), nil
}

// History copies only the requested page while holding the snapshot lock.
func (s *Service) History(id string, after uint64, limit int) ([]agent.Message, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, err := s.recordLocked(id)
	if err != nil {
		return nil, 0, err
	}
	if after > uint64(len(a.Agent.Messages)) {
		return nil, 0, problem(410, "cursor_invalid", "history cursor is ahead of conversation")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, 0, problem(400, "invalid_limit", "limit must not exceed 1000")
	}
	start := int(after)
	end := start + min(limit, len(a.Agent.Messages)-start)
	return cloneMessages(a.Agent.Messages[start:end]), end, nil
}

func (s *Service) ReadAgent(id string, cursor uint64) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Agent{}, err
	}
	a, err := s.recordLocked(id)
	if err != nil {
		return Agent{}, err
	}
	if cursor > a.Agent.Cursor {
		return Agent{}, problem(410, "cursor_invalid", "read cursor is beyond current agent events")
	}
	if cursor <= a.Agent.ReadCursor {
		return cloneAgent(a.Agent), nil
	}
	before := newStateChanges()
	before.agent(s.state, id)
	a.Agent.ReadCursor = cursor
	s.eventLocked(a, "agent.updated", s.summaryLocked(a))
	if err = s.commitLocked(before); err != nil {
		return Agent{}, err
	}
	return cloneAgent(a.Agent), nil
}
func (s *Service) CreateAgent(req CreateAgentRequest, key string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Agent{}, err
	}
	scope := "create:" + key
	var hash string
	if key != "" {
		hash = fingerprint(req)
		if r, ok := s.state.Receipts[scope]; ok {
			if r.Fingerprint != hash {
				return Agent{}, problem(409, "idempotency_conflict", "key already used with different request")
			}
			return cloneAgent(s.state.Agents[r.AgentID].Agent), nil
		}
	}
	p, ok := s.state.Projects[req.ProjectID]
	if !ok {
		return Agent{}, problem(404, "not_found", "project not found")
	}
	settings := p.Defaults
	if req.Settings != nil {
		if req.Settings.Model != "" {
			settings.Model = req.Settings.Model
			settings.Effort = ""
		}
		if req.Settings.Effort != "" {
			settings.Effort = req.Settings.Effort
		}
	}
	settings, err := s.normalizeSettings(settings)
	if err != nil {
		return Agent{}, err
	}
	workspace, err := s.creationWorkspaceLocked(p, req)
	if err != nil {
		return Agent{}, err
	}
	now := time.Now().UTC()
	id := newID()
	a := &storedAgent{Agent: Agent{ParentAgentID: req.ParentAgentID, Workspace: workspace, ID: id, ProjectID: p.ID, Title: req.Title, Settings: settings, State: "idle", Queue: []QueuedMessage{}, Messages: []agent.Message{}, CreatedAt: now, UpdatedAt: now}, Events: []Event{}}
	before := newStateChanges()
	before.agent(s.state, id)
	if key != "" {
		before.receipt(s.state, scope)
	}
	s.state.Agents[id] = a
	s.eventLocked(a, "agent.created", s.summaryLocked(a))
	if strings.TrimSpace(req.Prompt) != "" {
		s.lockWorkspaceLocked(a)
		m := QueuedMessage{ID: newID(), Text: req.Prompt, Status: "pending", CreatedAt: now}
		a.Agent.Queue = append(a.Agent.Queue, m)
		s.eventLocked(a, "message.queued", m)
	}
	if key != "" {
		s.state.Receipts[scope] = receipt{Fingerprint: hash, AgentID: id}
	}
	if err = s.commitLocked(before); err != nil {
		return Agent{}, err
	}
	s.startLocked(id)
	if err = s.writableLocked(); err != nil {
		return Agent{}, err
	}
	return cloneAgent(s.state.Agents[id].Agent), nil
}
func (s *Service) UpdateSettings(id string, patch SettingsPatch) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Agent{}, err
	}
	a, err := s.recordLocked(id)
	if err != nil {
		return Agent{}, err
	}
	settings := a.Agent.Settings
	if patch.Model != nil {
		if *patch.Model == "" {
			return Agent{}, problem(400, "invalid_settings", "model cannot be empty")
		}
		settings.Model = *patch.Model
		// Preserve supported effort, otherwise use the new model's default.
		base := agent.NewSettingsSelector(s.providers)
		if settingsOld := a.Agent.Settings; settingsOld.Model != "" {
			_, _ = base.SetModel(settingsOld.Model)
			if settingsOld.Effort != "" {
				_, _ = base.SetEffort(agent.Effort(settingsOld.Effort))
			}
		}
		if _, err = base.SetModel(settings.Model); err != nil {
			return Agent{}, problem(400, "invalid_settings", err.Error())
		}
		settings.Effort = string(base.Settings().Effort)
	}
	if patch.Effort != nil {
		settings.Effort = *patch.Effort
		if settings.Effort == "" {
			for _, m := range s.Models() {
				if m.ID == settings.Model && len(m.Efforts) > 0 {
					return Agent{}, problem(400, "invalid_settings", "effort cannot be empty for this model")
				}
			}
		}
	}
	settings, err = s.normalizeSettings(settings)
	if err != nil {
		return Agent{}, err
	}
	before := newStateChanges()
	before.agent(s.state, id)
	a.Agent.Settings = settings
	s.eventLocked(a, "agent.updated", s.summaryLocked(a))
	if err = s.commitLocked(before); err != nil {
		return Agent{}, err
	}
	return cloneAgent(a.Agent), nil
}
func (s *Service) Submit(id, text, key string) (QueuedMessage, error) {
	return s.SubmitMessage(id, SubmitMessageRequest{Text: text}, key)
}

func (s *Service) SubmitMessage(id string, req SubmitMessageRequest, key string) (QueuedMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return QueuedMessage{}, err
	}
	a, err := s.recordLocked(id)
	if err != nil {
		return QueuedMessage{}, err
	}
	if err = validateSubmitMessage(req); err != nil {
		return QueuedMessage{}, err
	}
	if req.Kind == "user" {
		req.Kind = ""
	}
	scope := "message:" + id + ":" + key
	var hash string
	if key != "" {
		if req.Kind == "" {
			hash = fingerprint(req.Text)
		} else {
			hash = fingerprint(req)
		}
		if r, ok := s.state.Receipts[scope]; ok {
			if r.Fingerprint != hash {
				return QueuedMessage{}, problem(409, "idempotency_conflict", "key already used with different message")
			}
			index, found, err := s.queuePositionLocked(a, r.MessageID)
			if err != nil {
				return QueuedMessage{}, s.failStorageLocked(err)
			}
			if found {
				return a.Agent.Queue[index], nil
			}
			return QueuedMessage{}, s.failStorageLocked(errors.New("durable receipt references a missing message"))
		}
	}
	if a.Agent.Workspace.Status == "failed" {
		return QueuedMessage{}, workspaceFailure(a.Agent.Workspace)
	}
	if a.Agent.Settled {
		return QueuedMessage{}, problem(409, "settled", "restore agent before submitting messages")
	}
	before := newStateChanges()
	before.agent(s.state, id)
	if key != "" {
		before.receipt(s.state, scope)
	}
	s.lockWorkspaceLocked(a)
	m := QueuedMessage{ID: newID(), Text: req.Text, Kind: req.Kind, SenderAgentID: req.SenderAgentID, Status: "pending", CreatedAt: time.Now().UTC()}
	a.Agent.Queue = append(a.Agent.Queue, m)
	a.Agent.Held = false
	s.eventLocked(a, "message.queued", m)
	s.eventLocked(a, "agent.updated", s.summaryLocked(a))
	if key != "" {
		s.state.Receipts[scope] = receipt{hash, id, m.ID}
	}
	if err = s.commitLocked(before); err != nil {
		return QueuedMessage{}, err
	}
	s.startLocked(id)
	if err = s.writableLocked(); err != nil {
		return QueuedMessage{}, err
	}
	return m, nil
}

func validateSubmitMessage(req SubmitMessageRequest) error {
	if strings.TrimSpace(req.Text) == "" {
		return problem(400, "invalid_message", "text cannot be empty")
	}
	if utf8.RuneCountInString(req.SenderAgentID) > 256 {
		return problem(400, "invalid_message", "sender_agent_id must not exceed 256 characters")
	}
	switch req.Kind {
	case "", "user":
		if req.SenderAgentID != "" {
			return problem(400, "invalid_message", "sender_agent_id is only valid for bot messages")
		}
	case "bot":
	default:
		return problem(400, "invalid_message", "kind must be user or bot")
	}
	return nil
}

func (s *Service) DeletePending(id, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	a, err := s.recordLocked(id)
	if err != nil {
		return err
	}
	index, found, err := s.queuePositionLocked(a, messageID)
	if err != nil {
		return s.failStorageLocked(err)
	}
	if !found {
		return problem(404, "not_found", "message not found")
	}
	m := &a.Agent.Queue[index]
	if m.Status == "cancelled" {
		return nil
	}
	if m.Status != "pending" {
		return problem(409, "not_pending", "only pending messages can be removed")
	}
	before := newStateChanges()
	before.queued(s.state, id, index)
	m.Status = "cancelled"
	s.eventLocked(a, "message.cancelled", *m)
	return s.commitLocked(before)
}

func (s *Service) Stop(id, turnID string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Agent{}, err
	}
	a, err := s.recordLocked(id)
	if err != nil {
		return Agent{}, err
	}
	if turnID == "" {
		return Agent{}, problem(400, "turn_required", "turn_id is required")
	}
	index, found, err := s.queuePositionLocked(a, turnID)
	if err != nil {
		return Agent{}, s.failStorageLocked(err)
	}
	if !found {
		return Agent{}, problem(404, "not_found", "turn not found")
	}
	if a.Agent.Queue[index].Status == "pending" {
		return Agent{}, problem(409, "not_running", "message has not started")
	}
	r := s.running[id]
	if r == nil || r.id != turnID || r.stopped {
		return cloneAgent(a.Agent), nil
	}
	before := newStateChanges()
	before.agent(s.state, id)
	a.Agent.State = "stopping"
	s.eventLocked(a, "agent.updated", s.summaryLocked(a))
	if err = s.commitLocked(before); err != nil {
		return Agent{}, err
	}
	r.stopped = true
	r.cancel()
	return cloneAgent(a.Agent), nil
}
func (s *Service) SetSettled(id string, settled bool) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Agent{}, err
	}
	a, err := s.recordLocked(id)
	if err != nil {
		return Agent{}, err
	}
	targets := []*storedAgent{a}
	if settled {
		children := make(map[string][]*storedAgent)
		for _, child := range s.state.Agents {
			if parent := child.Agent.ParentAgentID; parent != "" {
				children[parent] = append(children[parent], child)
			}
		}
		for i := 0; i < len(targets); i++ {
			targets = append(targets, children[targets[i].Agent.ID]...)
		}
	}
	changed := targets[:0]
	for _, target := range targets {
		if target.Agent.Settled != settled {
			changed = append(changed, target)
		}
	}
	if len(changed) == 0 {
		return cloneAgent(a.Agent), nil
	}
	before := newStateChanges()
	for _, target := range changed {
		before.agent(s.state, target.Agent.ID)
		target.Agent.Settled = settled
		target.Agent.Held = true
		if settled && s.running[target.Agent.ID] != nil {
			target.Agent.State = "stopping"
		}
		s.eventLocked(target, "agent.updated", s.summaryLocked(target))
	}
	if err = s.commitLocked(before); err != nil {
		return Agent{}, err
	}
	if settled {
		for _, target := range changed {
			if r := s.running[target.Agent.ID]; r != nil {
				r.stopped = true
				r.cancel()
			} else if instance := s.instances[target.Agent.ID]; instance != nil {
				_ = instance.Close()
			}
		}
	}
	return cloneAgent(a.Agent), nil
}
func (s *Service) Continue(id string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Agent{}, err
	}
	a, err := s.recordLocked(id)
	if err != nil {
		return Agent{}, err
	}
	if a.Agent.Workspace.Status == "failed" {
		return Agent{}, workspaceFailure(a.Agent.Workspace)
	}
	if a.Agent.Settled {
		return Agent{}, problem(409, "settled", "restore agent before continuing")
	}
	before := newStateChanges()
	before.agent(s.state, id)
	a.Agent.Held = false
	s.eventLocked(a, "agent.updated", s.summaryLocked(a))
	if err = s.commitLocked(before); err != nil {
		return Agent{}, err
	}
	s.startLocked(id)
	if err = s.writableLocked(); err != nil {
		return Agent{}, err
	}
	return cloneAgent(s.state.Agents[id].Agent), nil
}

// startLocked durably reserves the turn before allowing any external effects.
func (s *Service) startLocked(id string) {
	if s.closing || s.storageErr != nil || s.running[id] != nil {
		return
	}
	a := s.state.Agents[id]
	if a == nil || a.Agent.Settled || a.Agent.Held || a.Agent.Workspace.Status == "failed" {
		return
	}
	index, found, err := s.firstPendingLocked(a)
	if err != nil {
		s.failStorageLocked(err)
		return
	}
	if !found {
		return
	}
	before := newStateChanges()
	before.queued(s.state, id, index)
	m := &a.Agent.Queue[index]
	m.Status = "running"
	a.Agent.State = "running"
	settings := a.Agent.Settings
	a.Agent.ActiveSettings = &settings
	s.eventLocked(a, "turn.started", *m)
	s.eventLocked(a, "agent.updated", s.summaryLocked(a))
	if err := s.commitLocked(before); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningTurn{id: m.ID, cancel: cancel}
	s.running[id] = r
	project := s.state.Projects[a.Agent.ProjectID]
	history := cloneMessages(a.Agent.Messages)
	message := *m
	s.workers.Add(1)
	go s.run(ctx, id, r, project, settings, history, message)
}
func (s *Service) run(ctx context.Context, id string, r *runningTurn, project Project, settings Settings, history []agent.Message, message QueuedMessage) {
	defer s.workers.Done()
	defer r.cancel()
	s.mu.Lock()
	instance := s.instances[id]
	s.mu.Unlock()
	workspacePath, err := s.prepareWorkspace(ctx, id, project)
	if instance == nil && err == nil {
		instance, err = agent.NewAgentIn(s.logger, s.providers, workspacePath)
		if err == nil {
			err = instance.SetIdentity(id, project.Root, workspacePath, filepath.Join(s.dir, "runtime"))
		}
		if err == nil && len(history) > 0 {
			err = instance.RestoreConversation(history)
		}
		if err == nil {
			s.mu.Lock()
			offset := s.state.Agents[id].ManifestOffset
			s.instances[id] = instance
			s.mu.Unlock()
			instance.RestoreArtifactOffset(offset)
		}
	}
	if err == nil && settings.Model != "" {
		_, err = instance.SetModel(settings.Model)
	}
	if err == nil && settings.Effort != "" {
		_, err = instance.SetEffort(agent.Effort(settings.Effort))
	}

	if err == nil {
		s.mu.Lock()
		r.instance = instance
		s.mu.Unlock()
		instance.SetOutput(func(output agent.AgentResponse) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.storageErr != nil {
				return
			}
			a := s.state.Agents[id]
			before := newStateChanges()
			before.agent(s.state, id)
			a.Agent.ContextUsage = instance.ContextUsage()
			s.eventLocked(a, "output", output)
			if output.ResponseType == "agent" {
				a.Agent.LastResponseCursor = a.Agent.Cursor
			}
			if err := s.commitLocked(before); err != nil {
				s.logger.Error("could not save agent event", zap.Error(err))
			}
		})
		err = instance.TurnMessageContext(ctx, message.Text, message.Kind, message.SenderAgentID)
	}
	s.mu.Lock()
	delete(s.running, id)
	a := s.state.Agents[id]
	if s.storageErr != nil {
		s.mu.Unlock()
		if instance != nil {
			_ = instance.Close()
		}
		return
	}
	index, found, lookupErr := s.queuePositionLocked(a, r.id)
	if lookupErr == nil && !found {
		lookupErr = errors.New("durable running message is missing")
	}
	if lookupErr != nil {
		s.failStorageLocked(lookupErr)
		s.mu.Unlock()
		if instance != nil {
			_ = instance.Close()
		}
		return
	}
	before := newStateChanges()
	before.queued(s.state, id, index)
	m := &a.Agent.Queue[index]
	switch {
	case r.shutdown:
		m.Status = "interrupted"
		m.Error = "server shut down during turn"
		a.Agent.Held = true
	case r.stopped:
		m.Status = "cancelled"
	case err != nil:
		m.Status = "failed"
		m.Error = err.Error()
		a.Agent.Held = true
	default:
		m.Status = "completed"
	}
	finished := *m
	if instance != nil {
		a.ManifestOffset = instance.ArtifactOffset()
		current := instance.Messages()
		a.Agent.Messages = current
		a.Agent.ContextUsage = instance.ContextUsage()
		if len(current) > len(history) {
			s.eventLocked(a, "conversation", current[len(history):])
		}
	}
	a.Agent.State = "idle"
	a.Agent.ActiveSettings = nil
	s.eventLocked(a, "turn."+finished.Status, finished)
	s.eventLocked(a, "agent.updated", s.summaryLocked(a))
	if saveErr := s.commitLocked(before); saveErr == nil {
		// Release browser tabs before another client can restore/start this identity.
		if a.Agent.Settled && instance != nil {
			_ = instance.Close()
		}
		s.startLocked(id)
	}
	s.mu.Unlock()
}

func (s *Service) Events(id string, after uint64, limit int) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, err := s.recordLocked(id)
	if err != nil {
		return nil, err
	}
	if after > a.Agent.Cursor {
		return nil, problem(410, "cursor_invalid", "cursor is beyond retained history")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, problem(400, "invalid_limit", "limit must not exceed 1000")
	}
	end := min(len(a.Events), int(after)+limit)
	out := cloneEvents(a.Events[int(after):end])
	if out == nil {
		out = []Event{}
	}
	return out, nil
}

// SnapshotEvents obtains inventory, replay and next-change notification under
// one lock. Waiting on changed cannot miss mutations after this snapshot.
func (s *Service) SnapshotEvents(cursors map[string]uint64, all bool, ids []string) ([]Agent, []Event, <-chan struct{}, error) {
	return s.snapshotEvents(cursors, all, ids, false)
}

func (s *Service) snapshotEvents(cursors map[string]uint64, all bool, ids []string, summaries bool) ([]Agent, []Event, <-chan struct{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.storageErr != nil {
		return nil, nil, nil, problem(503, "storage_failed", s.storageErr.Error())
	}
	selected := map[string]bool{}
	for id, cursor := range cursors {
		a, ok := s.state.Agents[id]
		if !ok {
			return nil, nil, nil, problem(410, "cursor_invalid", "cursor references unknown agent "+id)
		}
		if cursor > a.Agent.Cursor {
			return nil, nil, nil, problem(410, "cursor_invalid", "cursor is beyond retained history for "+id)
		}
	}
	for _, id := range ids {
		if _, err := s.recordLocked(id); err != nil {
			return nil, nil, nil, err
		}
		selected[id] = true
	}
	if all {
		for id, a := range s.state.Agents {
			if !a.Agent.Settled {
				selected[id] = true
			}
		}
	}
	ordered := make([]string, 0, len(selected))
	for id := range selected {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	inventory := make([]Agent, 0, len(ordered))
	events := make([]Event, 0)
	for _, id := range ordered {
		a := s.state.Agents[id]
		snapshot := a.Agent
		if summaries {
			snapshot.Queue = nil
			snapshot.Messages = nil
		}
		inventory = append(inventory, cloneAgent(snapshot))
		events = append(events, cloneEvents(a.Events[cursors[id]:])...)
	}
	return inventory, events, s.changed, nil
}

// BeginShutdown blocks new starts and requests cancellation without waiting for
// workers. Call before closing HTTP sockets so ordinary Stop semantics cannot
// advance a queue during transport shutdown.
func (s *Service) BeginShutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if !s.closing {
		before := newStateChanges()
		s.closing = true
		for id, r := range s.running {
			r.shutdown = true
			a := s.state.Agents[id]
			before.agent(s.state, id)
			a.Agent.State = "stopping"
			a.Agent.Held = true
			s.eventLocked(a, "agent.updated", s.summaryLocked(a))
		}
		err = s.commitLocked(before)
	}
	for _, r := range s.running {
		r.shutdown = true
		r.cancel()
	}
	s.notifyLocked()
	return err
}

func (s *Service) Close(ctx context.Context) error {
	_ = s.BeginShutdown()
	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		s.mu.Lock()
		instances := make([]*agent.Agent, 0, len(s.instances))
		for _, instance := range s.instances {
			instances = append(instances, instance)
		}
		s.instances = map[string]*agent.Agent{}
		s.mu.Unlock()
		var cleanup sync.WaitGroup
		for _, instance := range instances {
			cleanup.Add(1)
			go func(a *agent.Agent) { defer cleanup.Done(); _ = a.Close() }(instance)
		}
		cleanup.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.store != nil {
		err = s.store.close()
	}
	if s.releaseLock != nil {
		err = errors.Join(err, s.releaseLock())
		s.releaseLock = nil
	}
	return errors.Join(s.storageErr, err)
}
func (s *Service) String() string { return fmt.Sprintf("control plane (%s)", s.dir) }

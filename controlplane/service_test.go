package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"go.uber.org/zap"
)

type controlledProvider struct {
	calls             chan agent.CompletionRequest
	results           chan error
	mu                sync.Mutex
	active, maxActive int
}

func (p *controlledProvider) Name() string { return "test" }
func (p *controlledProvider) ListModels() []agent.ModelInfo {
	return []agent.ModelInfo{{ID: "one", Efforts: []agent.Effort{agent.EffortLow, agent.EffortHigh}, DefaultEffort: agent.EffortLow}, {ID: "two", Efforts: []agent.Effort{agent.EffortLow, agent.EffortHigh}, DefaultEffort: agent.EffortHigh}}
}
func (p *controlledProvider) Complete(_ *zap.Logger, r agent.CompletionRequest) (*agent.Response, error) {
	p.mu.Lock()
	p.active++
	p.maxActive = max(p.maxActive, p.active)
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
	p.calls <- r
	select {
	case <-r.Context.Done():
		return nil, r.Context.Err()
	case err := <-p.results:
		if err != nil {
			return nil, err
		}
		return &agent.Response{Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("done")}}}}, nil
	}
}
func serviceFixture(t *testing.T) (*Service, *controlledProvider, Project, Agent) {
	t.Helper()
	p := &controlledProvider{calls: make(chan agent.CompletionRequest, 20), results: make(chan error, 20)}
	s, err := NewService(t.TempDir(), nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	project, err := s.CreateProject(CreateProjectRequest{Name: "test", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, p, project, a
}
func awaitCall(t *testing.T, p *controlledProvider) agent.CompletionRequest {
	t.Helper()
	select {
	case r := <-p.calls:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not start")
		return agent.CompletionRequest{}
	}
}
func awaitAgent(t *testing.T, s *Service, id string, f func(Agent) bool) Agent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a, err := s.GetAgent(id)
		if err != nil {
			t.Fatal(err)
		}
		if f(a) {
			return a
		}
		time.Sleep(5 * time.Millisecond)
	}
	a, _ := s.GetAgent(id)
	t.Fatalf("agent did not reach expected state: %+v", a)
	return a
}
func noCall(t *testing.T, p *controlledProvider) {
	t.Helper()
	select {
	case <-p.calls:
		t.Fatal("unexpected next turn")
	case <-time.After(40 * time.Millisecond):
	}
}
func assertStatus(t *testing.T, err error, status int) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Status != status {
		t.Fatalf("error %v, expected status %d", err, status)
	}
}
func TestQueueStopAdvancesSettingsAtExecutionAndIdempotency(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	first, err := s.Submit(a.ID, "first", "key")
	if err != nil {
		t.Fatal(err)
	}
	r := awaitCall(t, p)
	if r.Model != "test/one" && r.Model != "one" {
		t.Fatalf("model %q", r.Model)
	}
	same, err := s.Submit(a.ID, "first", "key")
	if err != nil || same.ID != first.ID {
		t.Fatalf("idempotency: %+v %v", same, err)
	}
	_, err = s.Submit(a.ID, "different", "key")
	assertStatus(t, err, 409)
	second, err := s.Submit(a.ID, "second", "")
	if err != nil {
		t.Fatal(err)
	}
	model, effort := "test/two", "low"
	updated, err := s.UpdateSettings(a.ID, SettingsPatch{Model: &model, Effort: &effort})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Settings.Model != model || updated.ActiveSettings.Model != "test/one" {
		t.Fatalf("settings %+v", updated)
	}
	if _, err = s.Stop(a.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	r = awaitCall(t, p)
	if r.Model != "test/two" || r.Effort != agent.EffortLow {
		t.Fatalf("next turn settings %+v", r)
	}
	if _, err = s.Stop(a.ID, first.ID); err != nil {
		t.Fatal(err)
	} // retry must not cancel second
	noCall(t, p)
	p.results <- nil
	done := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" })
	if done.Queue[0].Status != "cancelled" || done.Queue[1].ID != second.ID || done.Queue[1].Status != "completed" {
		t.Fatalf("queue %+v", done.Queue)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.maxActive != 1 {
		t.Fatalf("overlapping turns: %d", p.maxActive)
	}
}
func TestFailureHoldsAndNewMessageImplicitlyContinuesFIFO(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	s.Submit(a.ID, "first", "")
	awaitCall(t, p)
	s.Submit(a.ID, "second", "")
	p.results <- errors.New("nonretryable failure")
	awaitAgent(t, s, a.ID, func(a Agent) bool { return a.Held && a.State == "idle" })
	noCall(t, p)
	s.Submit(a.ID, "third", "")
	r := awaitCall(t, p)
	if got := r.Messages[len(r.Messages)-1].Content.Text(); got != "second" {
		t.Fatalf("ran %q, wanted second", got)
	}
	p.results <- nil
	r = awaitCall(t, p)
	if got := r.Messages[len(r.Messages)-1].Content.Text(); got != "third" {
		t.Fatalf("ran %q, wanted third", got)
	}
	p.results <- nil
	awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" && !a.Held })
}
func TestSettleCancelsHidesAndUnsettleDoesNotContinue(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	s.Submit(a.ID, "first", "")
	awaitCall(t, p)
	s.Submit(a.ID, "second", "")
	if _, err := s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" && a.Settled })
	noCall(t, p)
	if len(s.Agents(false, "")) != 0 || len(s.Agents(true, "")) != 1 {
		t.Fatal("settled filtering")
	}
	_, err := s.Submit(a.ID, "no", "")
	assertStatus(t, err, 409)
	_, err = s.Continue(a.ID)
	assertStatus(t, err, 409)
	s.SetSettled(a.ID, false)
	noCall(t, p)
	if _, err = s.Continue(a.ID); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	p.results <- nil
	awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" })
}
func TestRestartRetainsQueueEventsAndDoesNotRetryInterruptedTurn(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	s.Submit(a.ID, "first", "")
	awaitCall(t, p)
	second, _ := s.Submit(a.ID, "second", "persisted")
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewService(s.dir, nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(context.Background())
	got, err := recovered.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Held || got.State != "idle" || got.Queue[0].Status != "interrupted" || got.Queue[1].Status != "pending" {
		t.Fatalf("recovery %+v", got)
	}
	noCall(t, p)
	original, _ := s.Events(a.ID, 0, 1000)
	replay, _ := recovered.Events(a.ID, 0, 1000)
	if !reflect.DeepEqual(original, replay) {
		t.Fatal("events changed on clean restart")
	}
	same, err := recovered.Submit(a.ID, "second", "persisted")
	if err != nil || same.ID != second.ID {
		t.Fatalf("durable dedupe: %+v %v", same, err)
	}
	noCall(t, p) // retry isn't new input
	recovered.Continue(a.ID)
	r := awaitCall(t, p)
	if r.Messages[len(r.Messages)-1].Content.Text() != "second" {
		t.Fatal("retried interrupted turn")
	}
	p.results <- nil
	awaitAgent(t, recovered, a.ID, func(a Agent) bool { return a.State == "idle" })
}
func TestCrashRecoveryOfDurableReservation(t *testing.T) {
	s, _, _, a := serviceFixture(t)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	record := state.Agents[a.ID]
	record.Agent.State = "running"
	record.Agent.Queue = append(record.Agent.Queue, QueuedMessage{ID: "crashed", Text: "must not replay", Status: "running", CreatedAt: time.Now()})
	if err = saveState(s.dir, state); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewService(s.dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(context.Background())
	got, _ := recovered.GetAgent(a.ID)
	if got.Queue[0].Status != "interrupted" || !got.Held || got.State != "idle" {
		t.Fatalf("recovery %+v", got)
	}
	events, _ := recovered.Events(a.ID, a.Cursor, 100)
	if len(events) < 1 || events[0].Type != "turn.interrupted" {
		t.Fatalf("missing interruption event %+v", events)
	}
}
func TestAtomicDiscoveryAndCursorValidation(t *testing.T) {
	s, _, project, a := serviceFixture(t)
	inventory, events, wake, err := s.SnapshotEvents(map[string]uint64{a.ID: a.Cursor}, true, nil)
	if err != nil || len(inventory) != 1 || len(events) != 0 {
		t.Fatalf("snapshot %v %v %v", inventory, events, err)
	}
	second, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("missed discovery wake")
	}
	inventory, events, _, err = s.SnapshotEvents(map[string]uint64{a.ID: a.Cursor}, true, nil)
	if err != nil || len(inventory) != 2 || len(events) != 1 || events[0].AgentID != second.ID {
		t.Fatalf("discovery %v %v %v", inventory, events, err)
	}
	_, err = s.Events(a.ID, a.Cursor+1, 100)
	assertStatus(t, err, 410)
	_, _, _, err = s.SnapshotEvents(map[string]uint64{"missing": 0}, true, nil)
	assertStatus(t, err, 410)
	s.SetSettled(a.ID, true)
	inventory, events, _, err = s.SnapshotEvents(map[string]uint64{a.ID: a.Cursor, second.ID: second.Cursor}, true, []string{a.ID})
	if err != nil || len(inventory) != 2 || len(events) == 0 {
		t.Fatalf("final settled event %v %v", events, err)
	}
}
func TestStorageFailureRejectsAcknowledgmentAndStopsWork(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	s.Submit(a.ID, "running", "")
	awaitCall(t, p)
	s.mu.Lock()
	s.save = func(string, diskState) error { return errors.New("disk full") }
	s.mu.Unlock()
	_, err := s.Submit(a.ID, "not acknowledged", "")
	assertStatus(t, err, 503)
	got, _ := s.GetAgent(a.ID)
	if len(got.Queue) != 1 {
		t.Fatal("failed transaction not rolled back")
	}
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("storage failure did not stop active work")
	}
}
func TestProjectDefaultsAreCopiedAndCreateIsIdempotent(t *testing.T) {
	s, _, project, a := serviceFixture(t)
	defaults := Settings{"test/two", "high"}
	s.UpdateProject(project.ID, nil, &defaults)
	original, _ := s.GetAgent(a.ID)
	if original.Settings.Model != "test/one" {
		t.Fatal("existing settings changed")
	}
	req := CreateAgentRequest{ProjectID: project.ID, Title: "created"}
	created, err := s.CreateAgent(req, "create")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateAgent(req, "create")
	if err != nil || again.ID != created.ID {
		t.Fatal("duplicate creation")
	}
	if created.Settings != defaults {
		t.Fatalf("defaults %+v", created.Settings)
	}
	req.Title = "different"
	_, err = s.CreateAgent(req, "create")
	assertStatus(t, err, 409)
	err = s.DeleteProject(project.ID)
	assertStatus(t, err, 409)
	_, err = s.CreateProject(CreateProjectRequest{Name: "bad", Root: filepath.Join(t.TempDir(), "missing")})
	assertStatus(t, err, 400)
}
func TestReturnedSnapshotsAreDetachedAndStoreIsPrivate(t *testing.T) {
	s, _, _, a := serviceFixture(t)
	got, _ := s.GetAgent(a.ID)
	got.Settings.Model = "changed"
	events, _ := s.Events(a.ID, 0, 100)
	events[0].Data = json.RawMessage(`null`)
	again, _ := s.GetAgent(a.ID)
	replay, _ := s.Events(a.ID, 0, 100)
	if again.Settings.Model == "changed" || string(replay[0].Data) == "null" {
		t.Fatal("caller mutated store")
	}
	info, err := os.Stat(filepath.Join(s.dir, "state.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions %v %v", info, err)
	}
	_, err = NewService(s.dir, nil, nil)
	if err == nil {
		t.Fatal("two writers opened the same store")
	}
}

type workspaceProvider struct{}

func (workspaceProvider) Name() string                  { return "workspace" }
func (workspaceProvider) ListModels() []agent.ModelInfo { return []agent.ModelInfo{{ID: "one"}} }
func (workspaceProvider) Complete(_ *zap.Logger, r agent.CompletionRequest) (*agent.Response, error) {
	if r.Messages[len(r.Messages)-1].Role == "user" {
		return &agent.Response{Choices: []agent.Choice{{FinishReason: "tool_calls", Message: agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{ToolCallType: "function", Id: "identity", Function: agent.FunctionCall{Name: "bash", Arguments: `{"command":"pwd; printf '%s\\n' \"$TED_THREAD_ID\" \"$TED_PROJECT_ROOT\" \"$TED_HOME\""}`}}}}}}}, nil
	}
	return &agent.Response{Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("done")}}}}, nil
}
func TestServiceWorkspacesAndToolOutputSurviveRestart(t *testing.T) {
	cwd, _ := os.Getwd()
	dir := t.TempDir()
	s, err := NewService(dir, nil, []agent.Provider{workspaceProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	roots := []string{t.TempDir(), t.TempDir()}
	ids := []string{}
	for i, root := range roots {
		p, err := s.CreateProject(CreateProjectRequest{Name: fmt.Sprintf("project-%d", i), Root: root})
		if err != nil {
			t.Fatal(err)
		}
		a, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID, Prompt: "identity"}, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	for i, id := range ids {
		a := awaitAgent(t, s, id, func(a Agent) bool { return a.State == "idle" })
		if a.Held || a.Queue[0].Status != "completed" {
			t.Fatalf("turn failed: %+v", a.Queue)
		}
		want := strings.Join([]string{roots[i], id, roots[i], filepath.Join(dir, "runtime"), ""}, "\n")
		events, err := s.Events(id, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range events {
			if e.Type != "output" {
				continue
			}
			var output agent.AgentResponse
			json.Unmarshal(e.Data, &output)
			if output.ResponseType == "tool_result" {
				found = true
				if output.FullToolOutput != want {
					t.Fatalf("tool identity %q, want %q", output.FullToolOutput, want)
				}
			}
		}
		if !found {
			t.Fatal("missing full tool output")
		}
	}
	if current, _ := os.Getwd(); current != cwd {
		t.Fatal("service changed global cwd")
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, err := NewService(dir, nil, []agent.Provider{workspaceProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close(context.Background())
	for _, id := range ids {
		before, _ := s.GetAgent(id)
		after, _ := restored.GetAgent(id)
		if !reflect.DeepEqual(before.Messages, after.Messages) {
			t.Fatal("lost conversation checkpoint")
		}
		eventsBefore, _ := s.Events(id, 0, 100)
		eventsAfter, _ := restored.Events(id, 0, 100)
		if !reflect.DeepEqual(eventsBefore, eventsAfter) {
			t.Fatal("lost output events")
		}
	}
}

func TestDeleteProjectWithSettledAgents(t *testing.T) {
	s, _, project, a := serviceFixture(t)
	other, err := s.CreateProject(CreateProjectRequest{Name: "other", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, err := s.CreateAgent(CreateAgentRequest{ProjectID: other.ID}, "other-key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "delete-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, s.DeleteProject(project.ID), 409)
	if _, err = s.GetAgent(a.ID); err != nil {
		t.Fatal("blocked deletion removed settled agent", err)
	}
	if _, err = s.SetSettled(second.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteProject(project.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, second.ID} {
		_, err = s.GetAgent(id)
		assertStatus(t, err, 404)
	}
	if _, err = s.GetAgent(otherAgent.ID); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Projects) != 1 || len(state.Agents) != 1 || len(state.Receipts) != 1 {
		t.Fatalf("unexpected persisted state: %+v", state)
	}
	if _, err = os.Stat(project.Root); err != nil {
		t.Fatal("project directory removed", err)
	}
	assertStatus(t, s.DeleteProject(project.ID), 404)
}

func TestDeleteSettledProjectRollsBackOnStorageFailure(t *testing.T) {
	s, _, project, a := serviceFixture(t)
	if _, err := s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.save = func(string, diskState) error { return errors.New("disk full") }
	s.mu.Unlock()
	assertStatus(t, s.DeleteProject(project.ID), 503)
	if _, err := s.GetAgent(a.ID); err != nil {
		t.Fatal("failed deletion removed agent", err)
	}
	if _, ok := s.state.Projects[project.ID]; !ok {
		t.Fatal("failed deletion removed project")
	}
}

func TestDeleteProjectWaitsForSettledWorker(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	f := newHTTPFixture(t, &httpTestProvider{complete: func(r agent.CompletionRequest) (*agent.Response, error) {
		close(started)
		<-r.Context.Done()
		close(cancelled)
		<-release
		return nil, r.Context.Err()
	}})
	var finish sync.Once
	defer finish.Do(func() { close(release) })
	project := f.project()
	a := f.agent(project)
	if _, err := f.s.Submit(a.ID, "run", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not start")
	}
	if _, err := f.s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("settlement did not cancel the turn")
	}
	assertStatus(t, f.s.DeleteProject(project.ID), 409)
	got, err := f.s.GetAgent(a.ID)
	if err != nil || got.State != "stopping" {
		t.Fatalf("stopping agent was not retained: %+v %v", got, err)
	}
	finish.Do(func() { close(release) })
	awaitAgent(t, f.s, a.ID, func(a Agent) bool { return a.State == "idle" })
	if err := f.s.DeleteProject(project.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteProjectPreservesCrossProjectParentage(t *testing.T) {
	s, _, project, parent := serviceFixture(t)
	childProject, err := s.CreateProject(CreateProjectRequest{Name: "child", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateAgent(CreateAgentRequest{ProjectID: childProject.ID, ParentAgentID: parent.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetSettled(parent.ID, true); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, s.DeleteProject(project.ID), 409)
	persisted, err := loadState(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Projects) != 2 || len(persisted.Agents) != 2 || persisted.Agents[child.ID].Agent.ParentAgentID != parent.ID {
		t.Fatal("blocked deletion damaged the family")
	}
	if err = s.DeleteProject(childProject.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteProject(project.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSettleDescendants(t *testing.T) {
	s, p, project, root := serviceFixture(t)
	create := func(parent string) Agent {
		t.Helper()
		a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, ParentAgentID: parent}, "")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	child := create(root.ID)
	otherProject, err := s.CreateProject(CreateProjectRequest{Name: "other", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	crossProject, err := s.CreateAgent(CreateAgentRequest{ProjectID: otherProject.ID, ParentAgentID: child.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	unrelated := create("")
	for _, a := range []Agent{child, crossProject} {
		if _, err := s.Submit(a.ID, "running", ""); err != nil {
			t.Fatal(err)
		}
		awaitCall(t, p)
		if _, err := s.Submit(a.ID, "queued", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetSettled(root.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Agent{root, child, crossProject} {
		awaitAgent(t, s, a.ID, func(a Agent) bool { return a.Settled && a.Held && a.State == "idle" })
	}
	other, err := s.GetAgent(unrelated.ID)
	if err != nil || other.Settled || other.Held {
		t.Fatalf("unrelated chat changed: %+v, %v", other, err)
	}
	noCall(t, p)
	if _, err := s.SetSettled(child.ID, false); err != nil {
		t.Fatal(err)
	}
	restored, _ := s.GetAgent(child.ID)
	descendant, _ := s.GetAgent(crossProject.ID)
	if restored.Settled || !descendant.Settled {
		t.Fatal("restore must affect only the requested chat")
	}
	// Repeating settlement must traverse an already-settled root.
	if _, err := s.SetSettled(root.ID, true); err != nil {
		t.Fatal(err)
	}
	restored, _ = s.GetAgent(child.ID)
	if !restored.Settled {
		t.Fatal("restored child was not settled again")
	}
	cursor := restored.Cursor
	if _, err := s.SetSettled(root.ID, true); err != nil {
		t.Fatal(err)
	}
	restored, _ = s.GetAgent(child.ID)
	if restored.Cursor != cursor {
		t.Fatal("duplicate settlement emitted another event")
	}
}

func TestMixedUserBotQueueFIFOAndAttribution(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	first, err := s.Submit(a.ID, "first user", "")
	if err != nil {
		t.Fatal(err)
	}
	firstCall := awaitCall(t, p)
	if got := firstCall.Messages[len(firstCall.Messages)-1].Content.Text(); got != "first user" {
		t.Fatal(got)
	}
	bot, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Text: "bot report", Kind: "bot", SenderAgentID: "unknown-script-source"}, "bot")
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Text: "last user", Kind: "user"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	p.results <- nil
	botCall := awaitCall(t, p)
	botInput := botCall.Messages[len(botCall.Messages)-1]
	if botInput.Kind != "" || botInput.SenderAgentID != "" || !strings.Contains(botInput.Content.Text(), "unknown-script-source") || !strings.Contains(botInput.Content.Text(), "bot report") {
		t.Fatalf("unsafe bot input: %+v", botInput)
	}
	p.results <- nil
	userCall := awaitCall(t, p)
	if got := userCall.Messages[len(userCall.Messages)-1].Content.Text(); got != "last user" {
		t.Fatalf("FIFO ran %q", got)
	}
	p.results <- nil
	done := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" })
	if len(done.Queue) != 3 || done.Queue[0].ID != first.ID || done.Queue[1].ID != bot.ID || done.Queue[2].ID != user.ID {
		t.Fatalf("queue order: %+v", done.Queue)
	}
	if done.Queue[1].Kind != "bot" || done.Queue[1].SenderAgentID != "unknown-script-source" || done.Queue[2].Kind != "" {
		t.Fatalf("queue metadata: %+v", done.Queue)
	}
	if len(done.Messages) < 6 {
		t.Fatalf("history: %+v", done.Messages)
	}
	botHistory := done.Messages[len(done.Messages)-4]
	if botHistory.Role != "user" || botHistory.Kind != "bot" || botHistory.SenderAgentID != "unknown-script-source" || botHistory.Content.Text() != "bot report" {
		t.Fatalf("bot history: %+v", botHistory)
	}
}

func TestBotSubmissionWakesHeldQueue(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	if _, err := s.Submit(a.ID, "fail", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	p.results <- errors.New("failed turn")
	awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" && a.Held })
	if _, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Text: "wake from bot", Kind: "bot"}, ""); err != nil {
		t.Fatal(err)
	}
	call := awaitCall(t, p)
	if got := call.Messages[len(call.Messages)-1].Content.Text(); !strings.Contains(got, "wake from bot") || !strings.Contains(got, "external bot report") {
		t.Fatal(got)
	}
	p.results <- nil
	done := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" })
	if done.Held || done.Queue[1].Status != "completed" {
		t.Fatalf("bot did not resume held agent: %+v", done)
	}
}

func TestBotMessagePersistenceAndIdempotencyMetadata(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	if _, err := s.Submit(a.ID, "blocking", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	req := SubmitMessageRequest{Text: "persist bot", Kind: "bot", SenderAgentID: "external-42"}
	queued, err := s.SubmitMessage(a.ID, req, "metadata-key")
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.SubmitMessage(a.ID, req, "metadata-key")
	if err != nil || same.ID != queued.ID {
		t.Fatalf("dedupe: %+v %v", same, err)
	}
	for _, conflict := range []SubmitMessageRequest{
		{Text: req.Text, Kind: "bot", SenderAgentID: "other-source"},
		{Text: req.Text, Kind: "user"},
		{Text: req.Text},
	} {
		_, err = s.SubmitMessage(a.ID, conflict, "metadata-key")
		assertStatus(t, err, 409)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewService(s.dir, nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Close(context.Background()) })
	got, err := recovered.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Queue) != 2 || got.Queue[1].Kind != "bot" || got.Queue[1].SenderAgentID != "external-42" || got.Queue[1].Status != "pending" {
		t.Fatalf("restored queue: %+v", got.Queue)
	}
	events, err := recovered.Events(a.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type != "message.queued" {
			continue
		}
		var message QueuedMessage
		if json.Unmarshal(event.Data, &message) == nil && message.ID == queued.ID {
			found = message.Kind == "bot" && message.SenderAgentID == "external-42"
		}
	}
	if !found {
		t.Fatal("durable queued event lost bot metadata")
	}
	same, err = recovered.SubmitMessage(a.ID, req, "metadata-key")
	if err != nil || same.ID != queued.ID {
		t.Fatalf("restored receipt: %+v %v", same, err)
	}
	noCall(t, p)
	if _, err = recovered.Continue(a.ID); err != nil {
		t.Fatal(err)
	}
	call := awaitCall(t, p)
	if input := call.Messages[len(call.Messages)-1]; !strings.Contains(input.Content.Text(), "persist bot") || input.Kind != "" || input.SenderAgentID != "" {
		t.Fatalf("restored bot input: %+v", input)
	}
	p.results <- nil
	awaitAgent(t, recovered, a.ID, func(a Agent) bool { return a.State == "idle" })
	if err = recovered.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(s.dir, nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	got, err = restarted.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, message := range got.Messages {
		if message.Kind == "bot" && message.SenderAgentID == "external-42" && message.Content.Text() == "persist bot" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored history lost bot metadata: %+v", got.Messages)
	}
}

func TestSubmitMessageMetadataValidation(t *testing.T) {
	s, _, _, a := serviceFixture(t)
	for _, req := range []SubmitMessageRequest{
		{Text: "message", Kind: "system"},
		{Text: "message", SenderAgentID: "source"},
		{Text: "message", Kind: "user", SenderAgentID: "source"},
		{Text: "message", Kind: "bot", SenderAgentID: strings.Repeat("source", 50)},
	} {
		_, err := s.SubmitMessage(a.ID, req, "")
		assertStatus(t, err, 400)
	}
	if _, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Text: "message", Kind: "bot", SenderAgentID: "does-not-exist"}, ""); err != nil {
		t.Fatal(err)
	}
}

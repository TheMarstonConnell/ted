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

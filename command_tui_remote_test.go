package main

import (
	"context"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/controlplane"
	"github.com/TheMarstonConnell/ted/remote"
	"go.uber.org/zap"
)

type apiTUIProvider struct {
	calls   atomic.Int32
	started chan agent.CompletionRequest
}

func (*apiTUIProvider) Name() string { return "test" }
func (*apiTUIProvider) ListModels() []agent.ModelInfo {
	return []agent.ModelInfo{{ID: "model", Name: "Test", Efforts: []agent.Effort{"low", "high"}, DefaultEffort: "low", ContextWindow: 1000}}
}
func (p *apiTUIProvider) Complete(_ *zap.Logger, r agent.CompletionRequest) (*agent.Response, error) {
	p.calls.Add(1)
	p.started <- r
	<-r.RequestContext().Done()
	return nil, r.RequestContext().Err()
}
func TestPrepareRemoteAgentIdleResumeAndQueuedUI(t *testing.T) {
	provider := &apiTUIProvider{started: make(chan agent.CompletionRequest, 10)}
	service, err := controlplane.NewService(t.TempDir(), nil, []agent.Provider{provider})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	server := httptest.NewServer(controlplane.NewHandler(service))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := remote.New(server.URL)
	instance, err := prepareRemoteAgent(ctx, c, "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	agents := service.Agents(true, "")
	if len(agents) != 1 || len(agents[0].Queue) != 0 || provider.calls.Load() != 0 {
		t.Fatal("agent creation must be idle", agents)
	}
	id := agents[0].ID
	cwd, _ := os.Getwd()
	if instance.WorkingDir() != cwd {
		t.Fatal(instance.WorkingDir(), cwd)
	}
	m := initialModel(instance)
	m.initialPrompt = "first"
	next, cmd := m.Update(initialPromptMsg{})
	m = next.(model)
	if cmd == nil || !m.busy {
		t.Fatal("initial prompt was not reserved")
	}
	// Execute exactly the command Bubble Tea would schedule.
	batch := cmd().(tea.BatchMsg)
	for _, command := range batch {
		if msg := command(); msg != nil {
			if _, ok := msg.(submitDoneMsg); ok {
				next, _ = m.Update(msg)
				m = next.(model)
			}
		}
	}
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("prompt not submitted")
	}
	if a, _ := service.GetAgent(id); len(a.Queue) != 1 || a.Queue[0].Text != "first" {
		t.Fatal("double initial submission", a.Queue)
	}
	// Busy API model changes and another draft must not be rejected locally.
	m.busy = true
	m, cmd = submit(m, "/effort high")
	if cmd != nil || instance.Settings().Effort != "high" {
		t.Fatal("next-turn config blocked")
	}
	m, cmd = submit(m, "queued")
	if cmd == nil || m.textarea.Value() != "" {
		t.Fatal("busy draft not accepted")
	}
	for _, command := range cmd().(tea.BatchMsg) {
		_ = command()
	}
	current, _ := service.GetAgent(id)
	if len(current.Queue) != 2 || current.Settings.Effort != "high" || current.ActiveSettings.Effort != "low" {
		t.Fatal(current)
	}
	resumed, err := prepareRemoteAgent(ctx, c, "", "", id, false)
	if err != nil || resumed.WorkingDir() != cwd || len(service.Agents(true, "")) != 1 {
		t.Fatal(resumed, err)
	}
	_, err = prepareRemoteAgent(ctx, c, "", "", "", true)
	if err != nil || len(service.Agents(true, "")) != 1 {
		t.Fatal("continue created new agent", err)
	}
	if err := instance.Stop(); err != nil {
		t.Fatal(err)
	}

	select {
	case req := <-provider.started:
		if req.Effort != "high" {
			t.Fatal("next-turn settings lost", req.Effort)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not advance queue")
	}
	current, _ = service.GetAgent(id)
	if current.Queue[0].Status != "cancelled" {
		t.Fatal("stop did not cancel selected turn")
	}
	if err := instance.Turn("third"); err != nil {
		t.Fatal(err)
	}
	if err := instance.Settle(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ = service.GetAgent(id)
		if current.State == "idle" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !current.Settled || !current.Held || current.Queue[2].Status != "pending" {
		t.Fatal("settle did not hold pending input")
	}
	if _, err := prepareRemoteAgent(ctx, c, "", "", "", true); err == nil {
		t.Fatal("--continue selected a settled agent")
	}
	if err := instance.Continue(); err == nil {
		t.Fatal("Continue must reject settled agents")
	}
	if err := instance.Turn("must reject"); err == nil {
		t.Fatal("message must reject settled agents")
	}
	if err := instance.Unsettle(); err != nil {
		t.Fatal(err)
	}
	current, _ = service.GetAgent(id)
	if current.Settled || !current.Held || current.State != "idle" {
		t.Fatal("restoration unexpectedly released work")
	}
	if err := instance.Continue(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("continue did not release restored queue")
	}
}

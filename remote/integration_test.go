package remote_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/controlplane"
	"github.com/TheMarstonConnell/ted/remote"
	"go.uber.org/zap"
)

type completedProvider struct{}

func (completedProvider) Name() string { return "test" }
func (completedProvider) ListModels() []agent.ModelInfo {
	return []agent.ModelInfo{{ID: "one", Name: "One", ContextWindow: 1000}}
}
func (completedProvider) Complete(_ *zap.Logger, r agent.CompletionRequest) (*agent.Response, error) {
	return &agent.Response{Usage: &agent.TokenUsage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15}, Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("answer")}}}}, nil
}

func TestRealAPICompletedEventsConversationDeltasAndUsage(t *testing.T) {
	service, err := controlplane.NewService(t.TempDir(), nil, []agent.Provider{completedProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	server := httptest.NewServer(controlplane.NewHandler(service))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := remote.New(server.URL)
	models, err := client.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.CreateProject(ctx, "Test", t.TempDir(), remote.Settings{Model: models[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.CreateAgent(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := remote.NewAgent(ctx, client, snapshot, project.Root, models)
	var mu sync.Mutex
	var outputs []agent.AgentResponse
	if err := a.Subscribe(ctx, func(u remote.Update) {
		mu.Lock()
		defer mu.Unlock()
		if u.Output != nil {
			outputs = append(outputs, *u.Output)
		}
		if u.Err != nil {
			t.Errorf("subscription error: %v", u.Err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	// Identical text must be delivered twice, not swallowed as optimistic echoes.
	for turn := 1; turn <= 2; turn++ {
		if err := a.Turn("same text"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			count := 0
			for _, m := range a.Messages() {
				if m.Role == "assistant" {
					count++
				}
			}
			if count == turn {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	users, answers := 0, 0
	for _, m := range a.Messages() {
		if m.Role == "user" {
			users++
		}
		if m.Role == "assistant" {
			answers++
		}
	}
	if users != 2 || answers != 2 {
		t.Fatalf("conversation delta lost history: %d users, %d answers", users, answers)
	}
	usage := a.ContextUsage()
	if !usage.Known || usage.EstimatedTokens != 15 || usage.ContextWindow != 1000 {
		t.Fatalf("wire context usage lost: %+v", usage)
	}
	mu.Lock()
	defer mu.Unlock()
	users, answers = 0, 0
	for _, o := range outputs {
		if o.ResponseType == "user" {
			users++
		}
		if o.Content == "answer" {
			answers++
		}
	}
	if users != 2 || answers != 2 {
		t.Fatalf("event output duplicated or lost: %+v", outputs)
	}
}

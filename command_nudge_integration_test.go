package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/controlplane"
	"github.com/TheMarstonConnell/ted/remote"
	"go.uber.org/zap"
)

type nudgeProvider struct {
	started chan agent.CompletionRequest
	release chan struct{}
}

func (*nudgeProvider) Name() string { return "test" }
func (*nudgeProvider) ListModels() []agent.ModelInfo {
	return []agent.ModelInfo{{ID: "nudge", Name: "Nudge test"}}
}
func (p *nudgeProvider) Complete(_ *zap.Logger, r agent.CompletionRequest) (*agent.Response, error) {
	p.started <- r
	select {
	case <-p.release:
		return &agent.Response{Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("Acknowledged.")}}}}, nil
	case <-r.RequestContext().Done():
		return nil, r.RequestContext().Err()
	}
}

func TestNudgeCommandRealQueueAndRetry(t *testing.T) {
	t.Setenv("TED_THREAD_ID", "unrelated-source-chat")
	p := &nudgeProvider{started: make(chan agent.CompletionRequest, 3), release: make(chan struct{}, 3)}
	s, err := controlplane.NewService(t.TempDir(), nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	server := httptest.NewServer(controlplane.NewHandler(s))
	defer server.Close()
	ctx := context.Background()
	c := remote.New(server.URL)
	project, err := c.CreateProject(ctx, "Nudge integration", t.TempDir(), remote.Settings{Model: "test/nudge"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := c.CreateAgent(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := func(key, text string) string {
		t.Helper()
		cmd := newRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"nudge", "--server", server.URL, "--idempotency-key", key, target.ID, text})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	first := run("first", "First child report")
	if !strings.HasPrefix(first, "Nudge accepted: ") {
		t.Fatal(first)
	}
	if replay := run("first", "First child report"); replay != first {
		t.Fatalf("retry changed receipt: %q != %q", replay, first)
	}
	if err := remote.NewAgent(ctx, c, target, project.Root, nil).Turn("Human follow-up"); err != nil {
		t.Fatal(err)
	}
	second := run("second", "Second child report")
	if merged := run("third", "Child work fully complete"); merged != second {
		t.Fatalf("pending nudges did not share an ID: %q != %q", merged, second)
	}
	if retry := run("third", "Child work fully complete"); retry != second {
		t.Fatalf("merged retry changed receipt: %q != %q", retry, second)
	}
	snapshot, err := c.GetAgent(ctx, target.ID)
	if err != nil || len(snapshot.Queue) != 3 {
		t.Fatalf("expected one active bot, queued user, queued bot: %+v, %v", snapshot.Queue, err)
	}
	if snapshot.Queue[0].Status != "running" || snapshot.Queue[1].Status != "pending" || snapshot.Queue[2].Status != "pending" {
		t.Fatalf("CLI should acknowledge without waiting for execution: %+v", snapshot.Queue)
	}
	if snapshot.Queue[2].Text != "Second child report\n\nChild work fully complete" {
		t.Fatalf("merged CLI text: %q", snapshot.Queue[2].Text)
	}
	for _, text := range []string{"First child report", "Human follow-up", "Second child report"} {
		select {
		case r := <-p.started:
			input := r.Messages[len(r.Messages)-1]
			if text == "Second child report" && !strings.Contains(input.Content.Text(), "Child work fully complete") {
				t.Fatalf("model missed merged completion: %+v", input)
			}
			if input.Role != "user" || !strings.Contains(input.Content.Text(), text) {
				t.Fatalf("FIFO/model input mismatch, expected %q, got %+v", text, input)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no turn for %q", text)
		}
		p.release <- struct{}{}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err = c.GetAgent(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Queue[2].Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("notification turn did not finish", snapshot.Queue)
		}
		time.Sleep(time.Millisecond)
	}
	var inputs []agent.Message
	for _, m := range snapshot.Messages {
		if m.Role == "user" {
			inputs = append(inputs, m)
		}
	}
	if len(inputs) != 3 || inputs[0].Kind != "bot" || inputs[0].SenderAgentID != "unrelated-source-chat" || inputs[1].Kind == "bot" || inputs[2].Kind != "bot" {
		t.Fatalf("history lost bot attribution: %+v", inputs)
	}
	if inputs[0].Content.Text() != "First child report" {
		t.Fatal("provider attribution leaked into stored display text", inputs[0])
	}
}

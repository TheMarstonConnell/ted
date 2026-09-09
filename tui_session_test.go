package main

import (
	"github.com/TheMarstonConnell/ted/agent"
	"go.uber.org/zap"
	"testing"
)

type transcriptProvider struct{ calls int }

func (*transcriptProvider) Name() string                  { return "transcript" }
func (*transcriptProvider) ListModels() []agent.ModelInfo { return []agent.ModelInfo{{ID: "a"}} }
func (p *transcriptProvider) Complete(_ *zap.Logger, _ agent.CompletionRequest) (*agent.Response, error) {
	p.calls++
	if p.calls == 1 {
		return &agent.Response{Choices: []agent.Choice{{FinishReason: "tool_calls", Message: agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{Id: "one", Function: agent.FunctionCall{Name: "bash", Arguments: `{"command":"echo hello"}`}}}}}}}, nil
	}
	return &agent.Response{Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("All done.")}}}}, nil
}

func TestRestoredTranscriptFromAgentHistory(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	a := agent.NewAgent(nil, []agent.Provider{&transcriptProvider{}})
	if err := a.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	if err := a.Turn("Say hello"); err != nil {
		t.Fatal(err)
	}
	b := agent.NewAgent(nil, []agent.Provider{&transcriptProvider{}})
	if err := b.RestoreSession(a.ThreadID(), "", ""); err != nil {
		t.Fatal(err)
	}
	m := initialModel(b)
	if len(m.messages) != 4 {
		t.Fatalf("entries: %+v", m.messages)
	}
	for i, want := range []transcriptEntry{{bannerMessage, "Ted Coding Agent"}, {userMessage, "Say hello"}, {toolCallMessage, `Ran shell command - "echo hello"`}, {agentMessage, "All done."}} {
		if m.messages[i] != want {
			t.Fatalf("entry %d = %+v; want %+v", i, m.messages[i], want)
		}
	}
	if m.directory != b.WorkingDir() {
		t.Fatal("wrong working directory")
	}
}

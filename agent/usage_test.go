package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestProviderUsageNormalization(t *testing.T) {
	var router Response
	if err := json.Unmarshal([]byte(`{"usage":{"prompt_tokens":800,"completion_tokens":40,"total_tokens":840,"prompt_tokens_details":{"cached_tokens":200},"completion_tokens_details":{"reasoning_tokens":30}}}`), &router); err != nil {
		t.Fatal(err)
	}
	completed, err := readResponsesStream(zap.NewNop(), strings.NewReader("data: "+`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":800,"output_tokens":40,"total_tokens":840,"input_tokens_details":{"cached_tokens":200},"output_tokens_details":{"reasoning_tokens":30}}}}`+"\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	codex, err := buildChatResponse(completed)
	if err != nil {
		t.Fatal(err)
	}
	want := TokenUsage{InputTokens: 800, OutputTokens: 40, TotalTokens: 840}
	if router.Usage == nil || codex.Usage == nil || *router.Usage != want || *codex.Usage != want {
		t.Fatalf("router=%+v codex=%+v", router.Usage, codex.Usage)
	}
	var absent Response
	_ = json.Unmarshal([]byte(`{}`), &absent)
	if absent.Usage != nil {
		t.Fatal("missing usage became zero")
	}
}

func TestContextTrackingLifecycle(t *testing.T) {
	p := &fakeProvider{name: "test", models: []ModelInfo{{ID: "a", ContextWindow: 1000}, {ID: "b", ContextWindow: 2000}}}
	a := NewAgent(nil, []Provider{p})
	if _, known := a.ContextUsage().Percent(); known {
		t.Fatal("new thread has usage")
	}
	calls := 0
	p.complete = func(CompletionRequest) (*Response, error) {
		calls++
		res := &Response{Usage: &TokenUsage{InputTokens: int64(calls * 100), OutputTokens: 20}, Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant", Content: TextContent("ok")}}}}
		if calls == 1 {
			res.Choices[0] = Choice{FinishReason: "tool_calls", Message: Message{Role: "assistant", ToolCalls: []ToolCall{{Id: "t", Function: FunctionCall{Name: "unknown"}}}}}
		}
		return res, nil
	}
	var snapshots []ContextUsage
	a.SetOutput(func(event AgentResponse) {
		if event.ResponseType == "usage" {
			snapshots = append(snapshots, a.ContextUsage())
		}
	})
	if err := a.Turn("hello"); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || snapshots[0].EstimatedTokens != 120 || snapshots[1].EstimatedTokens != 220 {
		t.Fatal(snapshots)
	}
	before := a.ContextUsage()
	if pct, ok := before.Percent(); !ok || pct != 22 {
		t.Fatal(before)
	}
	// A failed turn may have reported provisional usage; restore committed usage.
	calls = 0
	p.complete = func(CompletionRequest) (*Response, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("failure")
		}
		return &Response{Usage: &TokenUsage{InputTokens: 700, OutputTokens: 50}, Choices: []Choice{{FinishReason: "tool_calls", Message: Message{Role: "assistant", ToolCalls: []ToolCall{{Id: "t2", Function: FunctionCall{Name: "unknown"}}}}}}}, nil
	}
	if err := a.Turn("fail"); err == nil {
		t.Fatal("expected failure")
	}
	if a.ContextUsage() != before {
		t.Fatal("failed turn did not restore usage")
	}
	p.complete = func(CompletionRequest) (*Response, error) {
		return &Response{Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant", Content: TextContent("ok")}}}}, nil
	}
	if err := a.Turn("no usage"); err != nil {
		t.Fatal(err)
	}
	if a.ContextUsage().Known {
		t.Fatal("missing usage retained stale counts")
	}
	a.recordContextUsage("test/a", &TokenUsage{})
	if pct, ok := a.ContextUsage().Percent(); !ok || pct != 0 {
		t.Fatal("measured zero is unknown")
	}
	if _, err := a.SetModel("test/b"); err != nil {
		t.Fatal(err)
	}
	if u := a.ContextUsage(); u.Known || u.ContextWindow != 2000 || u.Model != "test/b" {
		t.Fatal(u)
	}
	if _, ok := (ContextUsage{Known: true, EstimatedTokens: 100}).Percent(); ok {
		t.Fatal("unknown capacity yielded percentage")
	}
}

type metadataTransport func(*http.Request) (*http.Response, error)

func (f metadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOpenRouterModelMetadata(t *testing.T) {
	p := NewOpenRouterProvider("")
	p.client = &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/models" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"meta/muse-spark-1.3-contributor","context_length":128000},{"id":"unlisted","context_length":500}]}`))}, nil
	})}
	if err := p.LoadModelMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	models := p.ListModels()
	if len(models) != 4 || models[0].ContextWindow != 128000 || models[1].ContextWindow != 0 {
		t.Fatal(models)
	}
	p.client.Transport = metadataTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	if err := p.LoadModelMetadata(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if p.ListModels()[0].ContextWindow != 128000 {
		t.Fatal("failure discarded metadata")
	}
	for _, m := range NewCodexProvider(nil).ListModels() {
		if m.ContextWindow != 1_050_000 {
			t.Fatal(m)
		}
	}
}

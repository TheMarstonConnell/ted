package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"go.uber.org/zap"
)

type fakeProvider struct {
	name     string
	models   []ModelInfo
	complete func(CompletionRequest) (*Response, error)
}

func (p *fakeProvider) Name() string            { return p.name }
func (p *fakeProvider) ListModels() []ModelInfo { return p.models }
func (p *fakeProvider) Complete(_ *zap.Logger, req CompletionRequest) (*Response, error) {
	if p.complete != nil {
		return p.complete(req)
	}
	return &Response{Choices: []Choice{{Message: Message{Role: "assistant", Content: TextContent("ok")}, FinishReason: "stop"}}}, nil
}
func settingsProvider() *fakeProvider {
	return &fakeProvider{name: "test", models: []ModelInfo{
		{ID: "a", Efforts: []Effort{EffortLow, EffortMedium, EffortHigh}, DefaultEffort: EffortMedium},
		{ID: "b", Efforts: []Effort{EffortMedium, EffortHigh}, DefaultEffort: EffortMedium},
		{ID: "plain"},
	}}
}

func TestSettingsValidationAndFallback(t *testing.T) {
	p := settingsProvider()
	a := NewAgent(nil, []Provider{p})
	if got := a.Settings(); got.Model != "test/a" || got.Effort != EffortMedium {
		t.Fatal(got)
	}
	if _, err := a.SetEffort(EffortHigh); err != nil {
		t.Fatal(err)
	}
	change, err := a.SetModel("test/b")
	if err != nil || change.EffortAdjusted || change.After.Effort != EffortHigh {
		t.Fatal(change, err)
	}
	_, _ = a.SetModel("test/a")
	_, _ = a.SetEffort(EffortLow)
	change, err = a.SetModel("test/b")
	if err != nil || !change.EffortAdjusted || change.After.Effort != EffortMedium {
		t.Fatal(change, err)
	}
	before := a.Settings()
	for _, id := range []string{"b", "other/b", "test/nope"} {
		if _, err := a.SetModel(id); err == nil {
			t.Fatalf("accepted %q", id)
		}
		if a.Settings() != before {
			t.Fatal("invalid model mutated settings")
		}
	}
	if _, err := a.SetEffort("bogus"); err == nil || a.Settings() != before {
		t.Fatal("invalid effort changed settings")
	}
	change, err = a.SetModel("test/plain")
	if err != nil || change.After.Effort != "" || len(a.ListEfforts()) != 0 {
		t.Fatal(change, err)
	}
	if _, err := a.SetEffort(EffortHigh); err == nil {
		t.Fatal("accepted unsupported effort")
	}
	models := a.ListModels()
	models[0].Efforts[0] = "corrupted"
	if p.models[0].Efforts[0] != EffortLow {
		t.Fatal("metadata aliases provider storage")
	}
	if NewAgent(nil, []Provider{p}).Settings().Effort != EffortMedium {
		t.Fatal("settings leaked between instances")
	}
}

func TestBusyAndFailureRollback(t *testing.T) {
	entered, release := make(chan CompletionRequest, 1), make(chan struct{})
	p := settingsProvider()
	p.complete = func(req CompletionRequest) (*Response, error) {
		entered <- req
		<-release
		return nil, errors.New("offline")
	}
	a := NewAgent(nil, []Provider{p})
	before := a.Messages()
	done := make(chan error, 1)
	go func() { done <- a.Turn("hello") }()
	req := <-entered
	if req.Model != "test/a" || req.Effort != EffortMedium {
		t.Fatal(req)
	}
	if a.Ready() {
		t.Fatal("ready during turn")
	}
	if _, err := a.SetModel("test/b"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if _, err := a.SetEffort(EffortHigh); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := a.Turn("overlap"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Messages(), before) {
		t.Fatal("uncommitted history visible")
	}
	_ = a.ListEfforts()
	a.SetOutput(func(AgentResponse) {})
	close(release)
	if err := <-done; err == nil {
		t.Fatal("expected failure")
	}
	if !a.Ready() || !reflect.DeepEqual(a.Messages(), before) {
		t.Fatal("failed turn did not restore idle/history")
	}
	if _, err := a.SetModel("test/b"); err != nil {
		t.Fatal(err)
	}
}

func TestTurnWithoutOutputAndHistorySnapshot(t *testing.T) {
	a := NewAgent(nil, []Provider{settingsProvider()})
	if err := a.Turn("hello"); err != nil {
		t.Fatal(err)
	}
	history := a.Messages()
	if len(history) != 3 {
		t.Fatal(history)
	}
	history[0].Content = TextContent("changed")
	if a.Messages()[0].Content.Text() == "changed" {
		t.Fatal("snapshot mutated history")
	}
	_, _ = a.SetModel("test/b")
	if len(a.Messages()) != 3 {
		t.Fatal("switch cleared history")
	}
}

func TestReasoningStaysWithSourceModel(t *testing.T) {
	original := []Message{{Role: "assistant", sourceModel: "codex/a", Content: TextContent("hello"), Reasoning: "private", ReasoningDetails: ReasoningDetails{json.RawMessage(`{"type":"reasoning"}`)}, ToolCalls: []ToolCall{{Id: "call"}}}}
	same := messagesForModel(original, "codex/a")
	if len(same[0].ReasoningDetails) != 1 {
		t.Fatal("lost same-model reasoning")
	}
	other := messagesForModel(original, "openrouter/b")
	if len(other[0].ReasoningDetails) != 0 || other[0].Reasoning != "" || other[0].Content.Text() != "hello" || len(other[0].ToolCalls) != 1 {
		t.Fatal(other)
	}
	same[0].ReasoningDetails[0][0] = 'x'
	if original[0].ReasoningDetails[0][0] != '{' {
		t.Fatal("request mutated history")
	}
}

func TestEmptyAgent(t *testing.T) {
	a := NewAgent(nil, nil)
	if err := a.Turn("hello"); err == nil {
		t.Fatal("expected no-model error")
	}
	if !a.Ready() {
		t.Fatal("empty agent stuck busy")
	}
}

func TestToolLoopUsesFixedSettingsAndRollsBackOnFailure(t *testing.T) {
	p := settingsProvider()
	a := NewAgent(nil, []Provider{p})
	calls := 0
	p.complete = func(req CompletionRequest) (*Response, error) {
		calls++
		if req.Model != "test/a" || req.Effort != EffortMedium {
			t.Fatal("turn settings changed", req)
		}
		if calls == 1 {
			return &Response{Choices: []Choice{{FinishReason: "tool_calls", Message: Message{Role: "assistant", ToolCalls: []ToolCall{{Id: "call1", Function: FunctionCall{Name: "missing", Arguments: "{}"}}}}}}}, nil
		}
		if len(req.Messages) != 4 || req.Messages[3].ToolCallId != "call1" {
			t.Fatal("tool call unanswered", req.Messages)
		}
		return nil, errors.New("failed after tool")
	}
	a.SetOutput(func(AgentResponse) {
		_ = a.Settings()
		_ = a.Messages()
		if _, err := a.SetEffort(EffortHigh); !errors.Is(err, ErrBusy) {
			t.Error(err)
		}
	})
	if err := a.Turn("use tool"); err == nil {
		t.Fatal("expected failure")
	}
	if !a.Ready() || len(a.Messages()) != 1 || calls != 2 {
		t.Fatal("incomplete tool exchange committed")
	}
}

func TestModelSwitchSanitizesRequestNotStoredHistory(t *testing.T) {
	p := settingsProvider()
	var requests []CompletionRequest
	p.complete = func(req CompletionRequest) (*Response, error) {
		requests = append(requests, req)
		return &Response{Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant", Content: TextContent("ok"), ReasoningDetails: ReasoningDetails{json.RawMessage(`{"opaque":true}`)}}}}}, nil
	}
	a := NewAgent(nil, []Provider{p})
	if err := a.Turn("first"); err != nil {
		t.Fatal(err)
	}
	_, _ = a.SetModel("test/b")
	if err := a.Turn("second"); err != nil {
		t.Fatal(err)
	}
	if len(requests[1].Messages[2].ReasoningDetails) != 0 {
		t.Fatal("old reasoning sent to new model")
	}
	if len(a.Messages()[2].ReasoningDetails) != 1 {
		t.Fatal("stored reasoning discarded")
	}
}

func TestOutputMayInspectAgent(t *testing.T) {
	a := NewAgent(nil, []Provider{settingsProvider()})
	emitted := false
	a.SetOutput(func(res AgentResponse) {
		emitted = true
		if res.Content != "ok" {
			t.Error(res)
		}
		_ = a.Settings()
		if len(a.Messages()) != 1 {
			t.Error("callback saw uncommitted history")
		}
		if _, err := a.SetEffort(EffortHigh); !errors.Is(err, ErrBusy) {
			t.Error(err)
		}
	})
	if err := a.Turn("hello"); err != nil {
		t.Fatal(err)
	}
	if !emitted || !a.Ready() {
		t.Fatal("output or completion missing")
	}
}

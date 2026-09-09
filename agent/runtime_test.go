package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func toolResponse(calls ...ToolCall) *Response {
	return &Response{Choices: []Choice{{FinishReason: "tool_calls", Message: Message{Role: "assistant", ToolCalls: calls}}}}
}
func bashCall(id, command string) ToolCall {
	args, _ := json.Marshal(map[string]string{"command": command})
	return ToolCall{Id: id, ToolCallType: "function", Function: FunctionCall{Name: "bash", Arguments: string(args)}}
}
func awaitTurn(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("turn failed to stop")
		return nil
	}
}

func TestNewAgentInDistinctDirectories(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{t.TempDir(), t.TempDir()}
	var wg sync.WaitGroup
	for _, dir := range dirs {
		p := settingsProvider()
		calls := 0
		p.complete = func(req CompletionRequest) (*Response, error) {
			calls++
			if calls == 1 {
				return toolResponse(bashCall("pwd", `printf '%s\n' "$PWD" > here; printf '%s\n' "$TED_PROJECT_ROOT"`)), nil
			}
			got := req.Messages[len(req.Messages)-1].Content.Text()
			if strings.TrimSpace(got) != dir {
				return nil, fmt.Errorf("root = %q, want %q", got, dir)
			}
			return &Response{Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant"}}}}, nil
		}
		a, err := NewAgentIn(nil, []Provider{p}, dir)
		if err != nil {
			t.Fatal(err)
		}
		if a.WorkingDir() != dir || a.ProjectRoot() != dir {
			t.Fatal(a.WorkingDir(), a.ProjectRoot())
		}
		wg.Go(func() {
			if err := a.Turn("where"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(dir, "here"))
		if err != nil || strings.TrimSpace(string(data)) != dir {
			t.Fatalf("%q: %q %v", dir, data, err)
		}
	}
	after, _ := os.Getwd()
	if after != cwd {
		t.Fatal("process cwd changed")
	}
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		if _, err := NewAgentIn(nil, nil, dir); err == nil {
			t.Fatal("accepted invalid directory")
		}
	}
}

func TestTurnContextCancellationWaitsForProvider(t *testing.T) {
	p := settingsProvider()
	started, stopped := make(chan struct{}), make(chan struct{})
	p.complete = func(req CompletionRequest) (*Response, error) {
		close(started)
		<-req.RequestContext().Done()
		// A runtime must wait for Complete to unwind, not race it in a goroutine.
		<-stopped
		return nil, req.RequestContext().Err()
	}
	a := NewAgent(nil, []Provider{p})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.TurnContext(ctx, "cancel me") }()
	<-started
	if err := a.RestoreConversation(a.Messages()); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := a.SetIdentity("id", a.ProjectRoot(), a.WorkingDir(), t.TempDir()); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("returned before provider stopped: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(stopped)
	if err := awaitTurn(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !a.Ready() || len(a.Messages()) != 2 {
		t.Fatal("cancelled history not committed")
	}
	p.complete = nil
	if err := a.Turn("next"); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationDuringRetryWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	p := settingsProvider()
	p.complete = func(CompletionRequest) (*Response, error) { calls++; return nil, io.ErrUnexpectedEOF }
	start := time.Now()
	_, err := completeWithRetry(zap.NewNop(), p, CompletionRequest{Context: ctx}, func(int, time.Duration) { cancel() })
	if !errors.Is(err, context.Canceled) || calls != 1 || time.Since(start) > time.Second {
		t.Fatalf("err=%v calls=%d elapsed=%s", err, calls, time.Since(start))
	}
}

func TestCancelledToolBatchIsCompleteAndFullOutputRetained(t *testing.T) {
	p := settingsProvider()
	p.complete = func(CompletionRequest) (*Response, error) {
		return toolResponse(bashCall("first", `head -c 100000 /dev/zero | tr '\0' x`), bashCall("skipped", "touch should-not-exist")), nil
	}
	a, err := NewAgentIn(nil, []Provider{p}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []AgentResponse
	a.SetOutput(func(event AgentResponse) {
		if event.ResponseType == "tool_result" {
			events = append(events, event)
			cancel()
		}
	})
	if err := a.TurnContext(ctx, "run"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ToolCallID != "first" || events[0].ToolName != "bash" || len(events[0].FullToolOutput) != 100000 {
		t.Fatalf("tool events missing full output (%d events)", len(events))
	}
	if len(events[0].Content) > MaxToolOutputBytes+256 || !strings.Contains(events[0].Content, "truncated") {
		t.Fatal("provider output not capped")
	}
	if !strings.Contains(events[1].FullToolOutput, "cancelled before execution") {
		t.Fatal(events[1])
	}
	if _, err := os.Stat(filepath.Join(a.WorkingDir(), "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled tool executed")
	}
	if err := validateConversation(a.Messages()); err != nil {
		t.Fatal(err)
	}
	p.complete = func(req CompletionRequest) (*Response, error) {
		if err := validateConversation(req.Messages); err != nil {
			return nil, err
		}
		return &Response{Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant"}}}}, nil
	}
	if err := a.Turn("continue"); err != nil {
		t.Fatal(err)
	}
}

func TestConversationCheckpointRoundTrip(t *testing.T) {
	a := NewAgent(nil, []Provider{settingsProvider()})
	a.messages = append(a.messages, Message{Role: "assistant", sourceModel: "test/a", ReasoningDetails: ReasoningDetails{json.RawMessage(`{"type":"opaque"}`)}, ToolCalls: []ToolCall{bashCall("one", "pwd")}}, Message{Role: "tool", ToolCallId: "one", Content: TextContent("done")})
	raw, err := json.Marshal(a.ExportConversation())
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint []Message
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	b := NewAgent(nil, []Provider{settingsProvider()})
	if err := b.RestoreConversation(checkpoint); err != nil {
		t.Fatal(err)
	}
	got := messagesForModel(b.Messages(), "test/a")
	if len(got[1].ReasoningDetails) != 1 || got[1].SourceModel != "" {
		t.Fatal("reasoning provenance lost or leaked to provider")
	}
	if len(messagesForModel(b.Messages(), "test/b")[1].ReasoningDetails) != 0 {
		t.Fatal("reasoning leaked across models")
	}
	checkpoint[1].ToolCalls[0].Id = "mutated"
	checkpoint[1].ReasoningDetails[0][0] = 'x'
	if b.Messages()[1].ToolCalls[0].Id != "one" || !json.Valid(b.Messages()[1].ReasoningDetails[0]) {
		t.Fatal("restore shares checkpoint memory")
	}
	before := b.Messages()
	for _, invalid := range [][]Message{nil, before[:2], append(cloneMessages(before), Message{Role: "tool", ToolCallId: "orphan"})} {
		if err := b.RestoreConversation(invalid); err == nil {
			t.Fatal("accepted invalid checkpoint")
		}
		if !reflect.DeepEqual(before, b.Messages()) {
			t.Fatal("failed restore mutated runtime")
		}
	}
	dir, home := t.TempDir(), t.TempDir()
	if err := b.SetIdentity("controlplane-id", dir, dir, home); err != nil {
		t.Fatal(err)
	}
	if b.ThreadID() != "controlplane-id" || b.WorkingDir() != dir || b.ProjectRoot() != dir || b.home != home {
		t.Fatal("identity not restored")
	}
	if err := b.SetIdentity("../escape", dir, dir, home); err == nil {
		t.Fatal("unsafe identity accepted")
	}
}

type runtimeTransport func(*http.Request) (*http.Response, error)

func (f runtimeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderHTTPRequestCancellation(t *testing.T) {
	for _, name := range []string{"openrouter", "codex", "codex-refresh"} {
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			stopped := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Consume the request so the server can observe a client disconnect.
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(stopped)
			}))
			defer server.Close()
			client := &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				target, _ := http.NewRequest(http.MethodPost, server.URL, nil)
				r.URL = target.URL
				return http.DefaultTransport.RoundTrip(r)
			})}
			var p Provider
			if name == "openrouter" {
				o := NewOpenRouterProvider("test")
				o.client = client
				p = o
			} else {
				path := filepath.Join(t.TempDir(), "auth.json")
				auth := `{"tokens":{"access_token":"test","refresh_token":"refresh","account_id":"acct"}}`
				if name == "codex-refresh" {
					auth = `{"last_refresh":"2000-01-01T00:00:00Z","tokens":{"access_token":"test","refresh_token":"refresh","account_id":"acct"}}`
				}
				if err := os.WriteFile(path, []byte(auth), 0600); err != nil {
					t.Fatal(err)
				}
				store := NewCodexAuthStore(path)
				store.client = client
				c := NewCodexProvider(store)
				c.client = client
				p = c
			}
			a := NewAgent(nil, []Provider{p})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- a.TurnContext(ctx, "hello") }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("HTTP request did not start")
			}
			cancel()
			if err := awaitTurn(t, done); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("HTTP request not cancelled")
			}
		})
	}
}

func TestAssistantTextWithToolCallsEmitted(t *testing.T) {
	p := settingsProvider()
	calls := 0
	p.complete = func(CompletionRequest) (*Response, error) {
		calls++
		if calls == 1 {
			res := toolResponse(bashCall("one", "printf done"))
			res.Choices[0].Message.Content = TextContent("I will check.")
			return res, nil
		}
		return &Response{Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant", Content: TextContent("All done.")}}}}, nil
	}
	a := NewAgent(nil, []Provider{p})
	var blocks []string
	a.SetOutput(func(event AgentResponse) {
		if event.ResponseType == "agent" {
			blocks = append(blocks, event.Content)
		}
	})
	if err := a.Turn("check"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(blocks, []string{"I will check.", "All done."}) {
		t.Fatal(blocks)
	}
}

func TestCodexAuthLockWaitCancellation(t *testing.T) {
	store := NewCodexAuthStore("not-read")
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, refresh := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		start := time.Now()
		var err error
		if refresh {
			_, err = store.RefreshContext(ctx, zap.NewNop())
		} else {
			_, err = store.CredentialsContext(ctx, zap.NewNop())
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatal(err, time.Since(start))
		}
	}
}

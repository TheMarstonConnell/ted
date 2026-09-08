package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestResponsesInputMapsEachRole(t *testing.T) {
	replayed := json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)
	messages := []Message{
		{Role: "system", Content: TextContent("be terse")},
		{Role: "user", Content: TextContent("list files")},
		{
			Role:             "assistant",
			Content:          TextContent(""),
			ReasoningDetails: ReasoningDetails{replayed},
			ToolCalls:        []ToolCall{{Id: "call_1", Function: FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}}},
		},
		{Role: "tool", ToolCallId: "call_1", Content: TextContent("a.go")},
		{
			Role:      "assistant",
			Content:   TextContent("done"),
			ToolCalls: []ToolCall{{Id: "call_2", Function: FunctionCall{Name: "bash", Arguments: `{"command":"pwd"}`}}},
		},
	}

	instructions, input, err := buildResponsesInput(messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if instructions != "be terse" {
		t.Fatalf("instructions = %q", instructions)
	}

	types := make([]string, 0, len(input))
	for _, raw := range input {
		var item struct {
			ItemType string `json:"type"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("could not parse item %s: %v", raw, err)
		}
		types = append(types, item.ItemType)
	}
	want := "message reasoning function_call_output message function_call"
	if got := strings.Join(types, " "); got != want {
		t.Fatalf("item types = %q, want %q", got, want)
	}

	if string(input[1]) != string(replayed) {
		t.Fatalf("codex output item was not replayed verbatim: %s", input[1])
	}
}

func TestResponsesStreamReturnsCompletedResponse(t *testing.T) {
	stream := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"ls\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-6-astra","status":"completed"}}`,
		``,
	}, "\n")

	completed, err := readResponsesStream(zap.NewNop(), strings.NewReader(stream))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res, err := buildChatResponse(completed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	choice := res.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 || choice.Message.ToolCalls[0].Id != "call_1" {
		t.Fatalf("tool calls = %+v", choice.Message.ToolCalls)
	}
	if len(choice.Message.ReasoningDetails) != 2 {
		t.Fatalf("expected both output items retained for replay, got %d", len(choice.Message.ReasoningDetails))
	}
}

func TestResponsesStreamReportsFailure(t *testing.T) {
	stream := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"quota exhausted\"}}}\n\n"

	_, err := readResponsesStream(zap.NewNop(), strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("expected failure to be reported, got %v", err)
	}
}

// TestCodexProviderLive exercises the real backend with the local Codex login.
// It runs only when HARNESS_LIVE_CODEX is set, because it spends the user's
// quota and needs network access.
func TestCodexProviderLive(t *testing.T) {
	if os.Getenv("HARNESS_LIVE_CODEX") == "" {
		t.Skip("set HARNESS_LIVE_CODEX=1 to run against the codex backend")
	}

	path, err := DefaultCodexAuthPath()
	if err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(NewCodexAuthStore(path))

	logger, _ := zap.NewDevelopment()
	res, err := provider.Complete(logger, CompletionRequest{Model: "codex/gpt-6-astra", Messages: []Message{
		{Role: "system", Content: TextContent("Reply with exactly the word pong. Do not use tools.")},
		{Role: "user", Content: TextContent("ping")},
	}})
	if err != nil {
		t.Fatalf("completion failed: %v", err)
	}

	msg := res.Choices[0].Message
	t.Logf("model=%s finish=%s text=%q items=%d", res.Model, res.Choices[0].FinishReason, msg.Content.Text(), len(msg.ReasoningDetails))
	if !strings.Contains(strings.ToLower(msg.Content.Text()), "pong") {
		t.Fatalf("unexpected reply %q", msg.Content.Text())
	}
}

// TestCodexProviderLiveToolRoundTrip drives a full agent turn so the reasoning
// and tool-call items from the first response are replayed on the second.
func TestCodexProviderLiveToolRoundTrip(t *testing.T) {
	if os.Getenv("HARNESS_LIVE_CODEX") == "" {
		t.Skip("set HARNESS_LIVE_CODEX=1 to run against the codex backend")
	}

	path, err := DefaultCodexAuthPath()
	if err != nil {
		t.Fatal(err)
	}
	logger, _ := zap.NewDevelopment()
	agent := NewAgent(logger, []Provider{NewCodexProvider(NewCodexAuthStore(path))})

	var replies []AgentResponse
	agent.SetOutput(func(res AgentResponse) { replies = append(replies, res) })

	if err := agent.Turn("Run the bash command `echo harness-round-trip` and reply with only its output."); err != nil {
		t.Fatalf("turn failed: %v", err)
	}

	sawTool := false
	final := ""
	for _, reply := range replies {
		if reply.ResponseType == "tool" {
			sawTool = true
		}
		if reply.ResponseType == "agent" {
			final = reply.Content
		}
	}
	t.Logf("messages=%d tool_used=%v final=%q", len(agent.Messages()), sawTool, final)
	if !sawTool || !strings.Contains(final, "harness-round-trip") {
		t.Fatalf("expected a tool call and its output in the reply; tool_used=%v final=%q", sawTool, final)
	}
}

func TestResponsesInputTranslatesUserImages(t *testing.T) {
	content := imageContent([]screenshotImage{{MIMEType: "image/png", Data: []byte("png bytes")}})
	_, input, err := buildResponsesInput([]Message{{Role: "user", Content: content}})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != 1 {
		t.Fatalf("input items = %d", len(input))
	}
	var item struct {
		Content []struct {
			Type     string `json:"type"`
			ImageURL string `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(input[0], &item); err != nil {
		t.Fatal(err)
	}
	if len(item.Content) != 1 || item.Content[0].Type != "input_image" || !strings.HasPrefix(item.Content[0].ImageURL, "data:image/png;base64,") {
		t.Fatalf("unexpected Codex image translation: %s", input[0])
	}
}

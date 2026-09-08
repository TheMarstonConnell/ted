package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// toolCallResponse is an assistant turn of the shape a reasoning model
// returns when it decides to call a tool: a null body, a readable reasoning
// summary, and reasoning records that carry opaque provider fields.
const toolCallResponse = `{
  "id": "gen-1",
  "choices": [
    {
      "index": 0,
      "finish_reason": "tool_calls",
      "message": {
        "role": "assistant",
        "content": null,
        "reasoning": "I should list the directory.",
        "reasoning_details": [
          {"type": "reasoning.text", "text": "List the directory first.", "signature": "sig-abc", "index": 0},
          {"type": "reasoning.encrypted", "data": "ZW5jcnlwdGVk", "format": "anthropic-claude-v1"}
        ],
        "tool_calls": [
          {"type": "function", "index": 0, "id": "call_1", "function": {"name": "bash", "arguments": "{\"command\":\"ls\"}"}}
        ]
      }
    }
  ]
}`

func TestAssistantMessageSurvivesRoundTrip(t *testing.T) {
	var res Response
	if err := json.Unmarshal([]byte(toolCallResponse), &res); err != nil {
		t.Fatalf("could not parse response: %v", err)
	}

	msg := res.Choices[0].Message
	if got := msg.Content.Text(); got != "" {
		t.Errorf("null body should read as empty text, got %q", got)
	}
	if len(msg.ReasoningDetails) != 2 {
		t.Fatalf("expected 2 reasoning records, got %d", len(msg.ReasoningDetails))
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("could not encode message: %v", err)
	}

	for _, want := range []string{
		`"content":null`,
		`"reasoning":"I should list the directory."`,
		`"signature":"sig-abc"`,
		`"data":"ZW5jcnlwdGVk"`,
		`"format":"anthropic-claude-v1"`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("re-encoded message lost %s\ngot: %s", want, encoded)
		}
	}
}

func TestContentPartsReadAsTextAndReplayIntact(t *testing.T) {
	const raw = `{"role":"assistant","content":[{"type":"text","text":"first "},{"type":"image_url","image_url":{"url":"https://example.invalid/a.png"}},{"type":"text","text":"second"}]}`

	var msg Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("could not parse message: %v", err)
	}

	if got, want := msg.Content.Text(), "first second"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("could not encode message: %v", err)
	}
	if !strings.Contains(string(encoded), `"image_url"`) {
		t.Errorf("re-encoded message dropped the image part\ngot: %s", encoded)
	}
}

func TestComposedMessageEncodesAsPlainString(t *testing.T) {
	encoded, err := json.Marshal(Message{Role: "user", Content: TextContent("hello")})
	if err != nil {
		t.Fatalf("could not encode message: %v", err)
	}

	if want := `{"role":"user","content":"hello"}`; string(encoded) != want {
		t.Errorf("encoded = %s, want %s", encoded, want)
	}
}

func TestStringBodyReadsAsText(t *testing.T) {
	var msg Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"done"}`), &msg); err != nil {
		t.Fatalf("could not parse message: %v", err)
	}

	if got, want := msg.Content.Text(), "done"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

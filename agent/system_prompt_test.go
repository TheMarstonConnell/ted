package agent

import (
	"os"
	"strings"
	"testing"
)

func TestEmbeddedSystemPrompt(t *testing.T) {
	contents, err := os.ReadFile("system_prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(SYSTEM_PROMPT) == "" {
		t.Fatal("embedded system prompt is empty")
	}
	if SYSTEM_PROMPT != string(contents) {
		t.Fatal("embedded system prompt does not match system_prompt.md")
	}

	messages := NewAgent(nil, nil).Messages()
	if len(messages) != 1 || messages[0].Role != "system" || messages[0].Content.Text() != SYSTEM_PROMPT {
		t.Fatalf("new agent did not initialize with the embedded system prompt: %+v", messages)
	}
}

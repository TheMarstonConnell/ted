package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

const SYSTEM_PROMPT = "you are Ted, a friendly coding assistant. You can run bash commands. You have no other tools available. Use bash to do everything including reading & writing files."

const OPENROUTER_API = "https://openrouter.ai/api/v1/chat/completions"

type Function struct {
	Description string         `json:"description,omitempty"`
	Name        string         `json:"name"`
	Parameters  map[string]any `json:"parameters"`
}

type Tool struct {
	ToolType string   `json:"type"`
	Function Function `json:"function"`
}

type Choice struct {
	Index              uint64  `json:"index"`
	FinishReason       string  `json:"finish_reason"`
	NativeFinishReason string  `json:"native_finish_reason"`
	Message            Message `json:"message"`
}

type Response struct {
	Id       string   `json:"id"`
	Object   string   `json:"object"`
	Created  uint64   `json:"created"`
	Model    string   `json:"model"`
	Provider string   `json:"provider"`
	Choices  []Choice `json:"choices"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ToolCallType string       `json:"type"`
	Index        uint64       `json:"index"`
	Id           string       `json:"id"`
	Function     FunctionCall `json:"function"`
}

// Content holds the body of a chat message. The API represents a body as a
// plain string, as an array of typed parts, or as null, and a message the
// model produced must be handed back in the form it arrived. The original
// JSON is therefore retained and replayed unaltered, while Text reports the
// readable portion for display.
type Content struct {
	raw  json.RawMessage
	text string
}

// TextContent builds a plain string body for a message the harness composes
// itself, such as the system prompt, user input, or a tool result.
func TextContent(text string) Content {
	return Content{text: text}
}

// Text reports the readable portion of the body. Parts that carry no text,
// such as images, contribute nothing.
func (c Content) Text() string {
	return c.text
}

func (c Content) MarshalJSON() ([]byte, error) {
	if c.raw != nil {
		return c.raw, nil
	}
	return json.Marshal(c.text)
}

func (c *Content) UnmarshalJSON(data []byte) error {
	c.raw = append(json.RawMessage(nil), data...)
	c.text = ""

	if string(data) == "null" {
		return nil
	}

	var plain string
	if err := json.Unmarshal(data, &plain); err == nil {
		c.text = plain
		return nil
	}

	var parts []struct {
		PartType string `json:"type"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(data, &parts); err != nil {
		return fmt.Errorf("could not parse message content %w", err)
	}

	var readable strings.Builder
	for _, part := range parts {
		if part.PartType == "text" {
			readable.WriteString(part.Text)
		}
	}
	c.text = readable.String()

	return nil
}

// ReasoningDetails carries the extended thinking records that accompany an
// assistant message. Its elements form a union whose variants hold opaque
// provider data, such as encrypted payloads and signatures, that the provider
// validates when the conversation continues. The records are therefore held
// as raw JSON and returned unaltered on the following request; dropping them
// costs the model its reasoning chain across a sequence of tool calls.
type ReasoningDetails []json.RawMessage

type Message struct {
	sourceModel      string
	Role             string           `json:"role"`
	Content          Content          `json:"content"`
	Reasoning        string           `json:"reasoning,omitempty"`
	ReasoningDetails ReasoningDetails `json:"reasoning_details,omitempty"`
	ToolCalls        []ToolCall       `json:"tool_calls,omitempty"`
	ToolCallId       string           `json:"tool_call_id,omitempty"`
}

type CompletionBody struct {
	Reasoning *ReasoningOptions `json:"reasoning,omitempty"`
	Model     string            `json:"model"`
	Messages  []Message         `json:"messages"`
	Tools     []Tool            `json:"tools"`
}

// ReasoningOptions is the OpenRouter reasoning configuration.
type ReasoningOptions struct {
	Effort Effort `json:"effort"`
}

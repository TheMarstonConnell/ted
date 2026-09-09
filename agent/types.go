package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

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
	Usage    *TokenUsage `json:"usage,omitempty"`
	Id       string      `json:"id"`
	Object   string      `json:"object"`
	Created  uint64      `json:"created"`
	Model    string      `json:"model"`
	Provider string      `json:"provider"`
	Choices  []Choice    `json:"choices"`
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

// imageContent builds a multimodal body containing in-memory images. The
// bytes, rather than their filesystem names, are sent to providers so a model
// cannot use this mechanism to request arbitrary local files.
func imageContent(images []screenshotImage) Content {
	parts := make([]map[string]any, 0, len(images))
	for _, image := range images {
		mime := image.MIMEType
		if mime == "" {
			mime = "image/png"
		}
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]string{
				"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
			},
		})
	}
	raw, _ := json.Marshal(parts) // maps above contain only JSON-safe values
	return Content{raw: raw}
}

// screenshotImage is an image body supplied as multimodal user input.
type screenshotImage struct {
	MIMEType string
	Data     []byte
}

// Text reports the readable portion of the body. Parts that carry no text,
// such as images, contribute nothing.
func (c Content) Text() string {
	return c.text
}

// imageURLs returns image URLs from an array-form body. It is intentionally
// private: callers should compose images from bytes via imageContent.
func (c Content) imageURLs() []string {
	if len(c.raw) == 0 {
		return nil
	}
	var parts []struct {
		PartType string          `json:"type"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if json.Unmarshal(c.raw, &parts) != nil {
		return nil
	}
	var urls []string
	for _, part := range parts {
		if part.PartType != "image_url" {
			continue
		}
		var object struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(part.ImageURL, &object) == nil && object.URL != "" {
			urls = append(urls, object.URL)
			continue
		}
		var direct string
		if json.Unmarshal(part.ImageURL, &direct) == nil && direct != "" {
			urls = append(urls, direct)
		}
	}
	return urls
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
	// SourceModel preserves reasoning provenance in exported checkpoints. It is
	// removed from provider requests by messagesForModel.
	SourceModel      string `json:"source_model,omitempty"`
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

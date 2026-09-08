package main

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

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallId string     `json:"tool_call_id,omitempty"`
}

type CompletionBody struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools"`
}

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

func makeBashTool() Tool {
	t := Tool{
		ToolType: "function",
		Function: Function{
			Description: "Calls bash. Commands time out after two minutes; partial output is returned on timeout.",
			Name:        "bash",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The bash command to execute.",
					},
				},
				"required": []string{"command"},
			},
		},
	}
	return t
}

// Agent is an instance-local conversation. One turn may run at a time.
// Settings and output registration are safe to access concurrently.
type Agent struct {
	mu        sync.Mutex
	settings  Settings
	messages  []Message
	busy      bool
	providers []Provider
	logger    *zap.Logger
	respond   func(AgentResponse)
}

func (a *Agent) emit(response AgentResponse) {
	a.mu.Lock()
	output := a.respond
	a.mu.Unlock()
	if output != nil {
		output(response)
	}
}

// runToolCall carries out one tool call and reports the text to hand back to
// the model. A failure is reported as result text rather than as an error:
// the model reads the message and can correct itself on the next request,
// whereas abandoning the call would leave it unanswered and make every later
// request invalid.
func (a *Agent) runToolCall(toolCall ToolCall) string {
	a.logger.Debug("running tool call",
		zap.String("tool_call_id", toolCall.Id),
		zap.String("function", toolCall.Function.Name),
		zap.String("arguments", toolCall.Function.Arguments),
	)

	if toolCall.Function.Name != "bash" {
		a.logger.Debug("tool call named an unavailable tool",
			zap.String("tool_call_id", toolCall.Id),
			zap.String("function", toolCall.Function.Name),
		)
		return fmt.Sprintf("error: there is no tool named %q; bash is the only tool available", toolCall.Function.Name)
	}

	var bashArgs map[string]string
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &bashArgs); err != nil {
		a.logger.Debug("could not parse tool call arguments",
			zap.String("tool_call_id", toolCall.Id),
			zap.Error(err),
		)
		return fmt.Sprintf("error: could not parse the arguments as JSON: %s", err)
	}

	command := bashArgs["command"]
	a.emit(
		AgentResponse{
			Content:      fmt.Sprintf("Ran shell command - %q", command),
			ResponseType: "tool"},
	)

	result := runBash(command, DefaultToolTimeout)

	a.logger.Debug("tool call finished",
		zap.String("tool_call_id", toolCall.Id),
		zap.String("command", command),
		zap.String("result", result),
	)

	return result
}

// Turn sends the user input to the model and runs the tool calls it asks for
// until it produces a final reply.
//
// A turn either completes and extends the conversation or leaves it exactly
// as it was. The API requires every tool call in an assistant message to be
// answered by a tool message carrying the same identifier, so a turn
// abandoned halfway would strand an unanswered tool call and every later
// request would be rejected. An abandoned turn is therefore rolled back.
func (a *Agent) Turn(userInput string) (err error) {

	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return ErrBusy
	}
	settings := a.settings
	var provider Provider
	for _, p := range a.providers {
		if p.Name() == settings.Provider {
			provider = p
			break
		}
	}
	if provider == nil {
		a.mu.Unlock()
		return errors.New("no model available")
	}
	a.busy = true
	messages := cloneMessages(a.messages)
	a.mu.Unlock()
	completed := false
	defer func() {
		a.mu.Lock()
		if completed {
			a.messages = messages
		}
		a.busy = false
		a.mu.Unlock()
	}()
	messages = append(messages, Message{Role: "user", Content: TextContent(userInput)})

	for {
		res, completionErr := provider.Complete(a.logger, CompletionRequest{Model: settings.Model, Effort: settings.Effort, Messages: messagesForModel(messages, settings.Model)})
		if completionErr != nil {
			return fmt.Errorf("completion failed %w", completionErr)
		}

		if res == nil || len(res.Choices) == 0 {
			return fmt.Errorf("completion returned no choices")
		}

		choice := res.Choices[0]

		a.logger.Debug("parsed completion choice",
			zap.String("response_id", res.Id),
			zap.String("provider", res.Provider),
			zap.String("finish_reason", choice.FinishReason),
			zap.Int("tool_call_count", len(choice.Message.ToolCalls)),
			zap.Int("reasoning_detail_count", len(choice.Message.ReasoningDetails)),
		)

		msg := choice.Message
		msg.sourceModel = settings.Model
		messages = append(messages, msg)

		finishReason := choice.FinishReason
		if finishReason == "tool_calls" {
			// A turn that reports tool calls but carries none would be sent
			// back unchanged on the next pass and loop without end.
			if len(msg.ToolCalls) == 0 {
				return fmt.Errorf("completion reported tool calls but sent none")
			}

			// Each pass appends exactly one result and never leaves the
			// loop early, so no tool call can go unanswered.
			for i := 0; i < len(msg.ToolCalls); i++ {
				toolCall := msg.ToolCalls[i]

				messages = append(messages, Message{
					Role:       "tool",
					ToolCallId: toolCall.Id,
					Content:    TextContent(a.runToolCall(toolCall)),
				})
			}

		} else {
			a.logger.Debug("assistant turn complete", zap.String("finish_reason", finishReason))
			a.emit(
				AgentResponse{
					Content:      msg.Content.Text(),
					ResponseType: "agent"},
			)
			break
		}
	}
	completed = true
	return nil
}

// Ready reports whether another turn or settings change can start.
func (a *Agent) Ready() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.busy
}

// SetOutput installs an optional synchronous event callback. Callbacks should
// return promptly; they may inspect settings and committed history.
func (a *Agent) SetOutput(respond func(AgentResponse)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.respond = respond
}

type AgentResponse struct {
	Content      string
	ResponseType string
}

// NewAgent does not read environment variables or require an output callback.
// A nil logger discards diagnostics. Providers are tried in supplied order.
func NewAgent(logger *zap.Logger, providers []Provider) *Agent {
	if logger == nil {
		logger = zap.NewNop()
	}
	a := &Agent{
		logger:    logger,
		providers: append([]Provider(nil), providers...),
		messages:  []Message{{Role: "system", Content: TextContent(SYSTEM_PROMPT)}},
	}
	if models := a.ListModels(); len(models) > 0 {
		_, _ = a.SetModel(models[0].ID)
	}
	return a
}

// Messages returns a detached snapshot of committed conversation history.
// An active turn becomes visible only after it succeeds.
func (a *Agent) Messages() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneMessages(a.messages)
}

func cloneMessages(messages []Message) []Message {
	result := append([]Message(nil), messages...)
	for i := range result {
		result[i].Content.raw = append(json.RawMessage(nil), messages[i].Content.raw...)
		result[i].ToolCalls = append([]ToolCall(nil), messages[i].ToolCalls...)
		result[i].ReasoningDetails = nil
		for _, raw := range messages[i].ReasoningDetails {
			result[i].ReasoningDetails = append(result[i].ReasoningDetails, append(json.RawMessage(nil), raw...))
		}
	}
	return result
}

// Opaque reasoning belongs to the model that produced it. Keep it in stored
// history, but do not send it to a different model/provider. Text and tool
// exchanges remain available when switching models.
func messagesForModel(messages []Message, model string) []Message {
	result := cloneMessages(messages)
	for i := range result {
		if result[i].sourceModel != model {
			result[i].ReasoningDetails = nil
			result[i].Reasoning = ""
		}
	}
	return result
}

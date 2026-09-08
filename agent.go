package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

func makeBashTool() Tool {
	t := Tool{
		ToolType: "function",
		Function: Function{
			Description: "calls bash",
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

func (a *Agent) Complete(logger *zap.Logger) (*Response, error) {
	currentProvider := a.getCurrentProvider()
	if currentProvider == nil {
		return nil, errors.New("could not load current provider")
	}
	return currentProvider.Complete(logger, a.Model, a.Messages)
}

type Agent struct {
	Model     string
	Messages  []Message
	ready     bool
	providers []Provider
	logger    *zap.Logger
	respond   func(AgentResponse)
}

func (a *Agent) getCurrentProvider() Provider {
	for _, provider := range a.providers {
		providerName := fmt.Sprintf("%s/", provider.Name())

		if strings.HasPrefix(a.Model, providerName) {
			return provider
		}
	}
	return nil
}

func (a *Agent) verifyModel() bool {
	for _, provider := range a.providers {
		modelList := provider.ListModels()
		providerName := fmt.Sprintf("%s/", provider.Name())
		a.logger.Debug("available models", zap.String("provider name", providerName), zap.Strings("model list", modelList))
		modelName := strings.TrimPrefix(a.Model, providerName)

		for _, model := range modelList {
			if modelName == model {
				return true
			}
		}
	}
	return false
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
	a.respond(
		AgentResponse{
			Content:      "Ran shell command",
			ResponseType: "tool"},
	)

	cmd := exec.Command("bash", "-c", command)
	out, err := cmd.CombinedOutput()
	result := string(out) // should add a cap on this
	if err != nil {
		result = fmt.Sprintf("%s\n%s", result, err)
	}

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

	if !a.verifyModel() {
		return fmt.Errorf("%s is not a valid model", a.Model)
	}

	committed := len(a.Messages)
	defer func() {
		if err == nil {
			return
		}
		a.logger.Debug("discarding incomplete turn",
			zap.Int("discarded_messages", len(a.Messages)-committed),
		)
		clear(a.Messages[committed:])
		a.Messages = a.Messages[:committed]
	}()

	a.Messages = append(a.Messages, Message{
		Role:    "user",
		Content: TextContent(userInput),
	})

	for {
		res, completionErr := a.Complete(a.logger)
		if completionErr != nil {
			return fmt.Errorf("completion failed %w", completionErr)
		}

		if len(res.Choices) == 0 {
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
		a.Messages = append(a.Messages, msg)

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

				a.Messages = append(a.Messages, Message{
					Role:       "tool",
					ToolCallId: toolCall.Id,
					Content:    TextContent(a.runToolCall(toolCall)),
				})
			}

		} else {
			a.logger.Debug("assistant turn complete", zap.String("finish_reason", finishReason))
			a.respond(
				AgentResponse{
					Content:      msg.Content.Text(),
					ResponseType: "agent"},
			)
			break
		}
	}
	return nil
}

func (a *Agent) Ready() bool {
	return a.ready
}

func (a *Agent) SetOutput(respond func(AgentResponse)) {
	a.respond = respond
}

type AgentResponse struct {
	Content      string
	ResponseType string
}

func NewAgent(logger *zap.Logger, providers []Provider) *Agent {

	a := Agent{
		Model: "openrouter/openai/gpt-5.6-luna",
		Messages: []Message{
			{
				Role:    "system",
				Content: TextContent(SYSTEM_PROMPT),
			},
		},
		ready:     true,
		logger:    logger,
		providers: providers,
	}

	return &a
}

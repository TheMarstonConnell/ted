package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"

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

func (a *Agent) Complete(logger *zap.Logger, openRouterKey string) (*Response, error) {
	client := &http.Client{}

	compBod := CompletionBody{
		Model:    a.Model,
		Messages: a.Messages,
		Tools:    []Tool{makeBashTool()},
	}

	bodyData, err := json.Marshal(compBod)
	if err != nil {
		return nil, fmt.Errorf("could not build completion body data %w", err)
	}

	req, err := http.NewRequest("POST", OPENROUTER_API, bytes.NewBuffer(bodyData))
	if err != nil {
		return nil, fmt.Errorf("failed to build completion request %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", openRouterKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://marston.dev")
	req.Header.Set("X-Title", "Marston Connell Harness Engineering")

	logger.Debug("sending completion request",
		zap.String("endpoint", OPENROUTER_API),
		zap.String("model", a.Model),
		zap.Int("message_count", len(a.Messages)),
	)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not complete request %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read body %w", err)
	}

	logger.Debug("received completion response",
		zap.Int("status_code", resp.StatusCode),
		zap.String("body", string(body)),
	)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("completion request returned status %d: %s", resp.StatusCode, body)
	}

	res := Response{}
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, fmt.Errorf("could not parse response %w", err)
	}

	return &res, nil
}

type Agent struct {
	Model         string
	Messages      []Message
	ready         bool
	openRouterKey string
	logger        *zap.Logger
}

func (a *Agent) Turn(userInput string) error {
	a.Messages = append(a.Messages, Message{
		Role:    "user",
		Content: userInput,
	})

	for {
		res, err := a.Complete(a.logger, a.openRouterKey)
		if err != nil {
			return fmt.Errorf("completion failed %w", zap.Error(err))
		}

		if len(res.Choices) == 0 {
			return fmt.Errorf("completion returned no choices %w", zap.String("response_id", res.Id))
		}

		choice := res.Choices[0]

		a.logger.Debug("parsed completion choice",
			zap.String("response_id", res.Id),
			zap.String("provider", res.Provider),
			zap.String("finish_reason", choice.FinishReason),
			zap.Int("tool_call_count", len(choice.Message.ToolCalls)),
		)

		msg := choice.Message
		a.Messages = append(a.Messages, msg)

		finishReason := choice.FinishReason
		if finishReason == "tool_calls" {
			for i := 0; i < len(msg.ToolCalls); i++ {
				toolCall := msg.ToolCalls[i]

				a.logger.Debug("running tool call",
					zap.String("tool_call_id", toolCall.Id),
					zap.String("function", toolCall.Function.Name),
					zap.String("arguments", toolCall.Function.Arguments),
				)

				args := toolCall.Function.Arguments
				var bashArgs map[string]string
				if err := json.Unmarshal([]byte(args), &bashArgs); err != nil {
					return fmt.Errorf("could not parse tool call arguments %w", zap.Error(err))
				}
				command := bashArgs["command"]
				fmt.Println("Ran shell command") // output

				cmd := exec.Command("bash", "-c", command)
				out, err := cmd.CombinedOutput()
				result := string(out)
				if err != nil {
					result = fmt.Sprintf("%s\n%s", result, err)
				}

				a.logger.Debug("tool call finished",
					zap.String("tool_call_id", toolCall.Id),
					zap.String("command", command),
					zap.String("result", result),
				)

				a.Messages = append(a.Messages, Message{
					Role:       "tool",
					ToolCallId: toolCall.Id,
					Content:    result,
				})
			}

		} else {
			a.logger.Debug("assistant turn complete", zap.String("finish_reason", finishReason))
			fmt.Printf("> %s\n", msg.Content) // output
			break
		}
	}
	return nil
}

func (a *Agent) Ready() bool {
	return a.ready
}

func NewAgent(logger *zap.Logger, openRouterKey string) *Agent {

	a := Agent{
		Model: "openai/gpt-5.6-luna",
		Messages: []Message{
			{
				Role:    "system",
				Content: SYSTEM_PROMPT,
			},
		},
		ready:         true,
		openRouterKey: openRouterKey,
		logger:        logger,
	}

	return &a
}

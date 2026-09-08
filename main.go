package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/joho/godotenv"
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

func completion(logger *zap.Logger, openRouterKey string, model string, messages []Message) (*Response, error) {
	client := &http.Client{}

	compBod := CompletionBody{
		Model:    model,
		Messages: messages,
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
		zap.String("model", model),
		zap.Int("message_count", len(messages)),
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

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "could not load .env file: %v\n", err)
		os.Exit(1)
	}

	logger, err := newLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not build logger: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()

	openRouterKey := os.Getenv("OPENROUTER_API_KEY")
	if openRouterKey == "" {
		logger.Fatal("no openrouter key found", zap.String("variable", "OPENROUTER_API_KEY"))
	}

	modelChoice := "openai/gpt-5.6-luna"

	messages := []Message{
		{
			Role:    "system",
			Content: SYSTEM_PROMPT,
		},
	}

	logger.Debug("starting session", zap.String("model", modelChoice))

	input := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("< ")
		if !input.Scan() {
			break
		}

		userInput := strings.TrimSpace(input.Text())
		if userInput == "" {
			continue
		}

		if userInput == "exit" {
			fmt.Println("exiting...")
			os.Exit(0)
			return
		}

		logger.Debug("received user input", zap.Int("length", len(userInput)))

		messages = append(messages, Message{
			Role:    "user",
			Content: userInput,
		})

		for {
			res, err := completion(logger, openRouterKey, modelChoice, messages)
			if err != nil {
				logger.Fatal("completion failed", zap.Error(err))
			}

			if len(res.Choices) == 0 {
				logger.Fatal("completion returned no choices", zap.String("response_id", res.Id))
			}

			choice := res.Choices[0]

			logger.Debug("parsed completion choice",
				zap.String("response_id", res.Id),
				zap.String("provider", res.Provider),
				zap.String("finish_reason", choice.FinishReason),
				zap.Int("tool_call_count", len(choice.Message.ToolCalls)),
			)

			msg := choice.Message
			messages = append(messages, msg)

			finishReason := choice.FinishReason
			if finishReason == "tool_calls" {
				for i := 0; i < len(msg.ToolCalls); i++ {
					toolCall := msg.ToolCalls[i]

					logger.Debug("running tool call",
						zap.String("tool_call_id", toolCall.Id),
						zap.String("function", toolCall.Function.Name),
						zap.String("arguments", toolCall.Function.Arguments),
					)

					args := toolCall.Function.Arguments
					var bashArgs map[string]string
					if err := json.Unmarshal([]byte(args), &bashArgs); err != nil {
						logger.Fatal("could not parse tool call arguments",
							zap.String("tool_call_id", toolCall.Id),
							zap.String("arguments", args),
							zap.Error(err),
						)
					}
					command := bashArgs["command"]
					fmt.Println("Ran shell command")

					cmd := exec.Command("bash", "-c", command)
					out, err := cmd.CombinedOutput()
					result := string(out)
					if err != nil {
						result = fmt.Sprintf("%s\n%s", result, err)
					}

					logger.Debug("tool call finished",
						zap.String("tool_call_id", toolCall.Id),
						zap.String("command", command),
						zap.String("result", result),
					)

					messages = append(messages, Message{
						Role:       "tool",
						ToolCallId: toolCall.Id,
						Content:    result,
					})
				}

			} else {
				logger.Debug("assistant turn complete", zap.String("finish_reason", finishReason))
				fmt.Printf("> %s\n", msg.Content)
				break
			}
		}
	}

	if err := input.Err(); err != nil {
		logger.Fatal("could not read input", zap.Error(err))
	}
}

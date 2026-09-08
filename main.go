package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/joho/godotenv"
)

const SYSTEM_PROMPT = "you are a friendly coding assistant. You can run bash commands. You have no other tools available. Use bash to do everything including reading & writing files."

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

const OPENROUTER_API = "https://openrouter.ai/api/v1/chat/completions"

func completion(openRouterKey string, model string, messages []Message) (*Response, error) {
	client := &http.Client{}

	BashTool := Tool{
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

	compBod := CompletionBody{
		Model:    model,
		Messages: messages,
		Tools:    []Tool{BashTool},
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

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not complete request %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read body %w", err)
	}

	fmt.Println(string(body))

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("not 200 %s | %w", body, err)
	}

	res := Response{}
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, fmt.Errorf("could not parse response %w", err)
	}

	return &res, nil
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	openRouterKey := os.Getenv("OPENROUTER_API_KEY")
	if openRouterKey == "" {
		fmt.Println("no openrouter key found")
		os.Exit(0)
		return
	}

	modelChoice := "openai/gpt-5.6-luna"

	messages := []Message{
		{
			Role:    "system",
			Content: SYSTEM_PROMPT,
		},
	}

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

		messages = append(messages, Message{
			Role:    "user",
			Content: userInput,
		})

		for {
			res, err := completion(openRouterKey, modelChoice, messages)
			if err != nil {
				log.Fatal(err)
				return
			}

			if len(res.Choices) == 0 {
				log.Fatal("completion returned no choices")
			}

			fmt.Println(res)

			choice := res.Choices[0]

			msg := choice.Message
			messages = append(messages, msg)

			finishReason := choice.FinishReason
			if finishReason == "tool_calls" {
				for i := 0; i < len(msg.ToolCalls); i++ {
					toolCall := msg.ToolCalls[i]
					fmt.Println(toolCall.Function.Name, toolCall.Function.Arguments)
					args := toolCall.Function.Arguments
					var bashArgs map[string]string
					if err := json.Unmarshal([]byte(args), &bashArgs); err != nil {
						log.Fatal(err)
					}
					command := bashArgs["command"]

					cmd := exec.Command("bash", "-c", command)
					out, err := cmd.CombinedOutput()
					result := string(out)
					if err != nil {
						result = fmt.Sprintf("%s\n%s", result, err)
					}
					fmt.Println(result)

					messages = append(messages, Message{
						Role:       "tool",
						ToolCallId: toolCall.Id,
						Content:    result,
					})
				}

			} else {
				fmt.Printf("--%s--\n", finishReason)
				fmt.Printf("> %s\n", msg.Content)
				break
			}
		}

	}

	if err := input.Err(); err != nil {
		log.Fatalf("could not read input: %v", err)
	}
}

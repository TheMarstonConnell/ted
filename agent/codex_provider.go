package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// A ChatGPT login is not an API key. It is only accepted by the Codex backend,
// which speaks the Responses API rather than chat completions, so this
// provider translates the harness's chat-shaped conversation into Responses
// items on the way out and back into a chat-shaped message on the way in.
const (
	CODEX_RESPONSES_API = "https://chatgpt.com/backend-api/codex/responses"
	codexOriginator     = "codex_cli_rs"
)

var _ Provider = &CodexProvider{}

type CodexProvider struct {
	auth   *CodexAuthStore
	client *http.Client
}

func NewCodexProvider(auth *CodexAuthStore) *CodexProvider {
	return &CodexProvider{
		auth:   auth,
		client: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (c *CodexProvider) ListModels() []ModelInfo {
	return []ModelInfo{
		{ID: "gpt-6-astra", Efforts: []Effort{EffortLow, EffortMedium, EffortHigh}, DefaultEffort: EffortMedium},
		{ID: "gpt-5.6-sol", Efforts: []Effort{EffortLow, EffortMedium, EffortHigh}, DefaultEffort: EffortMedium},
		{ID: "gpt-5.6-terra", Efforts: []Effort{EffortLow, EffortMedium, EffortHigh}, DefaultEffort: EffortMedium},
		{ID: "gpt-5.6-luna", Efforts: []Effort{EffortLow, EffortMedium, EffortHigh}, DefaultEffort: EffortMedium},
	}
}

func (c *CodexProvider) Name() string {
	return "codex"
}

type responsesTool struct {
	ToolType    string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

type responsesReasoning struct {
	Effort Effort `json:"effort"`
}

// The backend insists on stream: true and store: false. Encrypted reasoning
// is requested so the model's chain of thought can be replayed across tool
// calls without the backend storing anything.
type responsesRequest struct {
	Model             string             `json:"model"`
	Instructions      string             `json:"instructions,omitempty"`
	Input             []json.RawMessage  `json:"input"`
	Tools             []responsesTool    `json:"tools"`
	ToolChoice        string             `json:"tool_choice"`
	ParallelToolCalls bool               `json:"parallel_tool_calls"`
	Reasoning         responsesReasoning `json:"reasoning"`
	Store             bool               `json:"store"`
	Stream            bool               `json:"stream"`
	Include           []string           `json:"include"`
}

func (c *CodexProvider) Complete(logger *zap.Logger, completion CompletionRequest) (*Response, error) {
	model, messages := completion.Model, completion.Messages
	effort := completion.Effort
	if effort == "" {
		effort = EffortMedium
	}
	modelName := strings.TrimPrefix(strings.TrimPrefix(model, c.Name()), "/")

	instructions, input, err := buildResponsesInput(messages)
	if err != nil {
		return nil, err
	}

	bashTool := makeBashTool()
	request := responsesRequest{
		Model:        modelName,
		Instructions: instructions,
		Input:        input,
		Tools: []responsesTool{{
			ToolType:    "function",
			Name:        bashTool.Function.Name,
			Description: bashTool.Function.Description,
			Parameters:  bashTool.Function.Parameters,
			Strict:      false,
		}},
		ToolChoice:        "auto",
		ParallelToolCalls: false,
		Reasoning:         responsesReasoning{Effort: effort},
		Store:             false,
		Stream:            true,
		Include:           []string{"reasoning.encrypted_content"},
	}

	bodyData, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("could not build responses body data %w", err)
	}

	if err := checkRequestSize(logger, len(bodyData)); err != nil {
		return nil, err
	}

	credentials, err := c.auth.Credentials(logger)
	if err != nil {
		return nil, err
	}

	logger.Info("sending responses request",
		zap.String("endpoint", CODEX_RESPONSES_API),
		zap.String("model", modelName),
		zap.Int("message_count", len(messages)),
		zap.Int("request_bytes", len(bodyData)),
		zap.Int("input_item_count", len(input)),
	)

	output, err := c.send(logger, bodyData, credentials)
	if errors.Is(err, errCodexUnauthorized) {
		// The store believed the token was valid; the backend disagreed.
		logger.Debug("codex backend rejected the access token, refreshing and retrying")
		credentials, err = c.auth.Refresh(logger)
		if err != nil {
			return nil, err
		}
		output, err = c.send(logger, bodyData, credentials)
	}
	if err != nil {
		return nil, err
	}

	return output, nil
}

var errCodexUnauthorized = errors.New("codex backend returned 401 unauthorized")

func (c *CodexProvider) send(logger *zap.Logger, bodyData []byte, credentials *CodexCredentials) (*Response, error) {
	req, err := http.NewRequest("POST", CODEX_RESPONSES_API, bytes.NewReader(bodyData))
	if err != nil {
		return nil, fmt.Errorf("failed to build responses request %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", credentials.AccessToken))
	req.Header.Set("ChatGPT-Account-ID", credentials.AccountId)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("originator", codexOriginator)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not complete request %w", err)
	}
	defer resp.Body.Close()
	logger.Info("model HTTP response", zap.Int("status_code", resp.StatusCode), zap.Int("request_bytes", len(bodyData)))

	if resp.StatusCode == http.StatusUnauthorized {
		io.Copy(io.Discard, resp.Body)
		return nil, errCodexUnauthorized
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Error("received responses error",
			zap.Int("status_code", resp.StatusCode),
			zap.String("body", string(body)),
		)
		return nil, &statusError{status: resp.StatusCode, err: fmt.Errorf("responses request returned status %d: %s", resp.StatusCode, body)}
	}

	completed, err := readResponsesStream(logger, resp.Body)
	if err != nil {
		return nil, err
	}

	return buildChatResponse(completed)
}

// buildResponsesInput maps the chat-shaped conversation onto Responses items.
//
// System messages become the request's instructions. An assistant message the
// Codex backend produced carries its original output items, held verbatim in
// ReasoningDetails, and those are replayed unchanged so the backend can
// validate the encrypted reasoning. An assistant message from any other
// provider is rebuilt from its text and tool calls.
func buildResponsesInput(messages []Message) (string, []json.RawMessage, error) {
	var instructions []string
	var input []json.RawMessage

	appendItem := func(item any) error {
		data, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("could not encode responses input item: %w", err)
		}
		input = append(input, data)
		return nil
	}

	for _, message := range messages {
		switch message.Role {
		case "system":
			instructions = append(instructions, message.Content.Text())

		case "user":
			var content []map[string]any
			if text := message.Content.Text(); text != "" {
				content = append(content, map[string]any{
					"type": "input_text",
					"text": text,
				})
			}
			for _, imageURL := range message.Content.imageURLs() {
				content = append(content, map[string]any{
					"type":      "input_image",
					"image_url": imageURL,
				})
			}
			// Preserve the old representation for an empty user message.
			if len(content) == 0 {
				content = append(content, map[string]any{
					"type": "input_text",
					"text": "",
				})
			}
			if err := appendItem(map[string]any{
				"type":    "message",
				"role":    "user",
				"content": content,
			}); err != nil {
				return "", nil, err
			}

		case "assistant":
			if len(message.ReasoningDetails) > 0 {
				for _, item := range message.ReasoningDetails {
					input = append(input, item)
				}
				continue
			}
			if text := message.Content.Text(); text != "" {
				if err := appendItem(map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{{
						"type": "output_text",
						"text": text,
					}},
				}); err != nil {
					return "", nil, err
				}
			}
			for _, toolCall := range message.ToolCalls {
				if err := appendItem(map[string]any{
					"type":      "function_call",
					"call_id":   toolCall.Id,
					"name":      toolCall.Function.Name,
					"arguments": toolCall.Function.Arguments,
				}); err != nil {
					return "", nil, err
				}
			}

		case "tool":
			if err := appendItem(map[string]any{
				"type":    "function_call_output",
				"call_id": message.ToolCallId,
				"output":  message.Content.Text(),
			}); err != nil {
				return "", nil, err
			}

		default:
			return "", nil, fmt.Errorf("cannot send a message with role %q to the codex backend", message.Role)
		}
	}

	return strings.Join(instructions, "\n\n"), input, nil
}

// responsesCompleted is the response object carried by the final stream event.
type responsesCompleted struct {
	Id     string            `json:"id"`
	Model  string            `json:"model"`
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Error  *responsesError   `json:"error"`
}

type responsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type responsesEvent struct {
	EventType string              `json:"type"`
	Response  *responsesCompleted `json:"response"`
	Item      json.RawMessage     `json:"item"`
	Error     *responsesError     `json:"error"`
	Message   string              `json:"message"`
}

// readResponsesStream consumes the server-sent events until the backend
// reports the response complete. Streaming deltas are not surfaced; only the
// finished output items matter to the agent loop. The Codex backend delivers
// each item through its own done event and leaves the output list on the
// completion event empty, so the items are gathered as they finish.
func readResponsesStream(logger *zap.Logger, body io.Reader) (*responsesCompleted, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var data []string
	var events int
	var finishedItems []json.RawMessage

	dispatch := func() (*responsesCompleted, error) {
		if len(data) == 0 {
			return nil, nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		events++

		event := responsesEvent{}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil, fmt.Errorf("could not parse stream event: %w", err)
		}

		switch event.EventType {
		case "response.output_item.done":
			if len(event.Item) > 0 {
				finishedItems = append(finishedItems, event.Item)
			}
		case "response.completed":
			if event.Response == nil {
				return nil, errors.New("stream reported completion without a response")
			}
			if len(event.Response.Output) == 0 {
				event.Response.Output = finishedItems
			}
			return event.Response, nil
		case "response.failed", "response.incomplete":
			if event.Response != nil && event.Response.Error != nil {
				return nil, fmt.Errorf("responses request %s: %s", event.EventType, event.Response.Error.Message)
			}
			return nil, fmt.Errorf("responses request %s", event.EventType)
		case "error":
			if event.Error != nil {
				return nil, fmt.Errorf("responses stream error: %s", event.Error.Message)
			}
			return nil, fmt.Errorf("responses stream error: %s", event.Message)
		}
		return nil, nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			completed, err := dispatch()
			if err != nil || completed != nil {
				logger.Debug("responses stream finished", zap.Int("event_count", events))
				return completed, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("could not read responses stream: %w", err)
	}

	completed, err := dispatch()
	if err != nil || completed != nil {
		return completed, err
	}

	return nil, fmt.Errorf("responses stream ended after %d events without completing: %w", events, io.ErrUnexpectedEOF)
}

type responsesOutputItem struct {
	ItemType  string `json:"type"`
	Id        string `json:"id"`
	CallId    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		PartType string `json:"type"`
		Text     string `json:"text"`
	} `json:"content"`
}

// buildChatResponse folds the output items into one assistant message. The
// raw items are kept in ReasoningDetails so the next request can replay them.
func buildChatResponse(completed *responsesCompleted) (*Response, error) {
	var text strings.Builder
	var toolCalls []ToolCall

	for _, raw := range completed.Output {
		item := responsesOutputItem{}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("could not parse output item: %w", err)
		}

		switch item.ItemType {
		case "message":
			for _, part := range item.Content {
				if part.PartType == "output_text" {
					text.WriteString(part.Text)
				}
			}
		case "function_call":
			toolCalls = append(toolCalls, ToolCall{
				ToolCallType: "function",
				Index:        uint64(len(toolCalls)),
				Id:           item.CallId,
				Function: FunctionCall{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		}
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	message := Message{
		Role:             "assistant",
		Content:          TextContent(text.String()),
		ReasoningDetails: ReasoningDetails(completed.Output),
		ToolCalls:        toolCalls,
	}

	return &Response{
		Id:       completed.Id,
		Object:   "response",
		Model:    completed.Model,
		Provider: "codex",
		Choices: []Choice{{
			Index:              0,
			FinishReason:       finishReason,
			NativeFinishReason: completed.Status,
			Message:            message,
		}},
	}, nil
}

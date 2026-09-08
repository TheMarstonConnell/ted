package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

var _ Provider = &OpenRouterProvider{}

type OpenRouterProvider struct {
	apiKey string
	client *http.Client
}

func NewOpenRouterProvider(apiKey string) *OpenRouterProvider {
	o := OpenRouterProvider{
		apiKey: apiKey,
		client: &http.Client{Timeout: 10 * time.Minute},
	}

	return &o
}

func (o *OpenRouterProvider) ListModels() []ModelInfo {
	return []ModelInfo{
		{ID: "meta/muse-spark-1.3-contributor"},
		{ID: "deepseek/deepseek-v4-flash-0731"},
		{ID: "openai/gpt-5.6-luna", Efforts: []Effort{EffortLow, EffortMedium, EffortHigh}, DefaultEffort: EffortMedium},
		{ID: "z-ai/glm-5.3-flash"},
	}
}

func (o *OpenRouterProvider) Name() string {
	return "openrouter"
}

func (o *OpenRouterProvider) Complete(logger *zap.Logger, completion CompletionRequest) (*Response, error) {
	model, messages := completion.Model, completion.Messages

	modelName := strings.TrimPrefix(strings.TrimPrefix(model, o.Name()), "/")

	compBod := CompletionBody{
		Model:    modelName,
		Messages: messages,
		Tools:    []Tool{makeBashTool()},
	}

	if completion.Effort != "" {
		compBod.Reasoning = &ReasoningOptions{Effort: completion.Effort}
	}
	bodyData, err := json.Marshal(compBod)
	if err != nil {
		return nil, fmt.Errorf("could not build completion body data %w", err)
	}

	if err := checkRequestSize(logger, len(bodyData)); err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", OPENROUTER_API, bytes.NewBuffer(bodyData))
	if err != nil {
		return nil, fmt.Errorf("failed to build completion request %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", o.apiKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://marston.dev")
	req.Header.Set("X-Title", "Marston Connell Harness Engineering")

	logger.Info("sending completion request",
		zap.String("endpoint", OPENROUTER_API),
		zap.String("model", modelName),
		zap.Int("message_count", len(messages)),
		zap.Int("request_bytes", len(bodyData)),
	)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not complete request %w", err)
	}
	defer resp.Body.Close()
	logger.Info("model HTTP response", zap.Int("status_code", resp.StatusCode), zap.Int("request_bytes", len(bodyData)))

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read body %w", err)
	}

	logger.Debug("received completion response",
		zap.Int("status_code", resp.StatusCode),
		zap.String("body", string(body)),
	)

	if resp.StatusCode != 200 {
		return nil, &statusError{status: resp.StatusCode, err: fmt.Errorf("completion request returned status %d: %s", resp.StatusCode, body)}
	}

	res := Response{}
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, fmt.Errorf("could not parse response %w", err)
	}

	return &res, nil
}

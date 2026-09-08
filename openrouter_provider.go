package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

var _ Provider = &OpenRouterProvider{}

type OpenRouterProvider struct {
	apiKey string
}

func NewOpenRouterProvider(apiKey string) *OpenRouterProvider {
	o := OpenRouterProvider{
		apiKey: apiKey,
	}

	return &o
}

func (o *OpenRouterProvider) ListModels() []string {
	return []string{
		"meta/muse-spark-1.3-contributor",
		"deepseek/deepseek-v4-flash-0731",
		"openai/gpt-5.6-luna",
	}
}

func (o *OpenRouterProvider) Name() string {
	return "openrouter"
}

func (o *OpenRouterProvider) Complete(logger *zap.Logger, model string, messages []Message) (*Response, error) {
	client := &http.Client{}

	modelName := strings.TrimPrefix(strings.TrimPrefix(model, o.Name()), "/")

	compBod := CompletionBody{
		Model:    modelName,
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

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", o.apiKey))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://marston.dev")
	req.Header.Set("X-Title", "Marston Connell Harness Engineering")

	logger.Debug("sending completion request",
		zap.String("endpoint", OPENROUTER_API),
		zap.String("model", modelName),
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

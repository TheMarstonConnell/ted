package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

var _ Provider = &OpenRouterProvider{}

type OpenRouterProvider struct {
	metadataMu     sync.RWMutex
	contextWindows map[string]int64
	apiKey         string
	client         *http.Client
}

func NewOpenRouterProvider(apiKey string) *OpenRouterProvider {
	o := OpenRouterProvider{
		apiKey: apiKey,
		client: &http.Client{Timeout: 10 * time.Minute},
	}

	return &o
}

func (o *OpenRouterProvider) ListModels() []ModelInfo {
	models := []ModelInfo{
		{ID: "meta/muse-spark-1.3-contributor"},
		{ID: "deepseek/deepseek-v4-flash-0731"},
		{ID: "openai/gpt-5.6-luna", Efforts: []Effort{EffortLow, EffortMedium, EffortHigh}, DefaultEffort: EffortMedium},
		{ID: "z-ai/glm-5.3-flash"},
	}
	o.metadataMu.RLock()
	defer o.metadataMu.RUnlock()
	for i := range models {
		models[i].ContextWindow = o.contextWindows[models[i].ID]
	}
	return models
}

// LoadModelMetadata refreshes capacities without changing the supported model list.
// On failure existing metadata is retained. Callers should supply a short deadline.
func (o *OpenRouterProvider) LoadModelMetadata(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model metadata returned status %d", resp.StatusCode)
	}
	var catalog struct {
		Data []struct {
			ID            string `json:"id"`
			ContextWindow int64  `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&catalog); err != nil {
		return err
	}
	windows := make(map[string]int64, len(catalog.Data))
	for _, model := range catalog.Data {
		if model.ContextWindow > 0 {
			windows[model.ID] = model.ContextWindow
		}
	}
	o.metadataMu.Lock()
	o.contextWindows = windows
	o.metadataMu.Unlock()
	return nil
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

	req, err := http.NewRequestWithContext(completion.RequestContext(), "POST", OPENROUTER_API, bytes.NewBuffer(bodyData))
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

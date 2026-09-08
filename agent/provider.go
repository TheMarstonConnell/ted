package agent

import "go.uber.org/zap"

// Provider describes a configured backend. Model IDs returned here are local
// to the provider; Agent.ListModels qualifies them with the provider name.
type Provider interface {
	ListModels() []ModelInfo
	Name() string
	Complete(logger *zap.Logger, request CompletionRequest) (*Response, error)
}

// CompletionRequest is a single provider request within an agent turn.
type CompletionRequest struct {
	Model    string
	Effort   Effort
	Messages []Message
}

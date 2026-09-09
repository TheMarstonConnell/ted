package agent

import (
	"context"
	"go.uber.org/zap"
)

// Provider describes a configured backend. Model IDs returned here are local
// to the provider; Agent.ListModels qualifies them with the provider name.
type Provider interface {
	ListModels() []ModelInfo
	Name() string
	Complete(logger *zap.Logger, request CompletionRequest) (*Response, error)
}

// CompletionRequest is a single provider request within an agent turn.
type CompletionRequest struct {
	Context  context.Context
	Model    string
	Effort   Effort
	Messages []Message
}

// RequestContext returns the request context, or background for legacy callers.
func (r CompletionRequest) RequestContext() context.Context {
	if r.Context != nil {
		return r.Context
	}
	return context.Background()
}

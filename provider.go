package main

import "go.uber.org/zap"

type Provider interface {
	ListModels() []string
	Name() string
	Complete(logger *zap.Logger, model string, messages []Message) (*Response, error)
}

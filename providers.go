package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

// configuredProviders shares CLI environment and credential discovery.
func configuredProviders(logger *zap.Logger) ([]agent.Provider, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("could not load .env file: %w", err)
	}

	var providers []agent.Provider

	codexAuthPath, err := agent.DefaultCodexAuthPath()
	if err != nil {
		return nil, err
	}
	codexAuth := agent.NewCodexAuthStore(codexAuthPath)
	if codexAuth.Exists() {
		providers = append(providers, agent.NewCodexProvider(codexAuth))
	} else {
		logger.Debug("no codex login found", zap.String("path", codexAuthPath))
	}

	if openRouterKey := os.Getenv(openRouterKeyVariable); openRouterKey != "" {
		provider := agent.NewOpenRouterProvider(openRouterKey)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := provider.LoadModelMetadata(ctx); err != nil {
			logger.Debug("model capacities unavailable", zap.Error(err))
		}
		cancel()
		providers = append(providers, provider)
	} else {
		logger.Debug("no openrouter key found", zap.String("variable", openRouterKeyVariable))
	}

	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers available: log in with `codex login` or set %s", openRouterKeyVariable)
	}

	return providers, nil
}

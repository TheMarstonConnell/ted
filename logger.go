package main

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// LOG_LEVEL_VARIABLE names the environment variable that overrides the
// default logging level.
const LOG_LEVEL_VARIABLE = "LOG_LEVEL"

// newLogger builds the application logger. Diagnostic output is written to
// standard error so that it stays separate from the conversation the user
// reads on standard output. The level defaults to info and may be raised or
// lowered with the LOG_LEVEL environment variable, which accepts the zap
// level names: debug, info, warn, error, dpanic, panic, fatal.
func newLogger() (*zap.Logger, error) {
	level := zapcore.InfoLevel
	if requested := strings.TrimSpace(os.Getenv(LOG_LEVEL_VARIABLE)); requested != "" {
		if err := level.Set(requested); err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", LOG_LEVEL_VARIABLE, requested, err)
		}
	}

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	config := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Encoding:         "console",
		EncoderConfig:    encoderConfig,
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}

	// Failures here are configuration and network problems reported with a
	// plain message, so stack traces would only clutter the terminal.
	return config.Build(zap.AddStacktrace(zapcore.FatalLevel + 1))
}

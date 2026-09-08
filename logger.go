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

// LOG_FILE_VARIABLE names the environment variable that overrides the
// default log file path.
const LOG_FILE_VARIABLE = "LOG_FILE"

// DEFAULT_LOG_FILE is the log file path used when LOG_FILE is unset.
const DEFAULT_LOG_FILE = "harness.log"

// newLogger builds the application logger. Diagnostic output is appended to
// a log file so that it stays out of the terminal the TUI draws on. The path
// defaults to harness.log in the working directory and may be changed with
// the LOG_FILE environment variable. The level defaults to info and may be
// raised or lowered with the LOG_LEVEL environment variable, which accepts
// the zap level names: debug, info, warn, error, dpanic, panic, fatal.
func newLogger() (*zap.Logger, error) {
	level := zapcore.InfoLevel
	if requested := strings.TrimSpace(os.Getenv(LOG_LEVEL_VARIABLE)); requested != "" {
		if err := level.Set(requested); err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", LOG_LEVEL_VARIABLE, requested, err)
		}
	}

	logFile := strings.TrimSpace(os.Getenv(LOG_FILE_VARIABLE))
	if logFile == "" {
		logFile = DEFAULT_LOG_FILE
	}

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	config := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Encoding:         "console",
		EncoderConfig:    encoderConfig,
		OutputPaths:      []string{logFile},
		ErrorOutputPaths: []string{"stderr"},
	}

	// Failures here are configuration and network problems reported with a
	// plain message, so stack traces would only clutter the terminal.
	logger, err := config.Build(zap.AddStacktrace(zapcore.FatalLevel + 1))
	if err != nil {
		return nil, err
	}
	logger.Info("logger initialized", zap.String("log_file", logFile), zap.String("log_level", level.String()))
	return logger, nil
}

package agent

import (
	"fmt"
	"go.uber.org/zap"
)

// MaxRequestBytes is a conservative local safety budget, not a provider limit.
const MaxRequestBytes = 8 << 20

func checkRequestSize(logger *zap.Logger, size int) error {
	if size <= MaxRequestBytes {
		return nil
	}
	err := fmt.Errorf("request payload is %d bytes, exceeding the harness limit of %d bytes; start a fresh conversation or reduce tool output/screenshots", size, MaxRequestBytes)
	logger.Error("request blocked by payload limit", zap.Int("request_bytes", size), zap.Int("limit_bytes", MaxRequestBytes))
	return err
}

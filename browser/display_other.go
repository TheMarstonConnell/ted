//go:build !linux

package browser

import "context"

func startHeadedDisplay(context.Context, string) ([]string, func(), error) {
	return nil, func() {}, nil
}

//go:build !linux

package browser

import (
	"context"
	"testing"
)

func TestHeadedDisplayNativeDesktop(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("PATH", t.TempDir())
	env, cleanup, err := startHeadedDisplay(context.Background(), "/does/not/exist")
	if err != nil || env != nil {
		t.Fatalf("native display replaced: %v, %v", env, err)
	}
	cleanup()
	cleanup()
}

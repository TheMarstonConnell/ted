//go:build !unix

package controlplane

import (
	"fmt"
	"os"
	"path/filepath"
)

func lockStore(dir string) (func() error, error) {
	path := filepath.Join(dir, "state.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("control plane directory locked (remove state.lock after a crash on this platform): %w", err)
	}
	return func() error { f.Close(); return os.Remove(path) }, nil
}

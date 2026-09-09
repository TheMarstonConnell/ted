//go:build unix

package controlplane

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Storage locking prevents independently started servers from overwriting data.
func lockStore(dir string) (func() error, error) {
	f, err := os.OpenFile(filepath.Join(dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("control plane data directory is already in use: %w", err)
	}
	return f.Close, nil
}

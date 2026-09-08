//go:build windows

package browser

import (
	"fmt"
	"os"
)

func acquireDaemonLock(path string) (*os.File, error) {
	return nil, fmt.Errorf("browser daemon requires Unix-domain socket support")
}
func releaseDaemonLock(f *os.File) {}

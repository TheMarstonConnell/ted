package agent

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	threadIDEnvironment    = "TED_THREAD_ID"
	projectRootEnvironment = "TED_PROJECT_ROOT"
)

// newThreadID deliberately never consults TED_THREAD_ID. A ted process
// started by the bash tool is a new agent and must not accidentally take over
// its parent's browser session.
func newThreadID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		// An agent cannot safely share a browser identity. Failure of the OS
		// random source is exceptional enough that construction cannot proceed.
		panic("ted: cannot create thread ID: " + err.Error())
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	var encoded [36]byte
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded[:])
}

func resolveWorkingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	if absolute, err := filepath.Abs(dir); err == nil {
		dir = absolute
	}
	if canonical, err := filepath.EvalSymlinks(dir); err == nil {
		dir = canonical
	}
	return filepath.Clean(dir)
}

func resolveProjectRoot(dir string) string {
	if dir == "" {
		if cwd, err := os.Getwd(); err == nil {
			dir = cwd
		} else {
			dir = "."
		}
	}
	if absolute, err := filepath.Abs(dir); err == nil {
		dir = absolute
	}
	if canonical, err := filepath.EvalSymlinks(dir); err == nil {
		dir = canonical
	}

	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	if output, err := cmd.Output(); err == nil {
		root := strings.TrimSpace(string(output))
		if root != "" {
			if absolute, err := filepath.Abs(root); err == nil {
				root = absolute
			}
			if canonical, err := filepath.EvalSymlinks(root); err == nil {
				root = canonical
			}
			return filepath.Clean(root)
		}
	}
	return filepath.Clean(dir)
}

func tedHome() string {
	home := strings.TrimSpace(os.Getenv("TED_HOME"))
	if userHome, err := os.UserHomeDir(); err == nil {
		if home == "" {
			home = filepath.Join(userHome, ".ted")
		} else if strings.HasPrefix(home, "~/") {
			home = filepath.Join(userHome, home[2:])
		}
	}
	if home == "" {
		home = ".ted"
	}
	if absolute, err := filepath.Abs(home); err == nil {
		home = absolute
	}
	return filepath.Clean(home)
}

// Pin the resolved home as well as the identity: a tool may cd before calling
// ted browser, but its artifacts must still land where the agent reads them.
func toolEnvironment(threadID, projectRoot, home string) []string {
	environment := os.Environ()
	filtered := environment[:0]
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if name != threadIDEnvironment && name != projectRootEnvironment && name != "TED_HOME" {
			filtered = append(filtered, item)
		}
	}
	return append(filtered,
		threadIDEnvironment+"="+threadID,
		projectRootEnvironment+"="+projectRoot,
		"TED_HOME="+home,
	)
}

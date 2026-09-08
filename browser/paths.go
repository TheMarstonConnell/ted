package browser

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var threadPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Home returns TED_HOME, defaulting to ~/.ted, as an absolute canonical-ish
// path. The directory is created with private permissions.
func Home() (string, error) {
	h := strings.TrimSpace(os.Getenv("TED_HOME"))
	if h == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		h = filepath.Join(user, ".ted")
	} else if strings.HasPrefix(h, "~/") {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand TED_HOME: %w", err)
		}
		h = filepath.Join(user, h[2:])
	}
	abs, err := filepath.Abs(h)
	if err != nil {
		return "", fmt.Errorf("resolve TED_HOME: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("create TED_HOME: %w", err)
	}
	if err := ensurePrivateDir(abs); err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// ProjectRoot returns the canonical git worktree root containing dir. If dir
// is not in a git worktree (or git is unavailable), it returns canonical dir.
func ProjectRoot(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat project directory: %w", err)
	}
	if !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	abs = canonicalPath(abs)

	cmd := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel")
	cmd.Stderr = nil
	if out, gitErr := cmd.Output(); gitErr == nil {
		root := strings.TrimSpace(string(out))
		if root != "" {
			if !filepath.IsAbs(root) {
				root = filepath.Join(abs, root)
			}
			return canonicalPath(root), nil
		}
	}
	return abs, nil
}

func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func validateThread(thread string) error {
	if !threadPattern.MatchString(thread) || thread == "." || thread == ".." {
		return fail("invalid_thread", "thread ID must be 1-128 safe filename characters")
	}
	return nil
}

func ensurePrivateDir(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("make %s private: %w", path, err)
		}
	}
	return nil
}

func mkdirPrivate(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return ensurePrivateDir(path)
}

func projectKey(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:16])
}

func socketPath() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	d := filepath.Join(h, "browser")
	if err := mkdirPrivate(d); err != nil {
		return "", err
	}
	p := filepath.Join(d, "daemon.sock")
	// Most Unix kernels limit sockaddr_un paths to roughly 104 bytes. Keep all
	// state in TED_HOME but use a private, deterministic /tmp indirection when
	// TED_HOME is unusually long.
	if len(p) >= 100 {
		sum := sha256.Sum256([]byte(h))
		d = filepath.Join(os.TempDir(), "ted-browser-"+hex.EncodeToString(sum[:8]))
		if err := mkdirPrivate(d); err != nil {
			return "", err
		}
		p = filepath.Join(d, "daemon.sock")
	}
	return p, nil
}

func threadDir(home, thread string) (string, error) {
	if err := validateThread(thread); err != nil {
		return "", err
	}
	base := filepath.Join(home, "threads")
	path := filepath.Join(base, thread)
	if filepath.Dir(path) != base { // defense in depth
		return "", fail("invalid_thread", "thread ID escapes thread directory")
	}
	if err := mkdirPrivate(path); err != nil {
		return "", err
	}
	return path, nil
}

func isNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

// existingSocketPath computes the daemon endpoint without creating or chmodding
// any state. Keep this in sync with socketPath.
func existingSocketPath() (string, error) {
	h := strings.TrimSpace(os.Getenv("TED_HOME"))
	if h == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		h = filepath.Join(user, ".ted")
	} else if strings.HasPrefix(h, "~/") {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand TED_HOME: %w", err)
		}
		h = filepath.Join(user, h[2:])
	}
	abs, err := filepath.Abs(h)
	if err != nil {
		return "", fmt.Errorf("resolve TED_HOME: %w", err)
	}
	p := filepath.Join(filepath.Clean(abs), "browser", "daemon.sock")
	if len(p) >= 100 {
		sum := sha256.Sum256([]byte(filepath.Clean(abs)))
		p = filepath.Join(os.TempDir(), "ted-browser-"+hex.EncodeToString(sum[:8]), "daemon.sock")
	}
	return p, nil
}

func isDaemonAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

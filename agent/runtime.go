package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// NewAgentIn constructs an independent runtime in an explicit directory. It
// resolves symlinks and the enclosing git root without changing process cwd.
// Like NewAgent, it does not enable legacy session persistence.
func NewAgentIn(logger *zap.Logger, providers []Provider, workingDir string) (*Agent, error) {
	dir, err := canonicalDirectory(workingDir)
	if err != nil {
		return nil, fmt.Errorf("working directory: %w", err)
	}
	return newAgentIn(logger, providers, dir), nil
}

func canonicalDirectory(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("directory is required")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return filepath.Clean(canonical), nil
}

// SetIdentity restores control-plane identity without reading or writing legacy
// sessions. It is allowed only while idle and before legacy persistence is
// enabled. Paths must be absolute; dir must exist. The ID must be a safe single
// path component because browser artifacts are stored under home/threads/id.
func (a *Agent) SetIdentity(id, root, dir, home string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if a.persistent {
		return fmt.Errorf("cannot change identity of a persistent agent")
	}
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return fmt.Errorf("invalid agent identity %q", id)
	}
	for _, path := range []string{root, dir, home} {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
			return fmt.Errorf("identity paths must be absolute")
		}
	}
	resolved, err := canonicalDirectory(dir)
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	a.threadID, a.projectRoot, a.workingDir, a.home = id, filepath.Clean(root), resolved, filepath.Clean(home)
	a.artifactMu.Lock()
	a.manifestOffset = 0
	a.artifactMu.Unlock()
	return nil
}

// ExportConversation returns a detached, JSON-serializable checkpoint. It is
// identical to Messages. Full tool logs belong to tool_result events, not this
// capped provider-context checkpoint. Call after TurnContext returns for the
// latest committed (including cancelled) turn.
func (a *Agent) ExportConversation() []Message { return a.Messages() }

// RestoreConversation installs a detached checkpoint without executing tools or
// using legacy persistence. Incomplete tool exchanges are rejected atomically.
// Settings/identity are restored separately. Context usage is reset because a
// message checkpoint does not contain authoritative provider token accounting.
func (a *Agent) RestoreConversation(messages []Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if a.persistent {
		return fmt.Errorf("cannot restore over a persistent agent")
	}
	if err := validateConversation(messages); err != nil {
		return err
	}
	restored := cloneMessages(messages)
	for i := range restored {
		if restored[i].SourceModel != "" {
			restored[i].sourceModel = restored[i].SourceModel
		}
		restored[i].SourceModel = ""
	}
	a.messages = restored
	a.contextUsage = ContextUsage{}
	return nil
}

func validateConversation(messages []Message) error {
	if len(messages) == 0 || messages[0].Role != "system" {
		return fmt.Errorf("conversation must start with a system message")
	}
	pending := make(map[string]bool)
	for i, m := range messages {
		if len(pending) > 0 && m.Role != "tool" {
			return fmt.Errorf("message %d interrupts unanswered tool calls", i)
		}
		if m.Role != "assistant" && len(m.ToolCalls) > 0 {
			return fmt.Errorf("message %d: only assistant messages can call tools", i)
		}
		if m.Role != "tool" && m.ToolCallId != "" {
			return fmt.Errorf("message %d: unexpected tool result ID", i)
		}
		switch m.Role {
		case "system", "user":
		case "assistant":
			for _, call := range m.ToolCalls {
				if call.Id == "" || pending[call.Id] {
					return fmt.Errorf("message %d: missing or duplicate tool call ID", i)
				}
				pending[call.Id] = true
			}
		case "tool":
			if !pending[m.ToolCallId] {
				return fmt.Errorf("message %d: unexpected tool result %q", i, m.ToolCallId)
			}
			delete(pending, m.ToolCallId)
		default:
			return fmt.Errorf("message %d: unsupported role %q", i, m.Role)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("conversation has unanswered tool calls")
	}
	return nil
}

// ArtifactOffset is a checkpoint of completed browser artifact consumption.
// Read it after TurnContext returns; artifact callbacks may hold artifactMu.
func (a *Agent) ArtifactOffset() int64 {
	a.artifactMu.Lock()
	defer a.artifactMu.Unlock()
	return a.manifestOffset
}

// RestoreArtifactOffset restores a server-owned idle runtime checkpoint.
func (a *Agent) RestoreArtifactOffset(offset int64) {
	a.artifactMu.Lock()
	defer a.artifactMu.Unlock()
	a.manifestOffset = max(0, offset)
}

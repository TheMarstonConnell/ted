package controlplane

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	legacyStoreVersion = 1
	storeVersion       = 2
)

type storedAgent struct {
	Agent  Agent   `json:"agent"`
	Events []Event `json:"events"`
	// Browser artifact consumption is private runtime state.
	ManifestOffset int64 `json:"manifest_offset"`
}
type receipt struct {
	Fingerprint string `json:"fingerprint"`
	AgentID     string `json:"agent_id"`
	MessageID   string `json:"message_id,omitempty"`
}
type diskState struct {
	Version  int                     `json:"version"`
	Projects map[string]Project      `json:"projects"`
	Agents   map[string]*storedAgent `json:"agents"`
	Receipts map[string]receipt      `json:"receipts"`
}

func emptyState() diskState {
	return diskState{storeVersion, make(map[string]Project), make(map[string]*storedAgent), make(map[string]receipt)}
}
func loadState(dir string) (diskState, error) {
	state := emptyState()
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	return decodeLegacyState(b)
}

func decodeLegacyState(b []byte) (diskState, error) {
	state := emptyState()
	if err := json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("read control plane state: %w", err)
	}
	legacy := state.Version == legacyStoreVersion
	if (!legacy && state.Version != storeVersion) || state.Projects == nil || state.Agents == nil || state.Receipts == nil {
		return state, fmt.Errorf("unsupported or incomplete control plane state")
	}
	for id, p := range state.Projects {
		if p.ID != id {
			return state, fmt.Errorf("invalid project record %q", id)
		}
	}
	for id, a := range state.Agents {
		if a == nil || a.Agent.ID != id {
			return state, fmt.Errorf("invalid agent record %q", id)
		}
		if _, ok := state.Projects[a.Agent.ProjectID]; !ok {
			return state, fmt.Errorf("agent %q references missing project", id)
		}
		if uint64(len(a.Events)) != a.Agent.Cursor {
			return state, fmt.Errorf("invalid event cursor for agent %q", id)
		}
		for i, e := range a.Events {
			if e.AgentID != id || e.Cursor != uint64(i+1) {
				return state, fmt.Errorf("invalid event sequence for agent %q", id)
			}
		}
		if legacy {
			for _, e := range a.Events {
				if e.Type != "output" {
					continue
				}
				var output struct {
					ResponseType string `json:"ResponseType"`
				}
				if json.Unmarshal(e.Data, &output) == nil && output.ResponseType == "agent" {
					a.Agent.LastResponseCursor = e.Cursor
				}
			}
			a.Agent.ReadCursor = a.Agent.LastResponseCursor
		}
		if a.Agent.LastResponseCursor > a.Agent.Cursor || a.Agent.ReadCursor > a.Agent.Cursor {
			return state, fmt.Errorf("invalid read cursor for agent %q", id)
		}
	}
	if err := validateParentDAG(state.Agents); err != nil {
		return state, err
	}
	state.Version = storeVersion
	return state, nil
}

// Legacy JSON writer retained for migration fixtures and comparative benchmarks.
func saveState(dir string, state diskState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(dir, "state.json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

package controlplane

import (
	"os"
	"path/filepath"
)

// creationWorkspaceLocked snapshots a location independently of parentage. A
// shared worktree has its own draft/lock state, but never owns Git provisioning.
func (s *Service) creationWorkspaceLocked(p Project, req CreateAgentRequest) (Workspace, error) {
	var parent *storedAgent
	if req.ParentAgentID != "" {
		var err error
		parent, err = s.recordLocked(req.ParentAgentID)
		if err != nil {
			return Workspace{}, err
		}
	}
	selection := p.WorkspaceDefaults
	if selection.Mode == "" {
		selection.Mode = "current_checkout"
	}
	// Keep already-validated project defaults, even if a remote ref has gone away.
	if req.Workspace != nil {
		selection = *req.Workspace
		if selection.BaseBranch == "" {
			selection.BaseBranch = p.WorkspaceDefaults.BaseBranch
		}
		var err error
		selection, err = normalizeWorkspace(p.Root, selection)
		if err != nil {
			return Workspace{}, err
		}
	}
	result := Workspace{WorkspaceSelection: selection, Status: "draft"}
	if req.WorkingDirectory != "" {
		path, err := canonicalWorkspaceDirectory(req.WorkingDirectory)
		if err != nil {
			return Workspace{}, err
		}
		var existing *Workspace
		if path != p.Root {
			// Only server-recorded worktrees belong to the original project. In
			// particular, arbitrary external paths cannot masquerade as its checkout.
			for _, a := range s.state.Agents {
				w := a.Agent.Workspace
				if a.Agent.ProjectID != p.ID || w.Mode != "worktree" || w.Path == "" || w.Status != "ready" {
					continue
				}
				// The control-plane data directory itself may be a symlink.
				canonical, err := filepath.EvalSymlinks(w.Path)
				if err == nil && filepath.Clean(canonical) == path {
					existing = &w
					break
				}
			}
			if existing == nil {
				return Workspace{}, problem(400, "invalid_workspace", "working_directory must be the project root or an established worktree of this project")
			}
		}
		if req.Workspace == nil || selection.Mode == "current_checkout" {
			if existing != nil {
				return sharedWorkspace(*existing), nil
			}
			// An explicit directory wins over implicit parent inheritance, but otherwise
			// behaves like launching from that project (including its workspace defaults).
			if selection.Mode == "current_checkout" {
				result.Path = path
			}
		}
		return result, nil
	}
	if req.Workspace == nil && parent != nil && parent.Agent.ProjectID == p.ID {
		w := parent.Agent.Workspace
		if w.Mode == "worktree" && w.Locked {
			if w.Status != "ready" {
				return Workspace{}, problem(409, "workspace_unavailable", "parent worktree is not ready; select a workspace explicitly or wait for setup")
			}
			if _, err := canonicalWorkspaceDirectory(w.Path); err != nil {
				return Workspace{}, err
			}
			return sharedWorkspace(w), nil
		}
		// A shared draft already has an established path, even before its first send.
		if w.Shared && w.Path != "" {
			if _, err := canonicalWorkspaceDirectory(w.Path); err != nil {
				return Workspace{}, err
			}
			return sharedWorkspace(w), nil
		}
	}
	return result, nil
}

func sharedWorkspace(w Workspace) Workspace {
	return Workspace{WorkspaceSelection: w.WorkspaceSelection, Shared: true,
		Status: "draft", Path: w.Path, Branch: w.Branch, BaseCommit: w.BaseCommit}
}

func canonicalWorkspaceDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", problem(400, "invalid_workspace", "working_directory must be an absolute server-local directory")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", problem(400, "invalid_workspace", err.Error())
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", problem(400, "invalid_workspace", "working_directory must be an existing directory")
	}
	return filepath.Clean(canonical), nil
}

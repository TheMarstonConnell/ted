package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestWorkspaceCreateAndPatchRequests(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/agents":
			var body struct {
				ProjectID string              `json:"project_id"`
				Workspace *WorkspaceSelection `json:"workspace"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			n := creates.Add(1)
			if body.ProjectID != "p" {
				t.Errorf("project_id = %q", body.ProjectID)
			}
			if n == 1 && body.Workspace != nil {
				t.Errorf("default create sent an override: %+v", body.Workspace)
			}
			if n == 2 && (body.Workspace == nil || body.Workspace.Mode != "worktree" || body.Workspace.BaseBranch != "origin/main") {
				t.Errorf("workspace = %+v", body.Workspace)
			}
			_ = json.NewEncoder(w).Encode(Snapshot{ID: "a", ProjectID: "p", Workspace: Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}, Status: "draft"}})
		case "/v1/agents/a/workspace":
			if r.Method != http.MethodPatch {
				t.Errorf("method = %s", r.Method)
			}
			var selection WorkspaceSelection
			_ = json.NewDecoder(r.Body).Decode(&selection)
			if selection.Mode != "current_checkout" {
				t.Errorf("selection = %+v", selection)
			}
			_ = json.NewEncoder(w).Encode(Snapshot{ID: "a", Workspace: Workspace{WorkspaceSelection: selection, Status: "draft"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := New(server.URL)
	if _, err := client.CreateAgent(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	selection := WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}
	got, err := client.CreateAgent(context.Background(), "p", selection)
	if err != nil || got.Workspace.Mode != selection.Mode || got.Workspace.BaseBranch != selection.BaseBranch {
		t.Fatal(got, err)
	}
	got, err = client.PatchAgentWorkspace(context.Background(), "a", WorkspaceSelection{Mode: "current_checkout"})
	if err != nil || got.Workspace.Mode != "current_checkout" {
		t.Fatal(got, err)
	}
}

func TestWorktreeAgentUsesWorkspacePathAndBranch(t *testing.T) {
	a := NewAgent(context.Background(), New("http://unused.invalid"), Snapshot{
		ID:        "a",
		ProjectID: "p",
		Workspace: Workspace{
			WorkspaceSelection: WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"},
			Status:             "ready",
			Path:               "/server/state/worktrees/p/a",
			Branch:             "ted/chat-a",
		},
	}, "/server/project", nil)
	if got := a.WorkingDir(); got != "/server/state/worktrees/p/a" {
		t.Fatalf("WorkingDir = %q", got)
	}
	if got, err := a.GitBranch(); err != nil || got != "ted/chat-a" {
		t.Fatalf("GitBranch = %q, %v", got, err)
	}
}

func TestWorktreeAgentRefreshesWorkspaceMetadata(t *testing.T) {
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/a" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		gets.Add(1)
		_ = json.NewEncoder(w).Encode(Snapshot{
			ID: "a", ProjectID: "p", Cursor: 2,
			Workspace: Workspace{
				WorkspaceSelection: WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"},
				Locked:             true, Status: "ready", Path: "/worktree/a", Branch: "ted/a",
			},
		})
	}))
	defer server.Close()

	a := NewAgent(context.Background(), New(server.URL), Snapshot{
		ID: "a", ProjectID: "p", Cursor: 1,
		Workspace: Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "worktree"}, Status: "draft"},
	}, "/project", nil)
	// WorkingDir is a snapshot-only accessor and must not perform HTTP while
	// the worktree is still a draft.
	if got := a.WorkingDir(); got != "/project" {
		t.Fatalf("draft WorkingDir = %q", got)
	}
	if gets.Load() != 0 {
		t.Fatalf("WorkingDir performed %d GET requests", gets.Load())
	}
	// GitBranch runs from an asynchronous Tea command and may refresh the
	// snapshot. The following WorkingDir sees that refreshed worktree path.
	if got, err := a.GitBranch(); err != nil || got != "ted/a" {
		t.Fatalf("GitBranch = %q, %v", got, err)
	}
	if got := a.WorkingDir(); got != "/worktree/a" {
		t.Fatalf("refreshed WorkingDir = %q", got)
	}
	if gets.Load() != 1 {
		t.Fatalf("GET agent calls = %d", gets.Load())
	}
}

func TestProjectWorkspaceDefaultsDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"p","root":"/repo","workspace_defaults":{"mode":"worktree","base_branch":"upstream/trunk"}}`))
	}))
	defer server.Close()
	project, err := New(server.URL).Project(context.Background(), "p")
	if err != nil || project.WorkspaceDefaults.Mode != "worktree" || project.WorkspaceDefaults.BaseBranch != "upstream/trunk" {
		t.Fatal(project, err)
	}
}

func TestAgentUpdatedPublishesStateWithWorkspaceSnapshot(t *testing.T) {
	a := NewAgent(context.Background(), nil, Snapshot{
		ID: "a", ProjectID: "p", Cursor: 1,
		Workspace: Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "worktree"}, Status: "draft"},
	}, "/project", nil)
	updated := Snapshot{
		ID: "a", ProjectID: "p", State: "running",
		Workspace: Workspace{
			WorkspaceSelection: WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"},
			Locked:             true, Status: "fetching", Path: "/worktree/a", Branch: "ted/a",
		},
	}
	data, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	var notification Update
	if err := a.consume(Event{AgentID: "a", Cursor: 2, Type: "agent.updated", Data: data}, func(update Update) {
		notification = update
	}); err != nil {
		t.Fatal(err)
	}
	if notification.Busy == nil || !*notification.Busy {
		t.Fatalf("state notification = %+v", notification)
	}
	if got := a.WorkingDir(); got != "/worktree/a" {
		t.Fatalf("WorkingDir = %q", got)
	}
	if got, err := a.GitBranch(); err != nil || got != "ted/a" {
		t.Fatalf("GitBranch = %q, %v", got, err)
	}
}

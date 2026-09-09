package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/remote"
)

func TestTUIWorkspaceFlags(t *testing.T) {
	for _, tt := range []struct {
		name       string
		args       []string
		wantMode   string
		wantBranch string
		wantNil    bool
		wantError  string
	}{
		{name: "inherit project defaults", wantNil: true},
		{name: "current checkout", args: []string{"--workspace", "current_checkout"}, wantMode: "current_checkout"},
		{name: "worktree default branch", args: []string{"--workspace", "worktree"}, wantMode: "worktree"},
		{name: "worktree explicit branch", args: []string{"--workspace", "worktree", "--base-branch", "origin/main"}, wantMode: "worktree", wantBranch: "origin/main"},
		{name: "base implies worktree", args: []string{"--base-branch", "upstream/trunk"}, wantMode: "worktree", wantBranch: "upstream/trunk"},
		{name: "invalid mode", args: []string{"--workspace", "shared"}, wantError: "current_checkout or worktree"},
		{name: "branch with checkout", args: []string{"--workspace", "current_checkout", "--base-branch", "origin/main"}, wantError: "requires --workspace worktree"},
		{name: "resume override", args: []string{"--resume", "agent-id", "--workspace", "worktree"}, wantError: "locked"},
		{name: "continue override", args: []string{"--continue", "--base-branch", "origin/main"}, wantError: "locked"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newTUICommand()
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatal(err)
			}
			mode, _ := cmd.Flags().GetString("workspace")
			branch, _ := cmd.Flags().GetString("base-branch")
			resume, _ := cmd.Flags().GetString("resume")
			latest, _ := cmd.Flags().GetBool("continue")
			selection, err := tuiWorkspaceSelection(cmd, mode, branch, resume, latest)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantNil {
				if selection != nil {
					t.Fatalf("selection = %+v", selection)
				}
				return
			}
			if selection == nil || selection.Mode != tt.wantMode || selection.BaseBranch != tt.wantBranch {
				t.Fatalf("selection = %+v", selection)
			}
		})
	}
}

func TestPrepareRemoteAgentRejectsWorkspaceOnResumeBeforeAPICall(t *testing.T) {
	_, err := prepareRemoteAgent(t.Context(), nil, "", "", "agent-id", false, remote.WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"})
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareRemoteAgentCreatesWithWorkspaceSelection(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var sent *remote.WorkspaceSelection
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`[{"id":"test/model","name":"Test","provider":"test","efforts":["low"],"default_effort":"low"}]`))
		case "/v1/projects":
			if r.Method != http.MethodGet {
				t.Errorf("unexpected projects method %s", r.Method)
			}
			_ = json.NewEncoder(w).Encode([]remote.Project{{ID: "p", Name: "project", Root: cwd}})
		case "/v1/agents":
			var body struct {
				ProjectID string                     `json:"project_id"`
				Workspace *remote.WorkspaceSelection `json:"workspace"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.ProjectID != "p" {
				t.Errorf("project_id = %q", body.ProjectID)
			}
			sent = body.Workspace
			_ = json.NewEncoder(w).Encode(remote.Snapshot{
				ID: "a", ProjectID: "p", State: "idle",
				Settings:  remote.Settings{Model: "test/model", Effort: "low"},
				Workspace: remote.Workspace{WorkspaceSelection: *body.Workspace, Status: "draft"},
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	selection := remote.WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}
	if _, err := prepareRemoteAgent(t.Context(), remote.New(server.URL), "", "", "", false, selection); err != nil {
		t.Fatal(err)
	}
	if sent == nil || *sent != selection {
		t.Fatalf("workspace request = %+v", sent)
	}
}

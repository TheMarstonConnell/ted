package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/controlplane"
	"github.com/TheMarstonConnell/ted/remote"
)

func TestTUIParentAndDirectoryFlagValidation(t *testing.T) {
	for _, tt := range []struct {
		args    []string
		message string
	}{
		{[]string{"--parent-agent", ""}, "--parent-agent requires"},
		{[]string{"--cwd", " "}, "--cwd requires"},
		{[]string{"--parent-agent", "p", "--resume", "a"}, "creation-only"},
		{[]string{"--parent-agent", "p", "--continue"}, "creation-only"},
		{[]string{"--cwd", "/repo", "--resume", "a"}, "--cwd cannot be used"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			cmd := newTUICommand()
			// A flag error must be returned before trying to connect to a server.
			cmd.SetArgs(append(tt.args, "--server", "not-a-server"))
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("error %v, want %s", err, tt.message)
			}
		})
	}
	for _, tt := range []struct {
		opts   tuiStartupOptions
		resume string
		latest bool
	}{
		{opts: tuiStartupOptions{ParentAgentID: "parent"}, resume: "a"},
		{opts: tuiStartupOptions{ParentAgentID: "parent"}, latest: true},
		{opts: tuiStartupOptions{CWD: "/repo"}, resume: "a"},
	} {
		if _, err := prepareRemoteAgent(t.Context(), nil, "", "", tt.resume, tt.latest, tt.opts); err == nil {
			t.Fatal("options bypassed validation")
		}
	}
}

func TestTUIParentAndDirectoryCreationRequests(t *testing.T) {
	root := t.TempDir()
	worktree := t.TempDir()
	worktreeAlias := filepath.Join(t.TempDir(), "worktree-link")
	if err := os.Symlink(worktree, worktreeAlias); err != nil {
		t.Fatal(err)
	}
	// This absolute server path deliberately does not exist on the TUI client.
	other := filepath.Join("/ted-remote-only", t.Name())
	local := remote.WorkspaceSelection{Mode: "current_checkout"}
	fresh := remote.WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}
	for _, tt := range []struct {
		name               string
		options            tuiStartupOptions
		project, directory string
	}{
		{name: "inherit parent without client directory", options: tuiStartupOptions{ParentAgentID: "parent"}, project: "p"},
		{name: "explicit parent checkout", options: tuiStartupOptions{ParentAgentID: "parent", Workspace: &local}, project: "p"},
		{name: "fresh child worktree", options: tuiStartupOptions{ParentAgentID: "parent", Workspace: &fresh}, project: "p"},
		{name: "explicit managed tree", options: tuiStartupOptions{ParentAgentID: "parent", CWD: worktree}, project: "p", directory: worktree},
		{name: "standalone managed tree", options: tuiStartupOptions{CWD: worktree}, project: "p", directory: worktree},
		{name: "cwd overrides parent tree", options: tuiStartupOptions{ParentAgentID: "parent", CWD: root}, project: "p", directory: root},
		{name: "cwd selects another project", options: tuiStartupOptions{ParentAgentID: "parent", CWD: other}, project: "other", directory: other},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var sent struct {
				ProjectID string `json:"project_id"`
				remote.CreateAgentOptions
			}
			creates := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				parent := remote.Snapshot{ID: "parent", ProjectID: "p", Settled: true, Workspace: remote.Workspace{WorkspaceSelection: fresh, Path: worktreeAlias, Status: "ready", Locked: true}}
				p := remote.Project{ID: "p", Root: root}
				switch r.URL.Path {
				case "/v1/models":
					fmt.Fprint(w, `[{"id":"test/model","name":"Test","provider":"test","efforts":["low"],"default_effort":"low"}]`)
				case "/v1/agents/parent":
					json.NewEncoder(w).Encode(parent)
				case "/v1/projects/p":
					json.NewEncoder(w).Encode(p)
				case "/v1/projects":
					if r.Method != "GET" {
						t.Error("managed worktree created a project")
					}
					json.NewEncoder(w).Encode([]remote.Project{p, {ID: "other", Root: other}, {ID: "old-duplicate", Root: worktree}})
				case "/v1/agents":
					if r.Method == "GET" {
						if r.URL.Query().Get("include_settled") != "true" {
							t.Error("settled workspace owners excluded")
						}
						json.NewEncoder(w).Encode([]remote.Snapshot{parent})
					} else {
						creates++
						if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
							t.Error(err)
						}
						json.NewEncoder(w).Encode(remote.Snapshot{ID: "child", ParentAgentID: sent.ParentAgentID, ProjectID: sent.ProjectID, Settings: remote.Settings{Model: "test/model", Effort: "low"}, State: "idle"})
					}
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			if _, err := prepareRemoteAgent(t.Context(), remote.New(server.URL), "", "", "", false, tt.options); err != nil {
				t.Fatal(err)
			}
			if creates != 1 || sent.ProjectID != tt.project || sent.WorkingDirectory != tt.directory || sent.ParentAgentID != tt.options.ParentAgentID {
				t.Fatalf("request: %+v (%d creates)", sent, creates)
			}
			if tt.options.Workspace == nil {
				if sent.Workspace != nil {
					t.Fatalf("implicit workspace sent %+v", sent.Workspace)
				}
			} else if sent.Workspace == nil || *sent.Workspace != *tt.options.Workspace {
				t.Fatalf("workspace %+v", sent.Workspace)
			}
		})
	}
}

func TestTUIDirectoryRelativeSymlinkAndContinue(t *testing.T) {
	provider := &apiTUIProvider{started: make(chan agent.CompletionRequest, 10)}
	s, err := controlplane.NewService(t.TempDir(), nil, []agent.Provider{provider})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	server := httptest.NewServer(controlplane.NewHandler(s))
	defer server.Close()
	c := remote.New(server.URL)
	base := t.TempDir()
	dir := filepath.Join(base, "project")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "alias")
	if err = os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	instance, err := prepareRemoteAgent(t.Context(), c, "", "", "", false, tuiStartupOptions{CWD: "alias"})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if instance.WorkingDir() != canonical {
		t.Fatalf("cwd %s", instance.WorkingDir())
	}
	agents := s.Agents(true, "")
	if len(agents) != 1 {
		t.Fatal(agents)
	}
	parentID := agents[0].ID
	child, err := prepareRemoteAgent(t.Context(), c, "", "", "", false, tuiStartupOptions{ParentAgentID: parentID})
	if err != nil || child.WorkingDir() != canonical {
		t.Fatalf("child %v %v", child, err)
	}
	agents = s.Agents(true, "")
	var childID string
	for _, a := range agents {
		if a.ParentAgentID == parentID {
			childID = a.ID
		}
	}
	if childID == "" {
		t.Fatal("no child")
	}
	// Both resume modes preserve workspace and relationship without creating.
	for _, latest := range []bool{false, true} {
		resume := childID
		opts := tuiStartupOptions{}
		if latest {
			resume = ""
			opts.CWD = "alias"
		}
		restored, err := prepareRemoteAgent(t.Context(), c, "", "", resume, latest, opts)
		if err != nil || restored.WorkingDir() != canonical || len(s.Agents(true, "")) != 2 {
			t.Fatalf("resume: %v %v", restored, err)
		}
	}
	cwd, _ := os.Getwd()
	if cwd != base {
		t.Fatalf("startup changed process cwd to %s", cwd)
	}
}

func TestTUIUnknownParentDoesNotCreateProjectOrAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `[]`)
			return
		}
		if r.URL.Path != "/v1/agents/missing" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":"not_found","message":"agent not found"}}`)
	}))
	defer server.Close()
	_, err := prepareRemoteAgent(t.Context(), remote.New(server.URL), "", "", "", false, tuiStartupOptions{ParentAgentID: "missing", CWD: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "parent agent") {
		t.Fatal(err)
	}
}

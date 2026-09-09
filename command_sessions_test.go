package main

import (
	"bytes"
	"strings"
	"testing"

	"encoding/json"
	"fmt"
	"github.com/TheMarstonConnell/ted/remote"
	"net/http"
	"net/http/httptest"
	"time"
)

func TestSessionsCommand(t *testing.T) {
	var sessions []remote.Snapshot
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"api_version":"1"}`)
		case "/v1/agents":
			if r.URL.Query().Get("include_settled") != "true" || r.URL.Query().Has("project_id") {
				t.Error(r.URL)
			}
			_ = json.NewEncoder(w).Encode(sessions)
		case "/v1/projects":
			fmt.Fprint(w, `[{"id":"p","root":"/server/root"}]`)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	run := func() string {
		t.Helper()
		cmd := newRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"sessions", "--server", server.URL})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := run(); !strings.Contains(got, "No saved sessions") {
		t.Fatal(got)
	}
	sessions = []remote.Snapshot{{ID: "old", Title: "Older", ProjectID: "p", UpdatedAt: time.Now().Add(-time.Hour)}, {ID: "new", Title: "Newest", ProjectID: "p", UpdatedAt: time.Now()}}
	got := run()
	if !strings.Contains(got, "/server/root") || strings.Index(got, "new") > strings.Index(got, "old") {
		t.Fatal(got)
	}
}

func TestResumeFlagsOnTUI(t *testing.T) {
	root := newRootCommand()
	root.SetArgs([]string{"tui", "--continue", "--resume", "id"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "were all set") {
		t.Fatal(err)
	}
}

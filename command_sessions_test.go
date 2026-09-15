package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/remote"
)

func TestSessionsCommand(t *testing.T) {
	var sessions []remote.Snapshot
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"api_version":"1"}`)
		case "/v1/agents":
			w.Header().Set("X-Total-Count", fmt.Sprint(len(sessions)))
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

func TestSessionsPagination(t *testing.T) {
	for _, tc := range []struct {
		name               string
		args               []string
		page, size         string
		first, last, count int
		notice             string
		next               bool
	}{
		{"default", nil, "1", "25", 1, 25, 25, "Showing 1–25 of 63 sessions.", true},
		{"second", []string{"--page", "2"}, "2", "25", 26, 50, 25, "Showing 26–50 of 63 sessions.", true},
		{"custom final", []string{"--page", "7", "--page-size", "10"}, "7", "10", 61, 63, 3, "Showing 61–63 of 63 sessions.", false},
		{"past end", []string{"--page", "100"}, "100", "25", 0, 0, 0, "No results on page 100 (63 sessions total).", false},
		{"all", []string{"--all"}, "", "", 1, 63, 63, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectsRequested := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					fmt.Fprint(w, `{"api_version":"1"}`)
				case "/v1/agents":
					q := r.URL.Query()
					if q.Get("include_settled") != "true" || q.Get("page") != tc.page || q.Get("page_size") != tc.size {
						t.Errorf("query: %s", r.URL)
					}
					if tc.page != "" {
						w.Header().Set("X-Total-Count", "63")
					}
					rows := []remote.Snapshot{}
					// Reverse the response to exercise sorting for --all as well as paged results.
					for i := tc.last; i >= tc.first && tc.count > 0; i-- {
						rows = append(rows, remote.Snapshot{ID: fmt.Sprintf("session-%02d", i), ProjectID: "p", Title: "Saved session", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour)})
					}
					_ = json.NewEncoder(w).Encode(rows)
				case "/v1/projects":
					projectsRequested = true
					fmt.Fprint(w, `[{"id":"p","root":"/server/root"}]`)
				default:
					t.Errorf("unexpected path: %s", r.URL)
				}
			}))
			defer server.Close()
			cmd := newRootCommand()
			var out, notices bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&notices)
			cmd.SetArgs(append([]string{"sessions", "--server", server.URL}, tc.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			rows := strings.Split(strings.TrimSpace(out.String()), "\n")
			if tc.count == 0 {
				if out.Len() != 0 || projectsRequested {
					t.Fatalf("empty page: %s, projects requested %v", out.String(), projectsRequested)
				}
			} else {
				if len(rows) != tc.count+1 || !strings.HasPrefix(rows[0], "ID ") || !strings.HasPrefix(rows[1], fmt.Sprintf("session-%02d ", tc.first)) || !strings.HasPrefix(rows[len(rows)-1], fmt.Sprintf("session-%02d ", tc.last)) {
					t.Fatal(out.String())
				}
				if !strings.Contains(out.String(), "/server/root") {
					t.Fatal(out.String())
				}
			}
			if tc.notice == "" {
				if notices.Len() != 0 {
					t.Fatal(notices.String())
				}
			} else if !strings.HasPrefix(notices.String(), tc.notice+"\n") {
				t.Fatal(notices.String())
			}
			if tc.next {
				page, _ := strconv.Atoi(tc.page)
				if !strings.Contains(notices.String(), "Next: ted sessions --server "+server.URL+" --page "+strconv.Itoa(page+1)) {
					t.Fatal(notices.String())
				}
			} else if strings.Contains(notices.String(), "Next:") {
				t.Fatal(notices.String())
			}
		})
	}
}

package controlplane

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/api"
)

func testPullRequestResolver(run pullRequestCommand) *pullRequestResolver {
	resolver := newPullRequestResolver()
	resolver.run = run
	return resolver
}

func TestParseGitHubRepository(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owner/repo.git":             "owner/repo",
		"ssh://git@github.com/owner/repo.git":       "owner/repo",
		"https://github.com/owner/repo":             "owner/repo",
		"https://github.example.com/Owner/Repo.git": "github.example.com/Owner/Repo",
		"git@github.example.com:team/project.git":   "github.example.com/team/project",
	}
	for remote, want := range cases {
		got, ok := parseGitHubRepository(remote)
		if !ok || got != want {
			t.Errorf("parseGitHubRepository(%q) = %q, %v; want %q, true", remote, got, ok, want)
		}
	}
	for _, remote := range []string{"", "/local/repo", "https://example.com/owner", "git@github.com:owner/repo/extra.git"} {
		if got, ok := parseGitHubRepository(remote); ok {
			t.Errorf("parseGitHubRepository(%q) = %q, true", remote, got)
		}
	}
}

func TestSelectPullRequestPrefersOpenThenLatestClosed(t *testing.T) {
	data := `[
		{"number":21,"headRefName":"feature","state":"MERGED","mergedAt":"2025-01-04T00:00:00Z","headRepository":{"nameWithOwner":"acme/app"}},
		{"number":22,"headRefName":"feature","state":"CLOSED","closedAt":"2025-01-05T00:00:00Z","headRepository":{"name":"app"},"headRepositoryOwner":{"login":"acme"}},
		{"number":7,"headRefName":"feature","state":"OPEN","updatedAt":"2024-01-01T00:00:00Z","headRepository":{"nameWithOwner":"acme/app"}},
		{"number":99,"headRefName":"other","state":"OPEN","headRepository":{"nameWithOwner":"acme/app"}},
		{"number":100,"headRefName":"feature","state":"OPEN","isCrossRepository":true,"headRepository":{"nameWithOwner":"fork/app"}},
		{"number":101,"headRefName":"feature","state":"OPEN","headRepository":{"nameWithOwner":"other/app"}}
	]`
	number, err := selectPullRequest([]byte(data), "acme/app", "feature")
	if err != nil || number != 7 {
		t.Fatalf("open selection = %d, %v; want 7", number, err)
	}

	withoutOpen := strings.Replace(data, `"state":"OPEN","updatedAt":"2024-01-01T00:00:00Z"`, `"state":"CLOSED","updatedAt":"2024-01-01T00:00:00Z"`, 1)
	number, err = selectPullRequest([]byte(withoutOpen), "acme/app", "feature")
	if err != nil || number != 22 {
		t.Fatalf("closed selection = %d, %v; want 22", number, err)
	}
}

func TestPullRequestLookupUsesExactBranchAndRepository(t *testing.T) {
	var commands [][]string
	resolver := testPullRequestResolver(func(_ context.Context, directory, name string, args ...string) ([]byte, error) {
		commands = append(commands, append([]string{directory, name}, args...))
		switch name {
		case "git":
			return []byte("git@github.com:acme/app.git\n"), nil
		case "gh":
			return []byte(`[{"number":42,"headRefName":"ted/exact","state":"OPEN","headRepository":{"nameWithOwner":"acme/app"}}]`), nil
		default:
			return nil, fmt.Errorf("unexpected command %q", name)
		}
	})

	number, err := resolver.lookup(context.Background(), "/repo", "/repo/worktree", "ted/exact")
	if err != nil || number != 42 {
		t.Fatalf("lookup = %d, %v; want 42", number, err)
	}
	want := [][]string{
		{"/repo", "git", "config", "--get", "remote.origin.url"},
		{"/repo/worktree", "gh", "pr", "list", "--head", "ted/exact", "--state", "all", "--limit", "100", "--json", "number,headRefName,state,createdAt,updatedAt,closedAt,mergedAt,headRepository,headRepositoryOwner,isCrossRepository", "--repo", "acme/app"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands:\n got %#v\nwant %#v", commands, want)
	}
}

func TestPullRequestLookupDeduplicatesAndCachesFailures(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var ghCalls atomic.Int32
	resolver := testPullRequestResolver(func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name == "git" {
			return []byte("https://github.com/acme/app.git"), nil
		}
		call := ghCalls.Add(1)
		if call == 1 {
			close(started)
			<-release
			return nil, errors.New("gh unavailable")
		}
		return []byte(`[{"number":8,"headRefName":"feature","state":"MERGED","headRepository":{"nameWithOwner":"acme/app"}}]`), nil
	})
	clock := time.Unix(1000, 0)
	resolver.now = func() time.Time { return clock }

	const callers = 8
	results := make(chan error, callers)
	for range callers {
		go func() {
			_, err := resolver.lookup(context.Background(), "/repo", "/repo", "feature")
			results <- err
		}()
	}
	<-started
	close(release)
	for range callers {
		if err := <-results; err == nil {
			t.Fatal("failed lookup unexpectedly succeeded")
		}
	}
	if _, err := resolver.lookup(context.Background(), "/repo", "/repo", "feature"); err == nil {
		t.Fatal("cached failure unexpectedly succeeded")
	}
	if got := ghCalls.Load(); got != 1 {
		t.Fatalf("gh calls during cache lifetime = %d; want 1", got)
	}

	clock = clock.Add(pullRequestCacheTTL + time.Second)
	number, err := resolver.lookup(context.Background(), "/repo", "/repo", "feature")
	if err != nil || number != 8 {
		t.Fatalf("lookup after expiry = %d, %v; want 8", number, err)
	}
	if got := ghCalls.Load(); got != 2 {
		t.Fatalf("gh calls after expiry = %d; want 2", got)
	}
}

func TestPullRequestLookupCachesMisses(t *testing.T) {
	var ghCalls atomic.Int32
	resolver := testPullRequestResolver(func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name == "git" {
			return []byte("https://github.com/acme/app.git"), nil
		}
		ghCalls.Add(1)
		return []byte(`[]`), nil
	})
	for range 2 {
		number, err := resolver.lookup(context.Background(), "/repo", "/repo", "feature")
		if err != nil || number != 0 {
			t.Fatalf("miss = %d, %v", number, err)
		}
	}
	if got := ghCalls.Load(); got != 1 {
		t.Fatalf("gh calls for cached miss = %d; want 1", got)
	}
}

func TestPullRequestLookupBoundsConcurrency(t *testing.T) {
	started := make(chan struct{}, pullRequestConcurrency+1)
	release := make(chan struct{})
	resolver := testPullRequestResolver(func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name == "git" {
			return []byte("https://github.com/acme/app.git"), nil
		}
		started <- struct{}{}
		<-release
		return []byte(`[]`), nil
	})

	const lookups = pullRequestConcurrency + 4
	done := make(chan error, lookups)
	for i := range lookups {
		go func() {
			_, err := resolver.lookup(context.Background(), "/repo", "/repo", fmt.Sprintf("feature-%d", i))
			done <- err
		}()
	}
	for range pullRequestConcurrency {
		<-started
	}
	select {
	case <-started:
		t.Fatalf("more than %d gh commands ran concurrently", pullRequestConcurrency)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range lookups {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPullRequestLookupCommandTimeout(t *testing.T) {
	resolver := testPullRequestResolver(func(ctx context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name == "git" {
			return []byte("https://github.com/acme/app.git"), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	resolver.timeout = 20 * time.Millisecond
	started := time.Now()
	if _, err := resolver.lookup(context.Background(), "/repo", "/repo", "feature"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lookup error = %v; want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded lookup took %v", elapsed)
	}
}

func TestAgentPullRequestBranchSourceAndFailureBehavior(t *testing.T) {
	f := newHTTPFixture(t, nil)
	project := f.project()
	a := f.agent(project)

	var symbolicCalls atomic.Int32
	resolver := testPullRequestResolver(func(_ context.Context, directory, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 0 && args[0] == "symbolic-ref" {
			symbolicCalls.Add(1)
			if directory != project.Root {
				t.Fatalf("symbolic-ref directory = %q; want project root %q", directory, project.Root)
			}
			return []byte("live-project-branch\n"), nil
		}
		if name == "git" {
			return []byte("https://github.com/acme/app.git"), nil
		}
		return nil, errors.New("gh unavailable")
	})
	f.s.pullRequests = resolver

	got, err := f.s.AgentPullRequest(context.Background(), a.ID)
	if err != nil || got != (AgentPullRequest{Branch: "live-project-branch"}) {
		t.Fatalf("local result = %+v, %v", got, err)
	}

	f.s.mu.Lock()
	record := f.s.state.Agents[a.ID]
	record.Agent.Workspace = Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "worktree"}, Status: "draft", Branch: "recorded", Path: "/worktree"}
	f.s.mu.Unlock()
	got, err = f.s.AgentPullRequest(context.Background(), a.ID)
	if err != nil || got != (AgentPullRequest{}) {
		t.Fatalf("draft worktree result = %+v, %v", got, err)
	}

	f.s.mu.Lock()
	record.Agent.Workspace.Status = "ready"
	record.Agent.Workspace.Shared = true
	record.Agent.Settled = true
	f.s.mu.Unlock()
	got, err = f.s.AgentPullRequest(context.Background(), a.ID)
	if err != nil || got != (AgentPullRequest{Branch: "recorded"}) {
		t.Fatalf("settled inherited worktree result = %+v, %v", got, err)
	}
	if calls := symbolicCalls.Load(); calls != 1 {
		t.Fatalf("symbolic-ref calls = %d; worktree must only use its recorded branch", calls)
	}
}

func TestAgentPullRequestDoesNotHoldServiceLockDuringLookup(t *testing.T) {
	f := newHTTPFixture(t, nil)
	project := f.project()
	a := f.agent(project)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.s.pullRequests = testPullRequestResolver(func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 0 && args[0] == "symbolic-ref" {
			return []byte("feature"), nil
		}
		if name == "git" {
			return []byte("https://github.com/acme/app.git"), nil
		}
		once.Do(func() { close(started) })
		<-release
		return []byte(`[]`), nil
	})

	done := make(chan struct{})
	go func() {
		_, _ = f.s.AgentPullRequest(context.Background(), a.ID)
		close(done)
	}()
	<-started
	readDone := make(chan error, 1)
	go func() {
		_, err := f.s.GetAgent(a.ID)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("GetAgent blocked behind pull request lookup")
	}
	close(release)
	<-done
}

func TestHTTPGetAgentPullRequest(t *testing.T) {
	f := newHTTPFixture(t, nil)
	project := f.project()
	a := f.agent(project)
	f.s.pullRequests = testPullRequestResolver(func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 0 && args[0] == "symbolic-ref" {
			return []byte("feature"), nil
		}
		if name == "git" {
			return []byte("https://github.com/acme/app.git"), nil
		}
		return []byte(`[{"number":33,"headRefName":"feature","state":"OPEN","headRepository":{"nameWithOwner":"acme/app"}}]`), nil
	})

	path := "/v1/agents/" + a.ID + "/pull-request"
	got := decodeHTTP[api.AgentPullRequest](t, f.request("GET", path, "", "", 200))
	if value(got.Branch) != "feature" || value(got.Number) != 33 {
		t.Fatalf("pull request response = %+v", got)
	}
	f.s.mu.Lock()
	f.s.state.Agents[a.ID].Agent.Workspace = Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "worktree"}, Status: "draft"}
	f.s.mu.Unlock()
	if body := string(f.request("GET", path, "", "", 200)); body != "{}\n" {
		t.Fatalf("draft worktree response = %q; want empty object", body)
	}
	f.request("GET", "/v1/agents/missing/pull-request", "", "", 404)
}

func TestAgentPullRequestSkipsMissingProjectAndUsesSharedDraftBranch(t *testing.T) {
	f := newHTTPFixture(t, nil)
	project := f.project()
	a := f.agent(project)
	var commands atomic.Int32
	f.s.pullRequests = testPullRequestResolver(func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		commands.Add(1)
		if name == "git" {
			if args[0] == "symbolic-ref" {
				t.Error("inherited worktree must not use the live project branch")
			}
			return []byte("https://github.com/acme/app.git"), nil
		}
		return []byte(`[{"number":42,"headRefName":"inherited","state":"OPEN","headRepository":{"nameWithOwner":"acme/app"}}]`), nil
	})
	f.s.mu.Lock()
	record := f.s.state.Agents[a.ID]
	record.Agent.Workspace = Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "worktree"}, Shared: true, Status: "draft", Branch: "inherited", Path: "/worktree"}
	f.s.mu.Unlock()
	got, err := f.s.AgentPullRequest(context.Background(), a.ID)
	if err != nil || got != (AgentPullRequest{Branch: "inherited", Number: 42}) {
		t.Fatalf("shared draft result = %+v, %v", got, err)
	}
	before := commands.Load()
	for _, projectID := range []string{"", "missing-project"} {
		f.s.mu.Lock()
		record.Agent.ProjectID = projectID
		record.Agent.Workspace = Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "current_checkout"}}
		f.s.mu.Unlock()
		got, err = f.s.AgentPullRequest(context.Background(), a.ID)
		if err != nil || got != (AgentPullRequest{}) {
			t.Fatalf("missing project result = %+v, %v", got, err)
		}
	}
	if commands.Load() != before {
		t.Fatal("missing-project agents must not execute git or gh")
	}
}

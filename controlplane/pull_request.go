package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	pullRequestCacheTTL       = 30 * time.Second
	pullRequestCommandTimeout = 10 * time.Second
	pullRequestGitTimeout     = 2 * time.Second
	pullRequestConcurrency    = 4
)

type AgentPullRequest struct {
	Branch string `json:"branch,omitempty"`
	Number int    `json:"number,omitempty"`
}

type pullRequestCommand func(context.Context, string, string, ...string) ([]byte, error)

type pullRequestCacheKey struct {
	repository string
	branch     string
}

type pullRequestCacheEntry struct {
	number    int
	err       error
	expiresAt time.Time
	ready     chan struct{}
}

type pullRequestResolver struct {
	mu      sync.Mutex
	cache   map[pullRequestCacheKey]*pullRequestCacheEntry
	run     pullRequestCommand
	now     func() time.Time
	ttl     time.Duration
	timeout time.Duration
	limit   chan struct{}
}

func newPullRequestResolver() *pullRequestResolver {
	return &pullRequestResolver{
		cache:   map[pullRequestCacheKey]*pullRequestCacheEntry{},
		run:     runPullRequestCommand,
		now:     time.Now,
		ttl:     pullRequestCacheTTL,
		timeout: pullRequestCommandTimeout,
		limit:   make(chan struct{}, pullRequestConcurrency),
	}
}

func runPullRequestCommand(ctx context.Context, directory, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GH_PROMPT_DISABLED=1")
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	detail := strings.TrimSpace(string(output))
	if len(detail) > 2000 {
		detail = detail[:2000]
	}
	if detail == "" {
		return nil, err
	}
	return nil, fmt.Errorf("%s: %w: %s", name, err, detail)
}

func (r *pullRequestResolver) currentBranch(ctx context.Context, directory string) string {
	ctx, cancel := context.WithTimeout(ctx, pullRequestGitTimeout)
	defer cancel()
	select {
	case r.limit <- struct{}{}:
		defer func() { <-r.limit }()
	case <-ctx.Done():
		return ""
	}
	output, err := r.run(ctx, directory, "git", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (r *pullRequestResolver) lookup(ctx context.Context, repositoryRoot, directory, branch string) (int, error) {
	key := pullRequestCacheKey{repository: filepath.Clean(repositoryRoot), branch: branch}
	now := r.now()
	r.mu.Lock()
	for existingKey, entry := range r.cache {
		if entry.ready != nil || now.Before(entry.expiresAt) {
			continue
		}
		delete(r.cache, existingKey)
	}
	if entry := r.cache[key]; entry != nil {
		ready := entry.ready
		if ready == nil {
			number, err := entry.number, entry.err
			r.mu.Unlock()
			return number, err
		}
		r.mu.Unlock()
		select {
		case <-ready:
			return r.entryResult(entry)
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	entry := &pullRequestCacheEntry{ready: make(chan struct{})}
	r.cache[key] = entry
	r.mu.Unlock()

	ready := entry.ready
	go r.populate(key, entry, repositoryRoot, directory, branch)
	select {
	case <-ready:
		return r.entryResult(entry)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (r *pullRequestResolver) entryResult(entry *pullRequestCacheEntry) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry.ready != nil {
		return 0, errors.New("pull request lookup did not complete")
	}
	return entry.number, entry.err
}

func (r *pullRequestResolver) populate(key pullRequestCacheKey, entry *pullRequestCacheEntry, repositoryRoot, directory, branch string) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	select {
	case r.limit <- struct{}{}:
		defer func() { <-r.limit }()
	case <-ctx.Done():
		r.finish(key, entry, 0, ctx.Err())
		return
	}
	number, err := r.lookupUncached(ctx, repositoryRoot, directory, branch)
	r.finish(key, entry, number, err)
}

func (r *pullRequestResolver) finish(key pullRequestCacheKey, entry *pullRequestCacheEntry, number int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache[key] != entry {
		return
	}
	ready := entry.ready
	entry.number = number
	entry.err = err
	entry.expiresAt = r.now().Add(r.ttl)
	entry.ready = nil
	close(ready)
}

func (r *pullRequestResolver) lookupUncached(ctx context.Context, repositoryRoot, directory, branch string) (int, error) {
	repository, err := r.githubRepository(ctx, repositoryRoot)
	if err != nil {
		return 0, err
	}
	const fields = "number,headRefName,state,createdAt,updatedAt,closedAt,mergedAt,headRepository,headRepositoryOwner,isCrossRepository"
	output, err := r.run(ctx, directory, "gh", "pr", "list", "--head", branch, "--state", "all", "--limit", "100", "--json", fields, "--repo", repository)
	if err != nil {
		return 0, err
	}
	return selectPullRequest(output, repository, branch)
}

func (r *pullRequestResolver) githubRepository(ctx context.Context, directory string) (string, error) {
	output, err := r.run(ctx, directory, "git", "config", "--get", "remote.origin.url")
	if err != nil {
		return "", fmt.Errorf("read origin remote: %w", err)
	}
	repository, ok := parseGitHubRepository(strings.TrimSpace(string(output)))
	if !ok {
		return "", errors.New("origin is not a GitHub repository")
	}
	return repository, nil
}

func parseGitHubRepository(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", false
	}
	if !strings.Contains(remote, "://") {
		if userHost, path, ok := strings.Cut(remote, ":"); ok && strings.Contains(userHost, "@") {
			host := userHost[strings.LastIndex(userHost, "@")+1:]
			return githubRepositoryName(host, path)
		}
		return "", false
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}
	return githubRepositoryName(parsed.Hostname(), parsed.Path)
}

func githubRepositoryName(host, path string) (string, bool) {
	path = strings.TrimSuffix(strings.Trim(strings.TrimSpace(path), "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.HasPrefix(parts[0], "-") || strings.HasPrefix(parts[1], "-") {
		return "", false
	}
	name := parts[0] + "/" + parts[1]
	if !strings.EqualFold(host, "github.com") {
		name = strings.ToLower(host) + "/" + name
	}
	return name, true
}

type githubPullRequest struct {
	Number              int             `json:"number"`
	HeadRefName         string          `json:"headRefName"`
	State               string          `json:"state"`
	CreatedAt           *time.Time      `json:"createdAt"`
	UpdatedAt           *time.Time      `json:"updatedAt"`
	ClosedAt            *time.Time      `json:"closedAt"`
	MergedAt            *time.Time      `json:"mergedAt"`
	HeadRepository      json.RawMessage `json:"headRepository"`
	HeadRepositoryOwner json.RawMessage `json:"headRepositoryOwner"`
	IsCrossRepository   bool            `json:"isCrossRepository"`
}

func selectPullRequest(data []byte, repository, branch string) (int, error) {
	var candidates []githubPullRequest
	if err := json.Unmarshal(data, &candidates); err != nil {
		return 0, fmt.Errorf("decode gh pull requests: %w", err)
	}
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if candidate.Number < 1 || candidate.HeadRefName != branch || candidate.IsCrossRepository {
			continue
		}
		if head := pullRequestHeadRepository(candidate); head != "" && !sameRepository(head, repository) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		openI := strings.EqualFold(filtered[i].State, "open")
		openJ := strings.EqualFold(filtered[j].State, "open")
		if openI != openJ {
			return openI
		}
		timeI, timeJ := pullRequestTime(filtered[i]), pullRequestTime(filtered[j])
		if !timeI.Equal(timeJ) {
			return timeI.After(timeJ)
		}
		return filtered[i].Number > filtered[j].Number
	})
	if len(filtered) == 0 {
		return 0, nil
	}
	return filtered[0].Number, nil
}

func pullRequestTime(candidate githubPullRequest) time.Time {
	latest := time.Time{}
	for _, timestamp := range []*time.Time{candidate.CreatedAt, candidate.UpdatedAt, candidate.ClosedAt, candidate.MergedAt} {
		if timestamp != nil && timestamp.After(latest) {
			latest = *timestamp
		}
	}
	return latest
}

func pullRequestHeadRepository(candidate githubPullRequest) string {
	var repository struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
	}
	_ = json.Unmarshal(candidate.HeadRepository, &repository)
	if repository.NameWithOwner != "" {
		return repository.NameWithOwner
	}
	var owner struct {
		Login string `json:"login"`
	}
	_ = json.Unmarshal(candidate.HeadRepositoryOwner, &owner)
	if owner.Login != "" && repository.Name != "" {
		return owner.Login + "/" + repository.Name
	}
	return ""
}

func sameRepository(head, repository string) bool {
	parts := strings.Split(repository, "/")
	if len(parts) == 3 {
		repository = strings.Join(parts[1:], "/")
	}
	return strings.EqualFold(head, repository)
}

func (s *Service) AgentPullRequest(ctx context.Context, id string) (AgentPullRequest, error) {
	s.mu.Lock()
	record, err := s.recordLocked(id)
	if err != nil {
		s.mu.Unlock()
		return AgentPullRequest{}, err
	}
	a := record.Agent
	project, hasProject := s.state.Projects[a.ProjectID]
	s.mu.Unlock()
	if !hasProject || project.Root == "" {
		return AgentPullRequest{}, nil
	}

	branch := ""
	directory := project.Root
	if a.Workspace.Mode == "worktree" {
		established := a.Workspace.Status == "ready" || (a.Workspace.Shared && a.Workspace.Status == "draft")
		if !established || a.Workspace.Branch == "" || a.Workspace.Path == "" {
			return AgentPullRequest{}, nil
		}
		branch = a.Workspace.Branch
		directory = a.Workspace.Path
	} else {
		branch = s.pullRequests.currentBranch(ctx, project.Root)
		if branch == "" {
			return AgentPullRequest{}, nil
		}
	}
	result := AgentPullRequest{Branch: branch}
	number, lookupErr := s.pullRequests.lookup(ctx, project.Root, directory, branch)
	if lookupErr == nil && number > 0 {
		result.Number = number
	}
	return result, nil
}

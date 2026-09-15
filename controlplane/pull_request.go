package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
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
	expiresAt time.Time
	ready     chan struct{}
}

type repositoryLookup struct {
	repository string
	expiresAt  time.Time
	ready      chan struct{}
}

type pullRequestResolver struct {
	mu                sync.Mutex
	cache             map[pullRequestCacheKey]*pullRequestCacheEntry
	repositoryLookups map[string]*repositoryLookup
	run               pullRequestCommand
	now               func() time.Time
	ttl               time.Duration
	timeout           time.Duration
	limit             chan struct{}
}

func newPullRequestResolver() *pullRequestResolver {
	return &pullRequestResolver{
		cache:             map[pullRequestCacheKey]*pullRequestCacheEntry{},
		repositoryLookups: map[string]*repositoryLookup{},
		run:               runWorkspaceCommand,
		now:               time.Now,
		ttl:               pullRequestCacheTTL,
		timeout:           pullRequestCommandTimeout,
		limit:             make(chan struct{}, pullRequestConcurrency),
	}
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

func (r *pullRequestResolver) lookup(ctx context.Context, repositoryRoot, directory, branch string) int {
	lookupCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	repository := r.repository(lookupCtx, repositoryRoot)
	if repository == "" {
		return 0
	}
	key := pullRequestCacheKey{repository: repository, branch: branch}
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
			number := entry.number
			r.mu.Unlock()
			return number
		}
		r.mu.Unlock()
		select {
		case <-ready:
			r.mu.Lock()
			number := entry.number
			r.mu.Unlock()
			return number
		case <-ctx.Done():
			return 0
		}
	}
	entry := &pullRequestCacheEntry{ready: make(chan struct{})}
	r.cache[key] = entry
	r.mu.Unlock()

	number := 0
	select {
	case r.limit <- struct{}{}:
		resolved, err := r.lookupUncached(lookupCtx, directory, repository, branch)
		<-r.limit
		if err == nil {
			number = resolved
		}
	case <-lookupCtx.Done():
	}

	r.mu.Lock()
	if r.cache[key] == entry {
		ready := entry.ready
		entry.number = number
		entry.expiresAt = r.now().Add(r.ttl)
		entry.ready = nil
		close(ready)
	}
	r.mu.Unlock()
	return number
}

func (r *pullRequestResolver) repository(ctx context.Context, directory string) string {
	path := filepath.Clean(directory)
	now := r.now()
	r.mu.Lock()
	if entry := r.repositoryLookups[path]; entry != nil {
		if entry.ready == nil && !now.Before(entry.expiresAt) {
			delete(r.repositoryLookups, path)
		} else {
			ready := entry.ready
			if ready == nil {
				repository := entry.repository
				r.mu.Unlock()
				return repository
			}
			r.mu.Unlock()
			select {
			case <-ready:
				r.mu.Lock()
				repository := entry.repository
				r.mu.Unlock()
				return repository
			case <-ctx.Done():
				return ""
			}
		}
	}
	entry := &repositoryLookup{ready: make(chan struct{})}
	r.repositoryLookups[path] = entry
	r.mu.Unlock()

	repository := ""
	select {
	case r.limit <- struct{}{}:
		resolved, err := r.githubRepository(ctx, directory)
		<-r.limit
		if err == nil {
			repository = resolved
		}
	case <-ctx.Done():
	}

	r.mu.Lock()
	if r.repositoryLookups[path] == entry {
		ready := entry.ready
		entry.repository = repository
		entry.ready = nil
		if repository == "" {
			entry.expiresAt = r.now().Add(r.ttl)
		} else {
			delete(r.repositoryLookups, path)
		}
		close(ready)
	}
	r.mu.Unlock()
	return repository
}

func (r *pullRequestResolver) lookupUncached(ctx context.Context, directory, repository, branch string) (int, error) {
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
	var selected *githubPullRequest
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.Number < 1 || candidate.HeadRefName != branch || candidate.IsCrossRepository {
			continue
		}
		if head := pullRequestHeadRepository(*candidate); head != "" && !sameRepository(head, repository) {
			continue
		}
		if selected == nil {
			selected = candidate
			continue
		}
		open, selectedOpen := strings.EqualFold(candidate.State, "open"), strings.EqualFold(selected.State, "open")
		if open != selectedOpen {
			if open {
				selected = candidate
			}
			continue
		}
		latest, selectedLatest := pullRequestTime(*candidate), pullRequestTime(*selected)
		if latest.After(selectedLatest) || latest.Equal(selectedLatest) && candidate.Number > selected.Number {
			selected = candidate
		}
	}
	if selected == nil {
		return 0, nil
	}
	return selected.Number, nil
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
	if number := s.pullRequests.lookup(ctx, project.Root, directory, branch); number > 0 {
		result.Number = number
	}
	return result, nil
}

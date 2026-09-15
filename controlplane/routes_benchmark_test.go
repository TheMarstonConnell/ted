package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const (
	routeBenchmarkProjectID       = "project-main"
	routeBenchmarkDeleteProjectID = "project-delete"
	routeBenchmarkAgentID         = "agent-00"
	routeBenchmarkTurnID          = "queue-00-099"
)

// routeBenchmarkProvider supplies only catalog metadata. Every route benchmark
// prevents a turn from starting, so Complete is a guard against accidentally
// introducing a live model call into this fixture.
type routeBenchmarkProvider struct{}

func (routeBenchmarkProvider) Name() string { return "benchmark" }
func (routeBenchmarkProvider) ListModels() []agent.ModelInfo {
	return []agent.ModelInfo{{
		ID:            "model",
		Name:          "Benchmark Model",
		ContextWindow: 128000,
		Efforts:       []agent.Effort{agent.EffortLow, agent.EffortHigh},
		DefaultEffort: agent.EffortLow,
	}}
}
func (routeBenchmarkProvider) Complete(*zap.Logger, agent.CompletionRequest) (*agent.Response, error) {
	panic("route benchmark unexpectedly launched a model turn")
}

type routeBenchmarkFixture struct {
	handler        http.Handler
	service        *Service
	stateJSON      []byte
	dataDir        string
	projectRoot    string
	newProjectRoot string
}

// newRouteBenchmarkFixture builds a stable, moderately large checkpoint: ten
// agents, each with 100 transcript messages, 100 queue records, and 100 events.
// State resets decode this JSON rather than using snapshot-cloning, and occur with the
// benchmark timer stopped. Mutating routes still run the production saveState,
// including file and directory fsyncs, while timed.
func newRouteBenchmarkFixture(b *testing.B) *routeBenchmarkFixture {
	b.Helper()
	base := b.TempDir()
	// testing.TempDir has a process-dependent name. Pad all paths embedded in
	// request/state JSON to a fixed length so baseline and candidate fixtures
	// have byte-identical sizes despite remaining safely isolated.
	fixedPath := func(component string) string {
		const pathLength = 180
		padding := pathLength - len(base) - 1
		if padding < 1 {
			b.Fatalf("temporary path is unexpectedly long: %q", base)
		}
		return filepath.Join(base, strings.Repeat(component, padding))
	}
	dataDir := fixedPath("s")
	projectRoot := fixedPath("p")
	deleteRoot := fixedPath("d")
	newProjectRoot := fixedPath("n")
	for _, dir := range []string{dataDir, projectRoot, deleteRoot, newProjectRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			b.Fatal(err)
		}
	}

	git := func(args ...string) {
		b.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = projectRoot
		if output, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init", "--initial-branch=main")
	git("-c", "user.name=Benchmark", "-c", "user.email=benchmark@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture")
	git("remote", "add", "origin", "https://github.com/benchmark/fixture.git")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	fixed := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	settings := Settings{Model: "benchmark/model", Effort: "low"}
	state := emptyState()
	state.Projects[routeBenchmarkProjectID] = Project{
		ID:                routeBenchmarkProjectID,
		Name:              "benchmark project",
		Root:              projectRoot,
		Defaults:          settings,
		WorkspaceDefaults: WorkspaceSelection{Mode: "current_checkout"},
	}
	state.Projects[routeBenchmarkDeleteProjectID] = Project{
		ID:                routeBenchmarkDeleteProjectID,
		Name:              "deletable benchmark project",
		Root:              deleteRoot,
		Defaults:          settings,
		WorkspaceDefaults: WorkspaceSelection{Mode: "current_checkout"},
	}

	messagePayload := "representative transcript payload: " + strings.Repeat("m", 224)
	eventPayload := "representative event payload: " + strings.Repeat("e", 224)
	queuePayload := "queued message payload: " + strings.Repeat("q", 48)
	for agentIndex := 0; agentIndex < 10; agentIndex++ {
		id := fmt.Sprintf("agent-%02d", agentIndex)
		messages := make([]agent.Message, 100)
		queue := make([]QueuedMessage, 100)
		events := make([]Event, 100)
		for i := 0; i < 100; i++ {
			role := "user"
			if i%2 != 0 {
				role = "assistant"
			}
			messages[i] = agent.Message{
				Role:        role,
				Content:     agent.TextContent(fmt.Sprintf("%s agent=%02d message=%03d", messagePayload, agentIndex, i)),
				SourceModel: "benchmark/model",
			}
			queue[i] = QueuedMessage{
				ID:        fmt.Sprintf("queue-%02d-%03d", agentIndex, i),
				Text:      fmt.Sprintf("%s agent=%02d message=%03d", queuePayload, agentIndex, i),
				Status:    "completed",
				CreatedAt: fixed.Add(time.Duration(i) * time.Second),
			}
			data, err := json.Marshal(struct {
				Index   int    `json:"index"`
				Payload string `json:"payload"`
			}{i, fmt.Sprintf("%s agent=%02d", eventPayload, agentIndex)})
			if err != nil {
				b.Fatal(err)
			}
			events[i] = Event{
				AgentID:   id,
				Cursor:    uint64(i + 1),
				Type:      "output",
				Data:      data,
				CreatedAt: fixed.Add(time.Duration(i) * time.Second),
			}
		}
		state.Agents[id] = &storedAgent{
			Agent: Agent{
				Workspace: Workspace{
					WorkspaceSelection: WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"},
					Status:             "draft",
				},
				ID:                 id,
				ProjectID:          routeBenchmarkProjectID,
				Title:              fmt.Sprintf("benchmark agent %02d", agentIndex),
				Settings:           settings,
				State:              "idle",
				Held:               true,
				Queue:              queue,
				Messages:           messages,
				ContextUsage:       agent.ContextUsage{Model: "benchmark/model", InputTokens: 8000, OutputTokens: 1000, EstimatedTokens: 9000, ContextWindow: 128000, Known: true},
				Cursor:             100,
				LastResponseCursor: 99,
				ReadCursor:         80,
				CreatedAt:          fixed.Add(time.Duration(agentIndex) * time.Minute),
				UpdatedAt:          fixed.Add(time.Duration(agentIndex) * time.Minute),
			},
			Events: events,
		}
	}

	stateJSON, err := json.Marshal(state)
	if err != nil {
		b.Fatal(err)
	}
	var decoded diskState
	if err = json.Unmarshal(stateJSON, &decoded); err != nil {
		b.Fatal(err)
	}
	if err = saveState(dataDir, decoded); err != nil {
		b.Fatal(err)
	}

	logger := zap.NewNop()
	providers := []agent.Provider{routeBenchmarkProvider{}}
	s := &Service{
		dir:          dataDir,
		logger:       logger,
		providers:    providers,
		catalog:      agent.NewAgent(logger, providers),
		state:        decoded,
		running:      map[string]*runningTurn{},
		instances:    map[string]*agent.Agent{},
		changed:      make(chan struct{}),
		save:         saveState,
		pullRequests: newPullRequestResolver(),
	}
	return &routeBenchmarkFixture{
		handler:        NewHandler(s),
		service:        s,
		stateJSON:      stateJSON,
		dataDir:        dataDir,
		projectRoot:    projectRoot,
		newProjectRoot: newProjectRoot,
	}
}

// reset replaces all mutable runtime state from the encoded fixture. prepare is
// for route-specific successful preconditions and runs while the service lock
// is held. In particular, no reset uses the production snapshot-cloning helper.
func (f *routeBenchmarkFixture) reset(b *testing.B, prepare func(*Service)) {
	b.Helper()
	var state diskState
	if err := json.Unmarshal(f.stateJSON, &state); err != nil {
		b.Fatal(err)
	}
	f.service.mu.Lock()
	f.service.state = state
	f.service.running = map[string]*runningTurn{}
	f.service.instances = map[string]*agent.Agent{}
	f.service.changed = make(chan struct{})
	f.service.closing = false
	f.service.storageErr = nil
	if prepare != nil {
		prepare(f.service)
	}
	f.service.mu.Unlock()
}

type routeBenchmarkCase struct {
	name    string
	method  string
	path    string
	body    []byte
	status  int
	mutates bool
	prepare func(*Service)
}

func benchmarkHandlerRoute(b *testing.B, fixture *routeBenchmarkFixture, route routeBenchmarkCase) {
	b.Helper()
	b.StopTimer()
	fixture.reset(b, route.prepare)
	b.ReportAllocs()
	b.ReportMetric(float64(len(fixture.stateJSON)), "fixture_B")
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if route.mutates {
			fixture.reset(b, route.prepare)
		}
		req := httptest.NewRequest(route.method, route.path, bytes.NewReader(route.body))
		if len(route.body) != 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		b.StartTimer()
		fixture.handler.ServeHTTP(recorder, req)
		b.StopTimer()
		if recorder.Code != route.status {
			b.Fatalf("%s %s: got status %d, want %d: %s", route.method, route.path, recorder.Code, route.status, recorder.Body.String())
		}
	}
}

// BenchmarkRoutes exercises every OpenAPI HTTP operation through the real
// validated handler. HTTP requests use httptest directly, so their timings are
// local handler processing rather than client or network latency. Mutations
// include the production durable checkpoint write and fsync. The WebSocket case
// is separate because a genuine upgrade necessarily uses a loopback socket.
func BenchmarkRoutes(b *testing.B) {
	fixture := newRouteBenchmarkFixture(b)
	projectPath := "/v1/projects/" + routeBenchmarkProjectID
	agentPath := "/v1/agents/" + routeBenchmarkAgentID
	createProjectBody, err := json.Marshal(map[string]any{
		"name": "created by benchmark",
		"root": fixture.newProjectRoot,
		"defaults": map[string]string{
			"model":  "benchmark/model",
			"effort": "low",
		},
	})
	if err != nil {
		b.Fatal(err)
	}

	routes := []routeBenchmarkCase{
		{name: "GET_Health", method: http.MethodGet, path: "/health", status: http.StatusOK},
		{name: "GET_ListProjects", method: http.MethodGet, path: "/v1/projects", status: http.StatusOK},
		{name: "POST_CreateProject", method: http.MethodPost, path: "/v1/projects", body: createProjectBody, status: http.StatusCreated, mutates: true},
		{name: "GET_GetProject", method: http.MethodGet, path: projectPath, status: http.StatusOK},
		{name: "PATCH_PatchProject", method: http.MethodPatch, path: projectPath, body: []byte(`{"name":"renamed benchmark project"}`), status: http.StatusOK, mutates: true},
		{name: "DELETE_DeleteProject", method: http.MethodDelete, path: "/v1/projects/" + routeBenchmarkDeleteProjectID, status: http.StatusNoContent, mutates: true},
		{name: "GET_ListProjectBranches", method: http.MethodGet, path: projectPath + "/branches", status: http.StatusOK},
		{name: "GET_ListAgents", method: http.MethodGet, path: "/v1/agents?include_settled=true", status: http.StatusOK},
		{name: "GET_ListAgentsLatePage", method: http.MethodGet, path: "/v1/agents?include_settled=true&page=2&page_size=5", status: http.StatusOK},
		{name: "POST_CreateAgent", method: http.MethodPost, path: "/v1/agents", body: []byte(`{"project_id":"project-main","title":"created agent"}`), status: http.StatusCreated, mutates: true},
		{name: "GET_GetAgent", method: http.MethodGet, path: agentPath, status: http.StatusOK},
		{name: "PATCH_PatchAgent", method: http.MethodPatch, path: agentPath, body: []byte(`{"settled":true}`), status: http.StatusOK, mutates: true},
		{name: "GET_GetAgentPullRequest", method: http.MethodGet, path: agentPath + "/pull-request", status: http.StatusOK},
		{name: "GET_GetAgentPullRequestWarmStub", method: http.MethodGet, path: agentPath + "/pull-request", status: http.StatusOK,
			prepare: func(s *Service) {
				a := &s.state.Agents[routeBenchmarkAgentID].Agent
				a.Workspace.Status = "ready"
				a.Workspace.Path = fixture.projectRoot
				a.Workspace.Branch = "benchmark-branch"
				s.pullRequests = newPullRequestResolver()
				s.pullRequests.run = func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
					if name == "git" {
						return []byte("https://github.com/benchmark/fixture.git"), nil
					}
					b.Fatal("warm PR benchmark unexpectedly queried gh")
					return nil, nil
				}
				s.pullRequests.cache[pullRequestCacheKey{repository: "benchmark/fixture", branch: a.Workspace.Branch}] = &pullRequestCacheEntry{number: 42, expiresAt: time.Now().Add(time.Hour)}
			}},
		{name: "PATCH_PatchAgentWorkspace", method: http.MethodPatch, path: agentPath + "/workspace", body: []byte(`{"mode":"current_checkout"}`), status: http.StatusOK, mutates: true},
		{name: "PATCH_PatchAgentSettings", method: http.MethodPatch, path: agentPath + "/settings", body: []byte(`{"effort":"high"}`), status: http.StatusOK, mutates: true},
		{name: "GET_ListMessagesQueue100", method: http.MethodGet, path: agentPath + "/messages", status: http.StatusOK},
		{
			name: "POST_SubmitMessage", method: http.MethodPost, path: agentPath + "/messages", body: []byte(`{"text":"new benchmark message"}`), status: http.StatusAccepted, mutates: true,
			prepare: func(s *Service) {
				// Existing running state makes startLocked a no-op after Submit.
				s.running[routeBenchmarkAgentID] = &runningTurn{id: "submit-stub", cancel: func() {}}
			},
		},
		{
			name: "DELETE_DeletePending", method: http.MethodDelete, path: agentPath + "/messages/" + routeBenchmarkTurnID, status: http.StatusNoContent, mutates: true,
			prepare: func(s *Service) {
				s.state.Agents[routeBenchmarkAgentID].Agent.Queue[99].Status = "pending"
			},
		},
		{
			name: "POST_StopAgent", method: http.MethodPost, path: agentPath + "/stop", body: []byte(`{"turn_id":"queue-00-099"}`), status: http.StatusOK, mutates: true,
			prepare: func(s *Service) {
				a := &s.state.Agents[routeBenchmarkAgentID].Agent
				a.Queue[99].Status = "running"
				a.State = "running"
				s.running[routeBenchmarkAgentID] = &runningTurn{id: routeBenchmarkTurnID, cancel: func() {}}
			},
		},
		{
			name: "POST_ContinueAgentEmptyQueue", method: http.MethodPost, path: agentPath + "/continue", status: http.StatusOK, mutates: true,
			prepare: func(s *Service) {
				a := &s.state.Agents[routeBenchmarkAgentID].Agent
				a.Queue = []QueuedMessage{}
				a.Held = true
			},
		},
		{name: "GET_GetHistoryLatePage", method: http.MethodGet, path: agentPath + "/history?after=90&limit=10", status: http.StatusOK},
		{name: "GET_GetEventsLatePage", method: http.MethodGet, path: agentPath + "/events?after=90&limit=10", status: http.StatusOK},
		{name: "GET_GetEvent", method: http.MethodGet, path: agentPath + "/events/95", status: http.StatusOK},
		{name: "GET_ListModels", method: http.MethodGet, path: "/v1/models", status: http.StatusOK},
	}
	for _, route := range routes {
		route := route
		b.Run(route.name, func(b *testing.B) {
			benchmarkHandlerRoute(b, fixture, route)
		})
	}

	b.Run("GET_WebSocketHandshake", func(b *testing.B) {
		benchmarkWebSocketHandshake(b, fixture)
	})
}

func benchmarkWebSocketHandshake(b *testing.B, fixture *routeBenchmarkFixture) {
	b.Helper()
	b.StopTimer()
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/ws"
	dialer := *websocket.DefaultDialer
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StartTimer()
		connection, response, err := dialer.DialContext(context.Background(), url, nil)
		b.StopTimer()
		if err != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			b.Fatalf("WebSocket handshake failed (status %d): %v", status, err)
		}
		if response == nil || response.StatusCode != http.StatusSwitchingProtocols {
			connection.Close()
			b.Fatalf("WebSocket handshake got response %#v, want status 101", response)
		}
		if err = connection.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// Separate CPU/allocation cost from filesystem sync variance; not route latency.
func BenchmarkDeleteProjectCPU(b *testing.B) {
	fixture := newRouteBenchmarkFixture(b)
	fixture.service.save = func(string, diskState) error { return nil }
	benchmarkHandlerRoute(b, fixture, routeBenchmarkCase{
		name: "DELETE_DeleteProject", method: http.MethodDelete,
		path: "/v1/projects/" + routeBenchmarkDeleteProjectID, status: http.StatusNoContent, mutates: true,
	})
}

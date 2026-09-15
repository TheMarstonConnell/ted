package controlplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/TheMarstonConnell/ted/internal/gitstatus"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/TheMarstonConnell/ted/api"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

const maxHTTPBody = 2 << 20

// httpAPI adapts generated transport types to the durable runtime. The generated
// ServerInterface and HandlerWithOptions are the only HTTP route implementation.
type httpAPI struct {
	service *Service
	spec    *openapi3.T
}

var _ api.ServerInterface = (*httpAPI)(nil)

// NewHandler exposes the versioned, spec-validated HTTP and WebSocket API.
// Authentication/TLS, if needed, belong at the caller's trusted reverse proxy.
func NewHandler(s *Service) http.Handler {
	definition, err := sharedHTTPDefinition()
	if err != nil {
		panic(err)
	}
	h := &httpAPI{service: s, spec: definition.spec}
	generated := api.HandlerWithOptions(h, api.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) { writeProblem(w, 400, "invalid", err.Error()) }})
	return validateHTTP(definition.router, generated, definition.metadata)
}

type httpRouteMetadata struct {
	query map[string]struct{}
	allow string
}
type httpDefinition struct {
	spec     *openapi3.T
	router   routers.Router
	metadata map[*openapi3.Operation]httpRouteMetadata
}

var (
	emptyQuery            = url.Values{}
	requestValidationOpts = &openapi3filter.Options{SkipSettingDefaults: true}
	sharedHTTPDefinition  = sync.OnceValues(func() (httpDefinition, error) {
		spec, err := api.GetSwagger()
		if err != nil {
			return httpDefinition{}, fmt.Errorf("load embedded API spec: %w", err)
		}
		// NewRouter validates the complete document before constructing routes.
		router, err := legacy.NewRouter(spec)
		if err != nil {
			return httpDefinition{}, fmt.Errorf("invalid API spec: %w", err)
		}
		return httpDefinition{spec: spec, router: router, metadata: newHTTPValidationMetadata(spec)}, nil
	})
)

func newHTTPValidationMetadata(spec *openapi3.T) map[*openapi3.Operation]httpRouteMetadata {
	metadata := make(map[*openapi3.Operation]httpRouteMetadata)
	for _, pathItem := range spec.Paths.Map() {
		operations := pathItem.Operations()
		allow := make([]string, 0, len(operations))
		for method := range operations {
			allow = append(allow, method)
		}
		sort.Strings(allow)
		allowHeader := strings.Join(allow, ", ")
		for _, operation := range operations {
			var query map[string]struct{}
			for _, parameters := range []openapi3.Parameters{pathItem.Parameters, operation.Parameters} {
				for _, parameter := range parameters {
					if parameter.Value.In == openapi3.ParameterInQuery {
						if query == nil {
							query = make(map[string]struct{})
						}
						query[parameter.Value.Name] = struct{}{}
					}
				}
			}
			metadata[operation] = httpRouteMetadata{query: query, allow: allowHeader}
		}
	}
	return metadata
}

// Validate against the same embedded document used to generate server types.
// In addition to schema validation, reject duplicate/unknown query parameters,
// unexpected bodies, trailing JSON, and oversized bodies before runtime mutation.
func validateHTTP(router routers.Router, next http.Handler, metadata map[*openapi3.Operation]httpRouteMetadata) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, pathParams, err := router.FindRoute(r)
		if err != nil {
			// legacy.Router reports method mismatches on templated paths as path misses.
			for _, method := range []string{"GET", "POST", "PATCH", "DELETE", "PUT", "HEAD", "OPTIONS"} {
				probe := new(http.Request)
				*probe = *r
				probe.Method = method
				if matched, _, probeErr := router.FindRoute(probe); probeErr == nil {
					w.Header().Set("Allow", metadata[matched.Operation].allow)
					writeProblem(w, 405, "method_not_allowed", "method not allowed")
					return
				}
			}
			writeProblem(w, 404, "not_found", "route not found")
			return
		}

		routeMetadata := metadata[route.Operation]
		query := emptyQuery
		if r.URL.RawQuery != "" {
			query, err = parseQuery(r)
			if err != nil {
				writeProblem(w, 400, "invalid", err.Error())
				return
			}
			for key, values := range query {
				if _, allowed := routeMetadata.query[key]; !allowed || len(values) != 1 {
					writeProblem(w, 400, "invalid", "unknown or repeated query parameter: "+key)
					return
				}
			}
		}
		if len(r.Header.Values("Idempotency-Key")) > 1 {
			writeProblem(w, 400, "invalid", "repeated Idempotency-Key header")
			return
		}

		var body []byte
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, maxHTTPBody)
			body, err = io.ReadAll(r.Body)
			if err != nil {
				var large *http.MaxBytesError
				if errors.As(err, &large) {
					writeProblem(w, 413, "too_large", "request body exceeds 2 MiB")
				} else {
					writeProblem(w, 400, "invalid", "cannot read request body")
				}
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if route.Operation.RequestBody == nil && len(body) > 0 {
			writeProblem(w, 400, "invalid", "request body not allowed")
			return
		}
		if route.Operation.RequestBody != nil {
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || media != "application/json" {
				writeProblem(w, 415, "unsupported_media_type", "expected application/json")
				return
			}
			if !json.Valid(body) {
				writeProblem(w, 400, "invalid", "expected exactly one valid JSON value")
				return
			}
		}
		input := &openapi3filter.RequestValidationInput{
			Request: r, PathParams: pathParams, QueryParams: query, Route: route,
			Options: requestValidationOpts,
		}
		if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
			writeProblem(w, 400, "invalid", err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseQuery(r *http.Request) (map[string][]string, error) {
	// URL.Query silently drops malformed pairs; strict APIs must not.
	return url.ParseQuery(r.URL.RawQuery)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		writeProblem(w, 500, "internal", "cannot encode response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}
func writeProblem(w http.ResponseWriter, status int, code, message string) {
	var v api.Error
	v.Error.Code = api.ErrorCode(code)
	v.Error.Message = message
	writeJSON(w, status, v)
}
func writeRuntimeError(w http.ResponseWriter, err error) {
	var e *Error
	if errors.As(err, &e) {
		writeProblem(w, e.Status, e.Code, e.Message)
	} else {
		writeProblem(w, 500, "internal", "internal server error")
	}
}
func respond[T any](w http.ResponseWriter, status int, v T, err error) {
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	writeJSON(w, status, v)
}
func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		writeProblem(w, 400, "invalid", err.Error())
		return v, false
	}
	return v, true
}
func value[T any](p *T) (zero T) {
	if p != nil {
		return *p
	}
	return
}
func pagination(after, limit *int64) (uint64, int) {
	n := 100
	if limit != nil {
		n = int(*limit)
	}
	return uint64(value(after)), n
}
func nonnil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
func workspaceSelection(v api.WorkspaceSelection) WorkspaceSelection {
	return WorkspaceSelection{Mode: string(v.Mode), BaseBranch: value(v.BaseBranch)}
}
func optionalWorkspaceSelection(v *api.WorkspaceSelection) *WorkspaceSelection {
	if v == nil {
		return nil
	}
	selection := workspaceSelection(*v)
	return &selection
}

// Normalize optional runtime slices without altering detached runtime data.
type httpAgent struct {
	Agent
	ContextUsage api.ContextUsage `json:"context_usage"`
}

func wireAgent(a Agent) httpAgent {
	a.Queue = nonnil(a.Queue)
	a.Messages = nonnil(a.Messages)
	u := a.ContextUsage
	return httpAgent{a, api.ContextUsage{Model: u.Model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, EstimatedTokens: u.EstimatedTokens, ContextWindow: u.ContextWindow, Known: u.Known, Estimated: u.Estimated}}
}
func agentResult(w http.ResponseWriter, status int, a Agent, err error) {
	respond(w, status, wireAgent(a), err)
}

func (h *httpAPI) GetHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, api.Health{ApiVersion: api.N1})
}
func (h *httpAPI) ListProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, nonnil(h.service.Projects()))
}
func (h *httpAPI) CreateProject(w http.ResponseWriter, r *http.Request) {
	b, ok := decodeBody[api.CreateProjectJSONRequestBody](w, r)
	if !ok {
		return
	}
	p, err := h.service.CreateProject(CreateProjectRequest{Name: b.Name, Root: b.Root, Defaults: Settings{Model: b.Defaults.Model, Effort: b.Defaults.Effort}, WorkspaceDefaults: optionalWorkspaceSelection(b.WorkspaceDefaults)})
	respond(w, 201, p, err)
}
func (h *httpAPI) GetProject(w http.ResponseWriter, r *http.Request, id string) {
	p, err := h.service.GetProject(id)
	if err == nil {
		p.GitBranch = gitstatus.Branch(p.Root)
	}
	respond(w, 200, p, err)
}
func (h *httpAPI) PatchProject(w http.ResponseWriter, r *http.Request, id string) {
	b, ok := decodeBody[api.PatchProjectJSONRequestBody](w, r)
	if !ok {
		return
	}
	var defaults *Settings
	if b.Defaults != nil {
		defaults = &Settings{Model: b.Defaults.Model, Effort: b.Defaults.Effort}
	}
	p, err := h.service.UpdateProject(id, b.Name, defaults, optionalWorkspaceSelection(b.WorkspaceDefaults))
	respond(w, 200, p, err)
}
func (h *httpAPI) ListProjectBranches(w http.ResponseWriter, r *http.Request, id string) {
	branches, err := h.service.ProjectBranches(id)
	if err == nil {
		branches.Branches = nonnil(branches.Branches)
	}
	respond(w, 200, branches, err)
}
func (h *httpAPI) DeleteProject(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.service.DeleteProject(id); err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *httpAPI) ListAgents(w http.ResponseWriter, r *http.Request, p api.ListAgentsParams) {
	if p.ProjectId != nil {
		if _, err := h.service.GetProject(*p.ProjectId); err != nil {
			writeRuntimeError(w, err)
			return
		}
	}
	var result []Agent
	if p.Page != nil || p.PageSize != nil {
		page, pageSize := int64(1), int64(25)
		if p.Page != nil {
			page = *p.Page
		}
		if p.PageSize != nil {
			pageSize = *p.PageSize
		}
		var total int
		result, total = h.service.AgentsPage(value(p.IncludeSettled), value(p.ProjectId), page, pageSize)
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
	} else {
		result = h.service.Agents(value(p.IncludeSettled), value(p.ProjectId))
	}
	agents := make([]httpAgent, 0, len(result))
	for _, a := range result {
		agents = append(agents, wireAgent(a))
	}
	writeJSON(w, 200, agents)
}
func (h *httpAPI) CreateAgent(w http.ResponseWriter, r *http.Request, p api.CreateAgentParams) {
	b, ok := decodeBody[api.CreateAgentJSONRequestBody](w, r)
	if !ok {
		return
	}
	var settings *Settings
	if b.Settings != nil {
		settings = &Settings{Model: value(b.Settings.Model), Effort: value(b.Settings.Effort)}
	}
	a, err := h.service.CreateAgent(CreateAgentRequest{ParentAgentID: value(b.ParentAgentId), WorkingDirectory: value(b.WorkingDirectory), ProjectID: b.ProjectId, Title: value(b.Title), Prompt: value(b.Prompt), Settings: settings, Workspace: optionalWorkspaceSelection(b.Workspace)}, value(p.IdempotencyKey))
	agentResult(w, 201, a, err)
}
func (h *httpAPI) GetAgent(w http.ResponseWriter, r *http.Request, id string) {
	a, err := h.service.GetAgent(id)
	agentResult(w, 200, a, err)
}
func (h *httpAPI) GetAgentPullRequest(w http.ResponseWriter, r *http.Request, id string) {
	pullRequest, err := h.service.AgentPullRequest(r.Context(), id)
	respond(w, 200, pullRequest, err)
}
func (h *httpAPI) PatchAgent(w http.ResponseWriter, r *http.Request, id string) {
	b, ok := decodeBody[api.PatchAgentJSONRequestBody](w, r)
	if !ok {
		return
	}
	if b.ReadCursor != nil {
		a, err := h.service.ReadAgent(id, uint64(*b.ReadCursor))
		agentResult(w, 200, a, err)
		return
	}
	a, err := h.service.SetSettled(id, *b.Settled)
	agentResult(w, 200, a, err)
}
func (h *httpAPI) PatchAgentSettings(w http.ResponseWriter, r *http.Request, id string) {
	b, ok := decodeBody[api.PatchAgentSettingsJSONRequestBody](w, r)
	if !ok {
		return
	}
	a, err := h.service.UpdateSettings(id, SettingsPatch{Model: b.Model, Effort: b.Effort})
	agentResult(w, 200, a, err)
}
func (h *httpAPI) PatchAgentWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	b, ok := decodeBody[api.PatchAgentWorkspaceJSONRequestBody](w, r)
	if !ok {
		return
	}
	a, err := h.service.UpdateWorkspace(id, workspaceSelection(b))
	agentResult(w, 200, a, err)
}
func (h *httpAPI) ListMessages(w http.ResponseWriter, r *http.Request, id string) {
	queue, err := h.service.QueuedMessages(id)
	respond(w, 200, nonnil(queue), err)
}
func (h *httpAPI) SubmitMessage(w http.ResponseWriter, r *http.Request, id string, p api.SubmitMessageParams) {
	b, ok := decodeBody[api.SubmitMessageJSONRequestBody](w, r)
	if !ok {
		return
	}
	m, err := h.service.Submit(id, b.Text, value(p.IdempotencyKey))
	respond(w, 202, m, err)
}
func (h *httpAPI) DeletePending(w http.ResponseWriter, r *http.Request, id, mid string) {
	if err := h.service.DeletePending(id, mid); err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *httpAPI) StopAgent(w http.ResponseWriter, r *http.Request, id string) {
	b, ok := decodeBody[api.StopAgentJSONRequestBody](w, r)
	if !ok {
		return
	}
	a, err := h.service.Stop(id, b.TurnId)
	agentResult(w, 200, a, err)
}
func (h *httpAPI) ContinueAgent(w http.ResponseWriter, r *http.Request, id string) {
	a, err := h.service.Continue(id)
	agentResult(w, 200, a, err)
}
func (h *httpAPI) GetEvents(w http.ResponseWriter, r *http.Request, id string, p api.GetEventsParams) {
	after, limit := pagination(p.After, p.Limit)
	events, err := h.service.Events(id, after, limit)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	next := after
	if len(events) > 0 {
		next = events[len(events)-1].Cursor
	}
	writeJSON(w, 200, struct {
		Events []Event `json:"events"`
		Next   uint64  `json:"next_cursor"`
	}{nonnil(events), next})
}
func (h *httpAPI) GetEvent(w http.ResponseWriter, r *http.Request, id string, cursor int64) {
	events, err := h.service.Events(id, uint64(cursor-1), 1)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	if len(events) == 0 || events[0].Cursor != uint64(cursor) {
		writeProblem(w, 404, "not_found", "event not found")
		return
	}
	writeJSON(w, 200, events[0])
}
func (h *httpAPI) GetHistory(w http.ResponseWriter, r *http.Request, id string, p api.GetHistoryParams) {
	after, limit := pagination(p.After, p.Limit)
	messages, end, err := h.service.History(id, after, limit)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Messages any `json:"messages"`
		Next     int `json:"next_cursor"`
	}{nonnil(messages), end})
}
func (h *httpAPI) ListModels(w http.ResponseWriter, r *http.Request) {
	models := []api.Model{}
	for _, m := range h.service.Models() {
		efforts := []string{}
		for _, e := range m.Efforts {
			efforts = append(efforts, string(e))
		}
		models = append(models, api.Model{Id: m.ID, Name: m.Name, Provider: m.Provider, ContextWindow: m.ContextWindow, Efforts: efforts, DefaultEffort: string(m.DefaultEffort)})
	}
	writeJSON(w, 200, models)
}

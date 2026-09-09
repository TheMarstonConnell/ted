package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/api"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"go.uber.org/zap"
)

type httpTestProvider struct {
	complete func(agent.CompletionRequest) (*agent.Response, error)
}

func (*httpTestProvider) Name() string { return "http-test" }
func (*httpTestProvider) ListModels() []agent.ModelInfo {
	return []agent.ModelInfo{{ID: "one", Name: "One", ContextWindow: 10000, Efforts: []agent.Effort{agent.EffortLow, agent.EffortHigh}, DefaultEffort: agent.EffortLow}, {ID: "two", Name: "Two", Efforts: []agent.Effort{agent.EffortLow, agent.EffortHigh}, DefaultEffort: agent.EffortHigh}}
}
func (p *httpTestProvider) Complete(_ *zap.Logger, r agent.CompletionRequest) (*agent.Response, error) {
	if p.complete != nil {
		return p.complete(r)
	}
	return &agent.Response{Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("done")}}}}, nil
}

type httpFixture struct {
	s      *Service
	server *httptest.Server
	t      *testing.T
}

func newHTTPFixture(t *testing.T, p *httpTestProvider) *httpFixture {
	t.Helper()
	if p == nil {
		p = &httpTestProvider{}
	}
	s, err := NewService(t.TempDir(), zap.NewNop(), []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(s))
	f := &httpFixture{s, server, t}
	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}
func (f *httpFixture) request(method, path, body, key string, want int) []byte {
	f.t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := f.server.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if res.StatusCode != want {
		f.t.Fatalf("%s %s: got %d want %d: %s", method, path, res.StatusCode, want, data)
	}
	if want != 204 && res.Header.Get("Content-Type") != "application/json" {
		f.t.Fatalf("non-JSON response: %v", res.Header)
	}
	if res.Header.Get("Access-Control-Allow-Origin") == "*" {
		f.t.Fatal("wildcard CORS")
	}
	// Exercise response shapes against the spec too, not just handwritten structs.
	spec, err := api.GetSwagger()
	if err != nil {
		f.t.Fatal(err)
	}
	router, err := legacy.NewRouter(spec)
	if err != nil {
		f.t.Fatal(err)
	}
	route, params, err := router.FindRoute(req)
	if err == nil {
		input := &openapi3filter.ResponseValidationInput{RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route}, Status: res.StatusCode, Header: res.Header, Body: io.NopCloser(bytes.NewReader(data)), Options: &openapi3filter.Options{IncludeResponseStatus: true}}
		if err = openapi3filter.ValidateResponse(context.Background(), input); err != nil {
			f.t.Fatalf("response violates spec: %v\n%s", err, data)
		}
	}
	return data
}
func decodeHTTP[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func (f *httpFixture) project() Project {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "test", "root": f.t.TempDir(), "defaults": Settings{Model: "http-test/one", Effort: "low"}})
	return decodeHTTP[Project](f.t, f.request("POST", "/v1/projects", string(body), "", 201))
}
func (f *httpFixture) agent(p Project) Agent {
	f.t.Helper()
	return decodeHTTP[Agent](f.t, f.request("POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q}`, p.ID), "", 201))
}
func waitHTTPAgent(t *testing.T, s *Service, id string, predicate func(Agent) bool) Agent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a, err := s.GetAgent(id)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(a) {
			return a
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("agent did not reach expected state")
	return Agent{}
}

func TestHTTPGeneratedRoutesProjectAgentCRUD(t *testing.T) {
	f := newHTTPFixture(t, nil)
	f.request("GET", "/health", "", "", 200)
	models := decodeHTTP[[]api.Model](t, f.request("GET", "/v1/models", "", "", 200))
	if len(models) != 2 || models[0].Provider != "http-test" {
		t.Fatal(models)
	}
	if got := string(f.request("GET", "/v1/projects", "", "", 200)); got != "[]\n" {
		t.Fatal(got)
	}
	p := f.project()
	f.request("GET", "/v1/projects/"+p.ID, "", "", 200)
	p = decodeHTTP[Project](t, f.request("PATCH", "/v1/projects/"+p.ID, `{"name":"renamed","defaults":{"model":"http-test/two","effort":"high"}}`, "", 200))
	if p.Name != "renamed" || p.Defaults.Model != "http-test/two" {
		t.Fatal(p)
	}
	a := f.agent(p)
	if a.Settings != p.Defaults {
		t.Fatalf("defaults not inherited: %+v", a)
	}
	f.request("GET", "/v1/agents/"+a.ID, "", "", 200)
	f.request("DELETE", "/v1/projects/"+p.ID, "", "", 409)
	a = decodeHTTP[Agent](t, f.request("PATCH", "/v1/agents/"+a.ID+"/settings", `{"model":"http-test/one","effort":"low"}`, "", 200))
	if a.Settings.Model != "http-test/one" {
		t.Fatal(a.Settings)
	}
	f.request("PATCH", "/v1/agents/"+a.ID, `{"settled":true}`, "", 200)
	if got := string(f.request("GET", "/v1/agents", "", "", 200)); got != "[]\n" {
		t.Fatal(got)
	}
	settled := decodeHTTP[[]Agent](t, f.request("GET", "/v1/agents?include_settled=true&project_id="+p.ID, "", "", 200))
	if len(settled) != 1 || !settled[0].Settled {
		t.Fatal(settled)
	}
	f.request("POST", "/v1/agents/"+a.ID+"/messages", `{"text":"no"}`, "settled", 409)
	f.request("PATCH", "/v1/agents/"+a.ID, `{"settled":false}`, "", 200)
	empty := f.project()
	f.request("DELETE", "/v1/projects/"+empty.ID, "", "", 204)
	f.request("GET", "/v1/projects/"+empty.ID, "", "", 404)
	f.request("GET", "/not-a-route", "", "", 404)
	f.request("PUT", "/health", "", "", 405)
}

func TestHTTPStrictValidationBeforeMutation(t *testing.T) {
	f := newHTTPFixture(t, nil)
	p := f.project()
	a := f.agent(p)
	base := "/v1/agents/" + a.ID
	cases := []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/v1/projects", `{"root":"/tmp"}`, 400},
		{"POST", "/v1/projects", `{"name":"x","root":"/tmp","defaults":{"model":"http-test/one","effort":"low","typo":true}}`, 400},
		{"POST", "/v1/projects", `{"name":"x","root":"/tmp","defaults":{"model":"http-test/one"}}`, 400},
		{"PATCH", "/v1/projects/" + p.ID, `{}`, 400},
		{"POST", "/v1/agents", `{}`, 400},
		{"POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q,"prompt":""}`, p.ID), 400},
		{"POST", base + "/messages", `{}`, 400},
		{"POST", base + "/messages", `{"text":"x","unknown":true}`, 400},
		{"POST", base + "/messages", `{"Text":"x"}`, 400},
		{"POST", base + "/messages", `{"text":null}`, 400},
		{"POST", base + "/messages", `{"text":""}`, 400},
		{"POST", base + "/messages", `{"text":2}`, 400},
		{"POST", base + "/messages", `null`, 400},
		{"POST", base + "/messages", `{"text":"x"} {}`, 400},
		{"POST", base + "/messages", `{"text":"` + strings.Repeat("x", 1048577) + `"}`, 400},
		{"POST", base + "/messages", `{"text":"` + strings.Repeat("x", maxHTTPBody) + `"}`, 413},
		{"PATCH", base, `{}`, 400},
		{"PATCH", base, `{"settled":null}`, 400},
		{"PATCH", base, `{"settled":"false"}`, 400},
		{"PATCH", base + "/settings", `{}`, 400},
		{"PATCH", base + "/settings", `{"model":""}`, 400},
		{"PATCH", base + "/settings", `{"effort":null}`, 400},
		{"PATCH", base + "/settings", `{"model":"http-test/one","temperature":1}`, 400},
		{"POST", base + "/stop", `{}`, 400},
		{"POST", base + "/stop", `{"turn_id":""}`, 400},
		{"POST", base + "/continue", `{}`, 400},
		{"GET", base + "/events?limit=0", "", 400},
		{"GET", base + "/events?limit=1001", "", 400},
		{"GET", base + "/events?after=-1", "", 400},
		{"GET", base + "/events?after=1.5", "", 400},
		{"GET", base + "/events?after=9223372036854775808", "", 400},
		{"GET", base + "/events?after=999999", "", 410},
		{"GET", base + "/events/0", "", 400},
		{"GET", base + "/history?after=1", "", 410},
		{"GET", base + "/events?limit=1&limit=2", "", 400},
		{"GET", base + "/events?unknown=1", "", 400},
		{"GET", base + "/events?limit=%GG", "", 400},
		{"GET", base + "/events?limit=1;after=0", "", 400},
		{"GET", "/v1/agents?include_settled=maybe", "", 400},
		{"GET", "/health", `{}`, 400},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) { f.request(tc.method, tc.path, tc.body, "", tc.status) })
	}
	if a, err := f.s.GetAgent(a.ID); err != nil || len(a.Queue) != 0 {
		t.Fatalf("invalid input mutated runtime: %+v %v", a, err)
	}
	// Unsupported media, duplicate idempotency headers, and oversized key.
	for _, variant := range []string{"media", "duplicate", "long-key", "empty-key"} {
		req, _ := http.NewRequest("POST", f.server.URL+base+"/messages", strings.NewReader(`{"text":"hello"}`))
		req.Header.Set("Content-Type", "application/json")
		want := 400
		switch variant {
		case "media":
			req.Header.Set("Content-Type", "text/plain")
			want = 415
		case "duplicate":
			req.Header.Add("Idempotency-Key", "one")
			req.Header.Add("Idempotency-Key", "two")
		case "long-key":
			req.Header.Set("Idempotency-Key", strings.Repeat("x", 257))
		case "empty-key":
			req.Header["Idempotency-Key"] = []string{""}
		}
		res, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s: %d %s", variant, res.StatusCode, data)
		}
	}
}

func TestHTTPQueueStopContinueAndIdempotency(t *testing.T) {
	started := make(chan struct{}, 8)
	p := &httpTestProvider{complete: func(r agent.CompletionRequest) (*agent.Response, error) {
		started <- struct{}{}
		<-r.RequestContext().Done()
		return nil, r.RequestContext().Err()
	}}
	f := newHTTPFixture(t, p)
	project := f.project()
	a := f.agent(project)
	base := "/v1/agents/" + a.ID
	first := decodeHTTP[QueuedMessage](t, f.request("POST", base+"/messages", `{"text":"first"}`, "one", 202))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn not started")
	}
	again := decodeHTTP[QueuedMessage](t, f.request("POST", base+"/messages", `{"text":"first"}`, "one", 202))
	if first.ID != again.ID {
		t.Fatal("idempotency did not deduplicate")
	}
	f.request("POST", base+"/messages", `{"text":"different"}`, "one", 409)
	second := decodeHTTP[QueuedMessage](t, f.request("POST", base+"/messages", `{"text":"second"}`, "two", 202))
	queue := decodeHTTP[[]QueuedMessage](t, f.request("GET", base+"/messages", "", "", 200))
	if len(queue) != 2 {
		t.Fatal(queue)
	}
	f.request("DELETE", base+"/messages/"+first.ID, "", "", 409)
	f.request("DELETE", base+"/messages/"+second.ID, "", "", 204)
	third := decodeHTTP[QueuedMessage](t, f.request("POST", base+"/messages", `{"text":"third"}`, "three", 202))
	f.request("POST", base+"/stop", fmt.Sprintf(`{"turn_id":%q}`, first.ID), "", 200)
	// User stop cancels this turn and automatically advances pending FIFO.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not advance pending turn")
	}
	f.request("POST", base+"/continue", "", "", 200)
	f.request("POST", base+"/stop", fmt.Sprintf(`{"turn_id":%q}`, first.ID), "", 200)
	latest, _ := f.s.GetAgent(a.ID)
	if latest.State != "running" {
		t.Fatalf("retried stop killed newer turn: %+v", latest)
	}
	f.request("POST", base+"/stop", fmt.Sprintf(`{"turn_id":%q}`, third.ID), "", 200)
}

func TestHTTPHistoryEventsPaginationAndCreateIdempotency(t *testing.T) {
	f := newHTTPFixture(t, nil)
	p := f.project()
	body := fmt.Sprintf(`{"project_id":%q,"prompt":"hello"}`, p.ID)
	a := decodeHTTP[Agent](t, f.request("POST", "/v1/agents", body, "create-key", 201))
	again := decodeHTTP[Agent](t, f.request("POST", "/v1/agents", body, "create-key", 201))
	if a.ID != again.ID {
		t.Fatal("create not idempotent")
	}
	f.request("POST", "/v1/agents", fmt.Sprintf(`{"project_id":%q,"prompt":"changed"}`, p.ID), "create-key", 409)
	a = waitHTTPAgent(t, f.s, a.ID, func(a Agent) bool { return len(a.Queue) > 0 && a.Queue[0].Status == "completed" })
	base := "/v1/agents/" + a.ID
	var cursor int64
	var messages []api.Message
	for {
		page := decodeHTTP[api.HistoryPage](t, f.request("GET", fmt.Sprintf("%s/history?after=%d&limit=1", base, cursor), "", "", 200))
		if len(page.Messages) == 0 {
			break
		}
		if page.NextCursor != cursor+1 {
			t.Fatal(page)
		}
		messages = append(messages, page.Messages...)
		cursor = page.NextCursor
	}
	if len(messages) != len(a.Messages) || len(messages) < 2 {
		t.Fatalf("history: %d vs %d", len(messages), len(a.Messages))
	}
	cursor = 0
	for {
		page := decodeHTTP[api.EventPage](t, f.request("GET", fmt.Sprintf("%s/events?after=%d&limit=1", base, cursor), "", "", 200))
		if len(page.Events) == 0 {
			break
		}
		if page.NextCursor != cursor+1 {
			t.Fatal(page)
		}
		single := decodeHTTP[api.Event](t, f.request("GET", fmt.Sprintf("%s/events/%d", base, page.NextCursor), "", "", 200))
		if single.Cursor != page.NextCursor {
			t.Fatal(single)
		}
		cursor = page.NextCursor
	}
	if uint64(cursor) != a.Cursor {
		t.Fatalf("lost events: %d vs %d", cursor, a.Cursor)
	}
	f.request("GET", fmt.Sprintf("%s/events/%d", base, cursor+1), "", "", 404)
}

func TestHTTPRuntimeErrorEnvelopeAndUnavailableService(t *testing.T) {
	f := newHTTPFixture(t, nil)
	p := f.project()
	a := f.agent(p)
	base := "/v1/agents/" + a.ID
	cases := []struct {
		method, path, body, code string
		status                   int
	}{
		{"POST", "/v1/projects", fmt.Sprintf(`{"name":"duplicate","root":%q,"defaults":{"model":"http-test/one","effort":"low"}}`, p.Root), "project_exists", 409},
		{"POST", "/v1/projects", `{"name":"bad","root":"relative","defaults":{"model":"http-test/one","effort":"low"}}`, "invalid_project", 400},
		{"PATCH", base + "/settings", `{"model":"missing/model"}`, "invalid_settings", 400},
		{"PATCH", base + "/settings", `{"effort":"impossible"}`, "invalid_settings", 400},
		{"POST", base + "/messages", `{"text":"   "}`, "invalid_message", 400},
		{"POST", base + "/stop", `{"turn_id":"missing"}`, "not_found", 404},
		{"GET", base + "/events?after=999", "", "cursor_invalid", 410},
	}
	for _, tc := range cases {
		got := decodeHTTP[api.Error](t, f.request(tc.method, tc.path, tc.body, "", tc.status))
		if string(got.Error.Code) != tc.code {
			t.Fatalf("%s: %+v", tc.path, got)
		}
	}
	if err := f.s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := decodeHTTP[api.Error](t, f.request("POST", base+"/messages", `{"text":"closed"}`, "", 503))
	if got.Error.Code != api.ShuttingDown {
		t.Fatal(got)
	}
	// Unexpected errors must not leak their internal diagnostic text.
	w := httptest.NewRecorder()
	writeRuntimeError(w, fmt.Errorf("secret storage details"))
	if w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Body.String())
	}
}

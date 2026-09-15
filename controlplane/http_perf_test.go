package controlplane

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

func benchmarkValidationHandler(b *testing.B) http.Handler {
	b.Helper()
	_, router, metadata, err := sharedHTTPDefinition()
	if err != nil {
		b.Fatal(err)
	}
	return validateHTTP(router, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), metadata)
}

func BenchmarkValidateHTTP(b *testing.B) {
	h := benchmarkValidationHandler(b)
	b.Run("no_body_or_query", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			r := httptest.NewRequest(http.MethodGet, "/health", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				b.Fatal(w.Code)
			}
		}
	})
	b.Run("query", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			r := httptest.NewRequest(http.MethodGet, "/v1/agents?include_settled=true&project_id=project&page=2&page_size=20", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				b.Fatal(w.Code)
			}
		}
	})
	b.Run("json_body", func(b *testing.B) {
		const body = `{"name":"project","root":"/tmp/project","defaults":{"model":"provider/model","effort":"low"}}`
		b.ReportAllocs()
		for b.Loop() {
			r := httptest.NewRequest(http.MethodPost, "/v1/projects", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				data, _ := io.ReadAll(w.Result().Body)
				b.Fatalf("%d: %s", w.Code, data)
			}
		}
	})
}

func BenchmarkNewHandler(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = NewHandler(&Service{})
	}
}

func BenchmarkSummaryWS(b *testing.B) {
	n := uint64(42)
	a := Agent{
		ParentAgentID: "parent", ID: "agent", ProjectID: "project", Title: "benchmark",
		Settings:       Settings{Model: "provider/model", Effort: "high"},
		ActiveSettings: &Settings{Model: "provider/model", Effort: "high"},
		State:          "running", Held: true, Cursor: n, LastResponseCursor: n - 1, ReadCursor: n - 2,
		Workspace:    Workspace{WorkspaceSelection: WorkspaceSelection{Mode: "worktree", BaseBranch: "main"}, Locked: true, Status: "ready", Path: "/tmp/worktree", Branch: "agent"},
		ContextUsage: agent.ContextUsage{Model: "provider/model", InputTokens: 100, OutputTokens: 50, EstimatedTokens: 200, ContextWindow: 10000, Known: true},
		CreatedAt:    time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := summaryWS(a); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidateWS(b *testing.B) {
	spec, _, _, err := sharedHTTPDefinition()
	if err != nil {
		b.Fatal(err)
	}
	h := &httpAPI{spec: spec}
	data := []byte(`{"type":"subscribe","request_id":"request","subscribe_all":true,"agent_ids":["one","two"],"cursors":{"one":42}}`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.validateWS(data); err != nil {
			b.Fatal(err)
		}
	}
}

func TestValidateHTTPPrecomputedMetadataPreservesContracts(t *testing.T) {
	spec, router, metadata, err := sharedHTTPDefinition()
	if err != nil {
		t.Fatal(err)
	}
	_ = spec
	called := false
	h := validateHTTP(router, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), metadata)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v1/agents/agent", nil))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, PATCH" || called {
		t.Fatalf("method contract: status=%d allow=%q called=%v", w.Code, w.Header().Get("Allow"), called)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/agents?include_settled=true&include_settled=false", nil))
	if w.Code != http.StatusBadRequest || called {
		t.Fatalf("repeated query contract: status=%d called=%v", w.Code, called)
	}
}

package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestAgentSummaryTitlesAndDetachedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, text, filename, title, want string
	}{
		{name: "empty"},
		{name: "prompt", text: " \tFix\n the\u00a0startup crash  ", want: "Fix the startup crash"},
		{name: "bounded", text: strings.Repeat("界", 10000), want: strings.Repeat("界", 80)},
		{name: "trimmed boundary", text: strings.Repeat("a", 79) + " next", want: strings.Repeat("a", 79)},
		{name: "image", filename: "screenshot.png", want: "screenshot.png"},
		{name: "bounded image", filename: strings.Repeat("a", 10000), want: strings.Repeat("a", 80)},
		{name: "explicit title", text: "first message", title: "Named chat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Agent{Title: tc.title, ActiveSettings: &Settings{Model: "original"}, Queue: []QueuedMessage{{Text: tc.text, Attachments: []Attachment{{Name: tc.filename}}}}, Messages: []agent.Message{{Role: "assistant", Content: agent.TextContent("history")}}}
			summary := cloneAgentSnapshot(a, true)
			if summary.DisplayTitle != tc.want || summary.Title != a.Title || summary.Queue != nil || summary.Messages != nil {
				t.Fatalf("unexpected summary: %+v", summary)
			}
			summary.ActiveSettings.Model = "changed"
			if a.ActiveSettings.Model != "original" || a.DisplayTitle != "" || len(a.Queue) != 1 || len(a.Messages) != 1 {
				t.Fatal("summary mutated retained agent")
			}
			wire := summaryWS(summary)
			if tc.want != "" && (wire.DisplayTitle == nil || *wire.DisplayTitle != tc.want) {
				t.Fatalf("inventory lost chat name: %+v", wire)
			}
		})
	}
}

func TestHTTPAgentSummariesOmitHistoryWithoutChangingLegacyReads(t *testing.T) {
	f := newHTTPFixture(t, nil)
	a := f.agent(f.project())
	retained := strings.Repeat("PRIVATE_RETAINED_HISTORY ", 50000)
	f.s.mu.Lock()
	record := f.s.state.Agents[a.ID]
	record.Agent.Queue = []QueuedMessage{{ID: "first", Text: "Investigate startup\n" + retained, Status: "completed", CreatedAt: a.CreatedAt}}
	record.Agent.Messages = []agent.Message{{Role: "assistant", Content: agent.TextContent(retained)}}
	f.s.mu.Unlock()
	for _, query := range []string{"summary=true", "summary=true&page=1&page_size=1", "summary=true&project_id=" + a.ProjectID} {
		raw := f.request("GET", "/v1/agents?"+query, "", "", 200)
		rows := decodeHTTP[[]map[string]json.RawMessage](t, raw)
		if len(rows) != 1 {
			t.Fatalf("%s: wrong agent count", query)
		}
		if _, exists := rows[0]["queue"]; exists {
			t.Fatal("summary includes queue")
		}
		if _, exists := rows[0]["messages"]; exists {
			t.Fatal("summary includes conversation")
		}
		if len(raw) > 4096 {
			t.Fatalf("summary retained large content: %d bytes", len(raw))
		}
		preview := decodeHTTP[string](t, rows[0]["display_title"])
		if !strings.HasPrefix(preview, "Investigate startup ") || len(preview) != 80 {
			t.Fatalf("missing bounded chat name: %q", preview)
		}
	}
	for _, query := range []string{"", "?summary=false"} {
		rows := decodeHTTP[[]Agent](t, f.request("GET", "/v1/agents"+query, "", "", 200))
		if len(rows) != 1 || len(rows[0].Queue) != 1 || len(rows[0].Messages) != 1 || rows[0].Messages[0].Content.Text() != retained || rows[0].DisplayTitle != "" {
			t.Fatal("legacy history changed")
		}
	}
	f.request("GET", "/v1/agents?summary=invalid", "", "", 400)

	c := dialHTTPWS(t, f.server)
	sendHTTPWS(t, c, map[string]any{"type": "subscribe", "request_id": "summary", "agent_ids": []string{a.ID}, "event_agent_ids": []string{}})
	readHTTPWS(t, c)
	frame := readHTTPWS(t, c)
	if frame.Type != "inventory" || frame.Agent.DisplayTitle == nil || !strings.HasPrefix(*frame.Agent.DisplayTitle, "Investigate startup ") {
		t.Fatalf("websocket omitted the background chat name: %+v", frame)
	}
}

func TestInMemoryAgentSummaryListingFiltersAndPages(t *testing.T) {
	s := &Service{state: emptyState()}
	for _, a := range []Agent{
		{ID: "a", ProjectID: "p"},
		{ID: "b", ProjectID: "p", Settled: true},
		{ID: "c", ProjectID: "q"},
	} {
		a.Queue = []QueuedMessage{{Text: "first message"}}
		a.Messages = []agent.Message{{Content: agent.TextContent("retained")}}
		s.state.Agents[a.ID] = &storedAgent{Agent: a}
	}
	for _, tc := range []struct {
		settled    bool
		project    string
		page, size int64
		ids        string
		total      int
	}{
		{false, "", 0, 0, "ac", 2},
		{true, "p", 0, 0, "ab", 2},
		{true, "p", 2, 1, "b", 2},
		{true, "p", 3, 1, "", 2},
	} {
		rows, total, err := s.listAgentSnapshots(tc.settled, tc.project, tc.page, tc.size, true)
		ids := ""
		for _, row := range rows {
			ids += row.ID
			if row.Queue != nil || row.Messages != nil || row.DisplayTitle != "first message" {
				t.Fatalf("history leaked into summary: %+v", row)
			}
		}
		if err != nil || ids != tc.ids || total != tc.total {
			t.Fatalf("%+v: ids=%q total=%d err=%v", tc, ids, total, err)
		}
	}
	// A summary must also serialize as metadata when the service has no SQLite index.
	w := httptest.NewRecorder()
	NewHandler(s).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/agents?summary=true", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), `"messages"`) || strings.Contains(w.Body.String(), `"queue"`) {
		t.Fatalf("in-memory HTTP summary: %d %s", w.Code, w.Body.String())
	}
}

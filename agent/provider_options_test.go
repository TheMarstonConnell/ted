package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCodexEffortPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"fake","refresh_token":"fake","account_id":"account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := NewCodexProvider(NewCodexAuthStore(path))
	for _, effort := range []Effort{EffortLow, EffortMedium, EffortHigh, ""} {
		p.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			var body responsesRequest
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			want := effort
			if want == "" {
				want = EffortMedium
			}
			if body.Model != "gpt-6-astra" || body.Reasoning.Effort != want {
				t.Fatalf("payload: %+v", body)
			}
			stream := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"gpt-6-astra\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream)), Header: make(http.Header)}, nil
		})}
		if _, err := p.Complete(zap.NewNop(), CompletionRequest{Model: "codex/gpt-6-astra", Effort: effort, Messages: []Message{{Role: "user", Content: TextContent("hello")}}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenRouterEffortPayload(t *testing.T) {
	p := NewOpenRouterProvider("fake")
	for _, effort := range []Effort{EffortLow, EffortMedium, EffortHigh, ""} {
		p.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			var body map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if string(body["model"]) != `"openai/gpt-5.6-luna"` {
				t.Fatal(string(body["model"]))
			}
			if effort == "" {
				if _, exists := body["reasoning"]; exists {
					t.Fatal("unset effort must be omitted")
				}
			} else {
				var reasoning ReasoningOptions
				if err := json.Unmarshal(body["reasoning"], &reasoning); err != nil {
					t.Fatal(err)
				}
				if reasoning.Effort != effort {
					t.Fatal(reasoning)
				}
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`)), Header: make(http.Header)}, nil
		})}
		if _, err := p.Complete(zap.NewNop(), CompletionRequest{Model: "openrouter/openai/gpt-5.6-luna", Effort: effort}); err != nil {
			t.Fatal(err)
		}
	}
}

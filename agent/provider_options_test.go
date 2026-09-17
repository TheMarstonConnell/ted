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

func TestOpenRouterPayloadRetainsUserImage(t *testing.T) {
	body, err := json.Marshal(CompletionBody{
		Model:    "vision-model",
		Messages: []Message{{Role: "user", Content: imageContent([]screenshotImage{{MIMEType: "image/png", Data: []byte("pixels")}})}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"type":"image_url"`) || !strings.Contains(string(body), `data:image/png;base64,`) {
		t.Fatalf("OpenRouter payload lost user image: %s", body)
	}
}

func TestSubmittedAttachmentProviderPayloads(t *testing.T) {
	imageURL := "data:image/png;base64,iVBORw0KGgo="
	content := attachmentContent("inspect screenshot", []Attachment{{Name: "screen.png", URL: imageURL}})
	messages := []Message{{Role: "user", Content: content}}
	openrouter := NewOpenRouterProvider("fake")
	openrouter.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Messages []struct {
				Content []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
			t.Fatalf("unexpected messages: %+v", body)
		}
		parts := body.Messages[0].Content
		if parts[0].Type != "text" || parts[0].Text != "inspect screenshot" || parts[1].Type != "image_url" || parts[1].ImageURL.URL != imageURL {
			t.Fatalf("lost multimodal parts: %+v", parts)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`)), Header: make(http.Header)}, nil
	})}
	if _, err := openrouter.Complete(zap.NewNop(), CompletionRequest{Model: "vision-model", Messages: messages}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"fake","refresh_token":"fake","account_id":"account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	codex := NewCodexProvider(NewCodexAuthStore(path))
	codex.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var body responsesRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var item struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		}
		if len(body.Input) != 1 {
			t.Fatalf("unexpected input count: %d", len(body.Input))
		}
		if err := json.Unmarshal(body.Input[0], &item); err != nil {
			t.Fatal(err)
		}
		if len(item.Content) != 2 || item.Content[0].Type != "input_text" || item.Content[0].Text != "inspect screenshot" || item.Content[1].Type != "input_image" || item.Content[1].ImageURL != imageURL {
			t.Fatalf("lost multimodal parts: %+v", item)
		}
		stream := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"gpt-6-astra\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream)), Header: make(http.Header)}, nil
	})}
	if _, err := codex.Complete(zap.NewNop(), CompletionRequest{Model: "codex/gpt-6-astra", Messages: messages}); err != nil {
		t.Fatal(err)
	}
}

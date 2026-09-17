package browser

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
)

func TestNetworkDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name  string
		event any
		want  map[string]any
	}{
		{"tls", &network.EventLoadingFailed{Type: network.ResourceTypeDocument, ErrorText: "net::ERR_CERT_AUTHORITY_INVALID"}, map[string]any{"text": "net::ERR_CERT_AUTHORITY_INVALID", "resource_type": network.ResourceTypeDocument}},
		{"dns", &network.EventLoadingFailed{ErrorText: "net::ERR_NAME_NOT_RESOLVED"}, map[string]any{"text": "net::ERR_NAME_NOT_RESOLVED"}},
		{"csp", &network.EventLoadingFailed{BlockedReason: network.BlockedReasonCsp}, map[string]any{"blocked_reason": network.BlockedReasonCsp}},
		{"cors", &network.EventLoadingFailed{CorsErrorStatus: &network.CorsErrorStatus{CorsError: network.CorsErrorMissingAllowOriginHeader, FailedParameter: "private-value"}}, map[string]any{"cors_error": network.CorsErrorMissingAllowOriginHeader}},
		{"canceled", &network.EventLoadingFailed{Canceled: true, ErrorText: "net::ERR_ABORTED"}, map[string]any{"canceled": true, "text": "net::ERR_ABORTED"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tab := &browserTab{}
			tab.handleNetworkDiagnostic(test.event)
			if len(tab.errors) != 1 {
				t.Fatalf("errors = %#v", tab.errors)
			}
			item := tab.errors[0].(map[string]any)
			if item["source"] != "network" || item["type"] != "loading_failed" {
				t.Fatalf("diagnostic = %#v", item)
			}
			for key, want := range test.want {
				if item[key] != want {
					t.Errorf("%s = %v, want %v", key, item[key], want)
				}
			}
			encoded, _ := json.Marshal(item)
			if strings.Contains(string(encoded), "private-value") {
				t.Fatal("diagnostics retained CORS parameter")
			}
		})
	}
}

func TestNetworkHTTPDiagnostics(t *testing.T) {
	for _, status := range []int64{399, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			tab := &browserTab{}
			tab.handleNetworkDiagnostic(&network.EventResponseReceived{Type: network.ResourceTypeDocument, Response: &network.Response{
				Status: status, URL: "https://private-value.example/?token=private-value",
				Headers: network.Headers{"Set-Cookie": "private-value"},
			}})
			if status < 400 {
				if len(tab.errors) != 0 {
					t.Fatalf("successful response generated diagnostics: %#v", tab.errors)
				}
				return
			}
			if len(tab.errors) != 1 {
				t.Fatalf("errors = %#v", tab.errors)
			}
			item := tab.errors[0].(map[string]any)
			if item["status"] != status || item["type"] != "http_error" {
				t.Fatalf("diagnostic = %#v", item)
			}
			encoded, _ := json.Marshal(item)
			if strings.Contains(string(encoded), "private-value") {
				t.Fatal("diagnostics retained URL or headers")
			}
		})
	}
}

func TestNetworkDiagnosticsBoundedAndClearable(t *testing.T) {
	tab := &browserTab{id: "tab"}
	s := &session{selected: tab.id, tabs: map[target.ID]*browserTab{tab.id: tab}}
	for i := 0; i < 550; i++ {
		tab.handleNetworkDiagnostic(&network.EventLoadingFailed{ErrorText: fmt.Sprint(i)})
	}
	data, err := s.readEvents(map[string]any{"clear": true}, true)
	if err != nil {
		t.Fatal(err)
	}
	items := data.(map[string]any)["errors"].([]any)
	if len(items) != 500 || items[0].(map[string]any)["text"] != "50" {
		t.Fatalf("failure buffer did not retain the most recent 500 events")
	}
	if len(tab.errors) != 0 || len(tab.console) != 0 {
		t.Fatal("clear failed or network failures leaked into console")
	}
}

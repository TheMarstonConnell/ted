package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/remote"
)

func TestNudgeCommand(t *testing.T) {
	for _, sender := range []string{"child-chat", ""} {
		t.Run("sender="+sender, func(t *testing.T) {
			t.Setenv("TED_THREAD_ID", sender)
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				if r.Method != "POST" || r.URL.Path != "/v1/agents/other-chat/messages" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Idempotency-Key") != "review-complete" {
					t.Errorf("headers: %v", r.Header)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["text"] != "All review comments are complete.\nTests pass." || body["kind"] != "bot" || body["sender_agent_id"] != sender {
					t.Errorf("body: %#v", body)
				}
				if _, present := body["sender_agent_id"]; sender == "" && present {
					t.Error("anonymous sender should be omitted")
				}
				w.WriteHeader(http.StatusAccepted)
				fmt.Fprint(w, `{"id":"receipt-123","kind":"bot","text":"All review comments are complete.\nTests pass.","status":"pending"}`)
			}))
			defer server.Close()
			cmd := newRootCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"nudge", "--server", server.URL, "--idempotency-key", "review-complete", "other-chat", "All review comments are complete.\nTests pass."})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if out.String() != "Nudge accepted: receipt-123\n" || posts != 1 {
				t.Fatalf("output %q, posts %d", out.String(), posts)
			}
		})
	}
}

func TestNudgeCommandRejectsInvalidArgumentsWithoutConnecting(t *testing.T) {
	for _, args := range [][]string{
		nil, {"chat"}, {"chat", "text", "extra"}, {"", "text"}, {"chat", " \n\t"},
		{"--idempotency-key", "", "chat", "text"},
	} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			cmd := newRootCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(append([]string{"nudge", "--server", "http://127.0.0.1:0"}, args...))
			if err := cmd.Execute(); err == nil || strings.Contains(err.Error(), "connect to server") {
				t.Fatalf("expected argument error before connecting, got %v", err)
			}
			if out.Len() != 0 {
				t.Fatal(out.String())
			}
		})
	}
}

func TestNudgeCommandDeliveryErrors(t *testing.T) {
	for _, tc := range []struct {
		code int
		name string
	}{
		{404, "not_found"}, {409, "settled"}, {409, "idempotency_conflict"}, {503, "shutting_down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					fmt.Fprint(w, `{"api_version":"1"}`)
					return
				}
				w.WriteHeader(tc.code)
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"delivery rejected"}}`, tc.name)
			}))
			defer server.Close()
			cmd := newRootCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"nudge", "--server", server.URL, "chat", "done"})
			err := cmd.Execute()
			var apiErr *remote.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != tc.name || apiErr.Status != tc.code {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.Len() != 0 {
				t.Fatal("failed delivery printed receipt", out.String())
			}
		})
	}
}

func TestNudgeCommandUnavailableServerAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		cmd := newRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"nudge", "--server", "http://127.0.0.1:0", "chat", "done"})
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cancel()
		}
		err := cmd.ExecuteContext(ctx)
		cancel()
		if err == nil || out.Len() != 0 {
			t.Fatalf("expected connection failure without receipt: %v, %q", err, out.String())
		}
	}
}

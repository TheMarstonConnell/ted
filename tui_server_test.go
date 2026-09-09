package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestExplicitServerNeverFallsBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TED_HOME", dir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	address := server.URL
	server.Close()
	base, owner, err := resolveTUIServer(context.Background(), address)
	if err == nil || owner != nil || base != "" || !strings.Contains(err.Error(), "--server") {
		t.Fatal(base, owner, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("explicit connection started local server", entries, err)
	}
}

func TestExplicitServerHasNoOwnership(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"api_version":"1"}`))
	}))
	defer server.Close()
	base, owner, err := resolveTUIServer(context.Background(), server.URL+"/")
	if err != nil || owner != nil || base != server.URL {
		t.Fatal(base, owner, err)
	}
	owner.Close()
	if err := checkServer(context.Background(), base); err != nil {
		t.Fatal("non-owner stopped server", err)
	}
}

func TestHealthVersionAndURLValidation(t *testing.T) {
	for _, body := range []string{`{"api_version":"2"}`, `{}`, `not json`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		if err := checkServer(context.Background(), server.URL); err == nil || serverUnavailable(err) {
			t.Errorf("accepted %s", body)
		}
		server.Close()
	}
	for _, url := range []string{"localhost:8281", "file:///tmp/socket", "http://", "http://localhost:8281?x=1"} {
		if err := checkServer(context.Background(), url); err == nil {
			t.Errorf("accepted %s", url)
		}
	}
	cmd := newTUICommand()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"--server", ""})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires a URL") {
		t.Fatal(err)
	}
}

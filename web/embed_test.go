package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-API", "yes")
		http.Error(w, "api", http.StatusTeapot)
	})
	h := Handler(api)
	for _, url := range []string{"/", "/agents/a?panel=settings", "/projects/p?panel=settings"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", url, nil))
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), `id="root"`) || rr.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: %d %s", url, rr.Code, rr.Body.String())
		}
	}
	for _, url := range []string{"/health", "/v1/ws", "/v1/agents", "/v1/missing"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", url, nil))
		if rr.Code != http.StatusTeapot || rr.Header().Get("X-API") != "yes" {
			t.Fatalf("API swallowed: %s", url)
		}
	}
	for _, url := range []string{"/missing.js", "/assets/missing.js", "/src/App.tsx", "/.env"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", url, nil))
		if rr.Code != 404 {
			t.Fatalf("%s should 404, got %d", url, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/", nil))
	if rr.Code != 405 {
		t.Fatal("POST accepted")
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("HEAD", "/agents/a", nil))
	if rr.Code != 200 || rr.Body.Len() != 0 {
		t.Fatal("HEAD must not return a body")
	}
	entries, err := fs.ReadDir(assets, "dist/assets")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", "/assets/"+entry.Name(), nil))
		if rr.Code != 200 || rr.Body.Len() == 0 || !strings.Contains(rr.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("asset not served: %s", entry.Name())
		}
	}
}

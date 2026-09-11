// Package web serves the embedded Vite control plane alongside the API.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The bundle is not checked in. Build it before go build, go test, or go install:
// cd web && npm ci && npm run build
//
//go:embed dist
var assets embed.FS

// Handler keeps API routes under the API's validation/error handling and serves
// the SPA for browser navigation, including direct agent and project links.
func Handler(api http.Handler) http.Handler {
	root, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
			api.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
			return
		}
		if name != "." && name != "" && name != "index.html" && !strings.HasPrefix(name, "agents/") && !strings.HasPrefix(name, "projects/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(w, "web bundle unavailable", http.StatusInternalServerError)
			return
		}
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	})
}

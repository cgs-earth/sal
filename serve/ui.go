package serve

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
)

// uiAssets holds the compiled `serve/sal-ui` React app. It is built by `make ui`
// and committed so that `go install` produces a binary with the UI baked in.
//
//go:embed all:sal-ui/dist
var uiAssets embed.FS

// uiHandler serves the embedded UI, falling back to index.html so that any path
// the server does not own is handled by the app itself.
func uiHandler() (http.Handler, error) {
	dist, err := fs.Sub(uiAssets, "sal-ui/dist")
	if err != nil {
		return nil, fmt.Errorf("read embedded UI assets: %w", err)
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, fmt.Errorf("embedded UI is missing index.html; run `make ui` to build it: %w", err)
	}
	files := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "the SAL UI only supports GET requests", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			serveIndex(w, index)
			return
		}
		if _, err := fs.Stat(dist, name); err != nil {
			serveIndex(w, index)
			return
		}
		// Vite fingerprints everything under assets/, so those are safe to cache forever.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	}), nil
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(index); err != nil {
		slog.Error("failed to write the SAL UI index", "error", err)
	}
}

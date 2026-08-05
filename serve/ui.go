package serve

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
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
	compressed, err := compressUIAssets(dist)
	if err != nil {
		return nil, err
	}

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
		if body, ok := compressed[name]; ok {
			// A cache in front of the server must not hand this body to a client
			// that did not ask for gzip, so vary either way.
			w.Header().Add("Vary", "Accept-Encoding")
			if acceptsGzip(r.Header.Get("Accept-Encoding")) {
				writeCompressedAsset(w, name, body)
				return
			}
		}
		files.ServeHTTP(w, r)
	}), nil
}

// minCompressibleSize is the size below which gzip's framing costs more than it saves.
const minCompressibleSize = 1024

// compressUIAssets gzips the embedded UI once, at startup. The bundle is large --
// the initial JS chunk alone is over 600 KB -- and `serve` hands it to every
// browser with a cold cache before the page can ask for anything else. The assets
// never change while the process runs, so compressing per request would repeat
// identical work on the same CPU the queries need.
func compressUIAssets(dist fs.FS) (map[string][]byte, error) {
	compressed := map[string][]byte{}
	err := fs.WalkDir(dist, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !isCompressibleAsset(name) {
			return err
		}
		content, err := fs.ReadFile(dist, name)
		if err != nil {
			return err
		}
		if len(content) < minCompressibleSize {
			return nil
		}
		var buf bytes.Buffer
		writer, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return err
		}
		if _, err := writer.Write(content); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		// Anything that fails to shrink is left to the file server rather than
		// held in memory twice for no gain.
		if buf.Len() >= len(content) {
			return nil
		}
		compressed[name] = buf.Bytes()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("compress the embedded UI assets: %w", err)
	}
	return compressed, nil
}

func isCompressibleAsset(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".css", ".html", ".svg", ".json", ".map":
		return true
	default:
		return false
	}
}

// writeCompressedAsset serves a precompressed asset. It bypasses http.FileServer,
// so the headers that would have set have to be set here.
func writeCompressedAsset(w http.ResponseWriter, name string, body []byte) {
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if _, err := w.Write(body); err != nil {
		slog.Error("failed to write a compressed UI asset", "asset", name, "error", err)
	}
}

// acceptsGzip reports whether a client accepts gzip, honoring an explicit refusal
// of it via q=0.
func acceptsGzip(acceptEncoding string) bool {
	for _, encoding := range strings.Split(acceptEncoding, ",") {
		fields := strings.Split(strings.TrimSpace(encoding), ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		for _, parameter := range fields[1:] {
			if strings.EqualFold(strings.TrimSpace(parameter), "q=0") {
				return false
			}
		}
		return true
	}
	return false
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(index); err != nil {
		slog.Error("failed to write the SAL UI index", "error", err)
	}
}

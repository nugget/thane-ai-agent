// Package web serves the embedded Cognition Engine dashboard — a single-page
// app — as static files at the root of the Thane HTTP server; this package is
// static-file serving only.
//
// The dashboard (graph, process table, and forensics views) gets all its JSON
// and SSE data from the native API under /v1; this package only serves the
// static assets.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
)

//go:embed static/*
var staticFS embed.FS

// allowedExtensions is the set of static file extensions served to
// clients. Requests for other extensions are rejected with 404.
var allowedExtensions = map[string]bool{
	".html": true,
	".css":  true,
	".js":   true,
	".svg":  true,
	".json": true,
}

// init verifies allowedExtensions are normalized at startup.
func init() {
	for ext := range allowedExtensions {
		if ext == "" || ext[0] != '.' || ext != strings.ToLower(ext) {
			panic(fmt.Sprintf("web: invalid extension in allowedExtensions: %q", ext))
		}
	}
}

// etags maps each embedded static file's path (relative to the static/ root,
// e.g. "app.js" or "views/forensics.js") to a strong content ETag. Computed
// once at startup so request handling never re-hashes the asset.
var etags = buildETags()

func buildETags() map[string]string {
	m := make(map[string]string)
	walkErr := fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, readErr := staticFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		m[strings.TrimPrefix(path, "static/")] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	if walkErr != nil {
		panic(fmt.Sprintf("web: failed to compute static asset etags: %v", walkErr))
	}
	return m
}

// Config holds dependencies for the web server.
type Config struct {
	// Logger for web server operations. Defaults to slog.Default().
	Logger *slog.Logger
}

// WebServer serves the Cognition Engine dashboard's static assets.
type WebServer struct {
	logger *slog.Logger
}

// NewWebServer creates a static-file web server with the given configuration.
func NewWebServer(cfg Config) *WebServer {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &WebServer{logger: logger}
}

// RegisterRoutes registers the dashboard's static routes on the given mux.
// This satisfies the [api.WebServerRegistrar] interface.
func (s *WebServer) RegisterRoutes(mux *http.ServeMux) {
	// Exact-root match only ("/{$}"), so retired /api/* URLs and other
	// unknown paths get a 404 instead of a 200 dashboard shell.
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /static/{file...}", s.handleStatic)
}

// handleIndex serves the single-page visualizer.
func (s *WebServer) handleIndex(w http.ResponseWriter, _ *http.Request) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(data); err != nil {
		s.logger.Debug("failed to write index response", "error", err)
	}
}

// contentTypes maps file extensions to MIME types for static files.
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "application/javascript; charset=utf-8",
	".svg":  "image/svg+xml",
	".json": "application/json",
}

// handleStatic serves embedded static files with extension filtering.
func (s *WebServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if file == "" {
		http.NotFound(w, r)
		return
	}

	// Prevent path traversal. Use path (not path/filepath): both the request
	// URL and the embed.FS keys are always slash-separated, so OS-specific
	// separators must not leak in — otherwise nested modules (views/*, data/*)
	// would miss the embed read and the etags lookup on Windows.
	clean := path.Clean(file)
	if strings.Contains(clean, "..") {
		http.NotFound(w, r)
		return
	}

	ext := strings.ToLower(path.Ext(clean))
	if !allowedExtensions[ext] {
		http.NotFound(w, r)
		return
	}

	data, err := staticFS.ReadFile("static/" + clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if ct, ok := contentTypes[ext]; ok {
		w.Header().Set("Content-Type", ct)
	}

	// Embedded assets are hashless and immutable for the binary's lifetime,
	// changing only on redeploy. Cache-Control: no-cache means "store but
	// always revalidate"; the content ETag then lets the browser get a 304
	// when nothing changed, so it never serves stale code.
	if etag := etags[clean]; etag != "" {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	if _, err = w.Write(data); err != nil {
		s.logger.Debug("failed to write static response", "error", err)
	}
}

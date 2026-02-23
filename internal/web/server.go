// Package web provides the web dashboard and chat interface for Thane.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/router"
)

//go:embed static/*
var staticFiles embed.FS

// StatsSnapshot holds runtime info for the dashboard. Currently only
// build metadata is needed; per-conversation stats were removed because
// they are misleading in Thane's multi-conversation architecture.
type StatsSnapshot struct {
	Build map[string]string `json:"build,omitempty"`
}

// RouterInfo combines routing stats and the model roster.
type RouterInfo struct {
	Stats  router.Stats   `json:"stats"`
	Models []router.Model `json:"models"`
}

// HealthStatus describes the health of a single dependency.
type HealthStatus struct {
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since,omitempty"`
	LastError string    `json:"last_error,omitempty"`
}

// StatsFunc returns a snapshot of session statistics.
type StatsFunc func() StatsSnapshot

// RouterFunc returns combined router statistics and model list.
type RouterFunc func() RouterInfo

// HealthFunc returns dependency health keyed by service name.
type HealthFunc func() map[string]HealthStatus

// Config holds the dependencies needed to construct a WebServer.
type Config struct {
	BrandName  string // Display name in the nav bar. Defaults to "Thane".
	StatsFunc  StatsFunc
	RouterFunc RouterFunc
	HealthFunc HealthFunc
	Logger     *slog.Logger
}

// WebServer serves the web dashboard and chat UI.
type WebServer struct {
	brandName  string
	statsFunc  StatsFunc
	routerFunc RouterFunc
	healthFunc HealthFunc
	templates  map[string]*template.Template
	logger     *slog.Logger
}

// NewWebServer creates a WebServer with parsed templates. It panics if
// templates contain syntax errors, providing fail-fast behavior at startup.
func NewWebServer(cfg Config) *WebServer {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	brandName := cfg.BrandName
	if brandName == "" {
		brandName = "Thane"
	}
	s := &WebServer{
		brandName:  brandName,
		statsFunc:  cfg.StatsFunc,
		routerFunc: cfg.RouterFunc,
		healthFunc: cfg.HealthFunc,
		logger:     logger,
	}
	s.templates = loadTemplates()
	return s
}

// RegisterRoutes adds dashboard, chat UI, and static file routes to the mux.
func (s *WebServer) RegisterRoutes(mux *http.ServeMux) {
	// Dashboard at root
	mux.HandleFunc("GET /", s.handleDashboard)

	// Static assets (htmx, CSS)
	mux.HandleFunc("GET /static/", s.handleStatic)

	// Chat UI (rendered through the shared layout template)
	mux.HandleFunc("GET /chat", s.handleChat)
	mux.HandleFunc("GET /chat/{path...}", func(w http.ResponseWriter, r *http.Request) {
		p := r.PathValue("path")
		if p == "" {
			s.handleChat(w, r)
			return
		}
		// Serve sub-path assets from the old static dir (future-proofing)
		serveFile(w, r, p)
	})

	// PWA manifest
	mux.HandleFunc("GET /manifest.json", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, "manifest.json")
	})
}

// RegisterRoutes adds the chat UI routes to a mux. This is the legacy
// package-level function kept for backward compatibility when no
// WebServer is wired in.
func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /chat", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, "index.html")
	})
	mux.HandleFunc("GET /chat/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		if path == "" {
			serveFile(w, r, "index.html")
			return
		}
		serveFile(w, r, path)
	})
	mux.HandleFunc("GET /manifest.json", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, "manifest.json")
	})
}

// dashboardStaticExts lists file extensions allowed through /static/.
// This prevents the chat UI's index.html and manifest.json from being
// served at /static/index.html, keeping them exclusively at /chat.
var dashboardStaticExts = map[string]bool{
	".css":   true,
	".js":    true,
	".svg":   true,
	".png":   true,
	".ico":   true,
	".woff2": true,
}

// handleStatic serves dashboard assets (CSS, JS, images, fonts) from
// the embedded static directory. Files that are not dashboard assets
// (e.g., index.html, manifest.json) are rejected to avoid exposing the
// chat UI at an unintended path.
func (s *WebServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	// Only allow known dashboard asset extensions.
	ext := strings.ToLower(path.Ext(r.URL.Path))
	if !dashboardStaticExts[ext] {
		http.NotFound(w, r)
		return
	}

	subFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.StripPrefix("/static/", http.FileServer(http.FS(subFS))).ServeHTTP(w, r)
}

// getStaticFS returns the embedded static filesystem.
func getStaticFS() fs.FS {
	subFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return subFS
}

// serveFile serves a specific file from the embedded filesystem.
func serveFile(w http.ResponseWriter, r *http.Request, name string) {
	staticFS := getStaticFS()
	content, err := fs.ReadFile(staticFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch {
	case len(name) > 5 && name[len(name)-5:] == ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case len(name) > 5 && name[len(name)-5:] == ".json":
		w.Header().Set("Content-Type", "application/json")
	case len(name) > 4 && name[len(name)-4:] == ".css":
		w.Header().Set("Content-Type", "text/css")
	case len(name) > 3 && name[len(name)-3:] == ".js":
		w.Header().Set("Content-Type", "application/javascript")
	}

	_, _ = w.Write(content)
}

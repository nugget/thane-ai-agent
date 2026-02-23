// Package web provides the web dashboard and chat interface for Thane.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/router"
)

//go:embed static/*
var staticFiles embed.FS

// StatsSnapshot mirrors api.SessionStatsSnapshot without importing api.
type StatsSnapshot struct {
	TotalInputTokens  int64             `json:"total_input_tokens"`
	TotalOutputTokens int64             `json:"total_output_tokens"`
	TotalRequests     int64             `json:"total_requests"`
	EstimatedCostUSD  float64           `json:"estimated_cost_usd"`
	ReportedBalance   float64           `json:"reported_balance_usd,omitempty"`
	BalanceSetAt      string            `json:"balance_set_at,omitempty"`
	ContextTokens     int               `json:"context_tokens"`
	ContextWindow     int               `json:"context_window"`
	MessageCount      int               `json:"message_count"`
	Build             map[string]string `json:"build,omitempty"`
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
	StatsFunc  StatsFunc
	RouterFunc RouterFunc
	HealthFunc HealthFunc
	Logger     *slog.Logger
}

// WebServer serves the web dashboard and chat UI.
type WebServer struct {
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
	s := &WebServer{
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

	// Chat UI
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

// handleStatic serves files from the embedded static directory.
func (s *WebServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	// Strip "/static/" prefix and serve from embedded FS
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

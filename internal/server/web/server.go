// Package web implements the Cognition Engine dashboard served at the
// root of the Thane HTTP server. It provides a single-page interface
// backed by REST endpoints for loop definitions, system health, the
// capability catalog, request detail, and log drill-down via the SQLite
// log index. Running-loop state and its live event stream are served by
// the native API's /v1/loops* surface, not here.
package web

import (
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
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
	".png":  true,
	".ico":  true,
	".json": true,
}

// LogQuerier queries the structured log index. Implementations wrap
// [logging.Query] to decouple the web package from database/sql.
type LogQuerier interface {
	Query(params logging.QueryParams) ([]logging.LogEntry, error)
}

// SystemStatusProvider exposes runtime health and metadata for the
// system node on the dashboard canvas. Nil disables the system node.
type SystemStatusProvider interface {
	// Health returns the current health state of all watched services.
	Health() map[string]ServiceHealth
	// Uptime returns how long the process has been running.
	Uptime() time.Duration
	// Version returns build and runtime metadata.
	Version() map[string]string
	// ModelRegistry returns the current model-registry snapshot.
	ModelRegistry() *fleet.RegistrySnapshot
	// RouterStats returns the current router statistics snapshot.
	RouterStats() *router.Stats
	// AnthropicRateLimitSnapshot returns the latest Anthropic
	// rate-limit snapshot, or nil when Anthropic has not been observed.
	AnthropicRateLimitSnapshot() *fleet.AnthropicRateLimitSnapshot
	// CapabilityCatalog returns the resolved runtime capability catalog
	// rendered with the supplied options.
	CapabilityCatalog(opts toolcatalog.CatalogViewOptions) *toolcatalog.CapabilityCatalogView
	// CapabilityEntry returns the resolved view of a single capability
	// tag, or nil when the tag is unknown.
	CapabilityEntry(tag string, opts toolcatalog.CatalogViewOptions) *toolcatalog.CapabilityCatalogEntry
}

// ServiceHealth describes the health of a single watched service.
type ServiceHealth struct {
	// Name is the human-readable service name.
	Name string `json:"name"`
	// Ready indicates whether the service is currently healthy.
	Ready bool `json:"ready"`
	// LastCheck is the RFC3339 timestamp of the last health probe.
	LastCheck string `json:"last_check,omitempty"`
	// LastError is the error from the most recent failed probe.
	LastError string `json:"last_error,omitempty"`
}

// Config holds dependencies for the web server.
type Config struct {
	// LogQuerier enables log drill-down. Nil disables the feature.
	LogQuerier LogQuerier
	// SystemStatus provides runtime health for the system canvas node.
	// Nil disables the system node.
	SystemStatus SystemStatusProvider
	// Logger for web server operations. Defaults to slog.Default().
	Logger *slog.Logger
}

// WebServer serves the Cognition Engine dashboard and its API endpoints.
type WebServer struct {
	logQuerier   LogQuerier
	systemStatus SystemStatusProvider
	logger       *slog.Logger
}

// NewWebServer creates a web server with the given configuration.
func NewWebServer(cfg Config) *WebServer {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &WebServer{
		logQuerier:   cfg.LogQuerier,
		systemStatus: cfg.SystemStatus,
		logger:       logger,
	}
}

// RegisterRoutes registers the visualizer routes on the given mux.
// This satisfies the [api.WebServerRegistrar] interface.
func (s *WebServer) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /static/{file...}", s.handleStatic)
	mux.HandleFunc("GET /api/system", s.handleSystem)
	mux.HandleFunc("GET /api/system/logs", s.handleSystemLogs)
}

// writeJSON encodes v as JSON to w, logging any errors.
func (s *WebServer) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Debug("failed to write JSON response", "error", err)
	}
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
	".png":  "image/png",
	".ico":  "image/x-icon",
	".json": "application/json",
}

// handleStatic serves embedded static files with extension filtering.
func (s *WebServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if file == "" {
		http.NotFound(w, r)
		return
	}

	// Prevent path traversal.
	clean := filepath.Clean(file)
	if strings.Contains(clean, "..") {
		http.NotFound(w, r)
		return
	}

	ext := strings.ToLower(filepath.Ext(clean))
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
	if _, err = w.Write(data); err != nil {
		s.logger.Debug("failed to write static response", "error", err)
	}
}

package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

// handleSystem returns runtime health, uptime, and version info for
// the system node on the dashboard canvas.
func (s *WebServer) handleSystem(w http.ResponseWriter, r *http.Request) {
	if s.systemStatus == nil {
		http.NotFound(w, r)
		return
	}
	health := s.systemStatus.Health()
	allReady := true
	for _, h := range health {
		if !h.Ready {
			allReady = false
			break
		}
	}
	status := "healthy"
	if !allReady {
		status = "degraded"
	}
	body := map[string]any{
		"status":  status,
		"health":  health,
		"uptime":  s.systemStatus.Uptime().Truncate(time.Second).String(),
		"version": s.systemStatus.Version(),
	}
	if snapshot := s.systemStatus.ModelRegistry(); snapshot != nil {
		body["model_registry"] = snapshot
	}
	if stats := s.systemStatus.RouterStats(); stats != nil {
		body["router_stats"] = stats
	}
	if snapshot := s.systemStatus.AnthropicRateLimitSnapshot(); snapshot != nil {
		body["anthropic_rate_limit"] = snapshot
	}
	if catalog := s.systemStatus.CapabilityCatalog(toolcatalog.CatalogViewOptions{IncludeDelegate: true}); catalog != nil {
		body["capability_catalog"] = catalog
	}
	s.writeJSON(w, body)
}

// handleSystemLogs returns all log entries across the entire runtime.
// When the runtime node is selected this acts as a full log tail,
// replacing the need for a separate terminal window.
func (s *WebServer) handleSystemLogs(w http.ResponseWriter, r *http.Request) {
	if s.logQuerier == nil {
		http.Error(w, `{"error":"log index not available"}`, http.StatusServiceUnavailable)
		return
	}

	// Parse optional query parameters.
	level := r.URL.Query().Get("level")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	entries, err := s.logQuerier.Query(logging.QueryParams{
		Level: level,
		Limit: limit,
		ExcludeSourcePrefixes: []string{
			"internal/server/", // Exclude API/web handler logs to avoid feedback loop
		},
	})
	if err != nil {
		s.logger.Warn("system log query failed", "error", err)
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, map[string]any{
		"entries": entries,
		"count":   len(entries),
	})
}

// handleStaticFile is used by handleStatic to strip the /static/ prefix.
// The path rewriting ensures the embedded FS serves from the right root.
func init() {
	// Verify allowedExtensions are normalized at init time.
	for ext := range allowedExtensions {
		if ext == "" || ext[0] != '.' || ext != strings.ToLower(ext) {
			panic(fmt.Sprintf("web: invalid extension in allowedExtensions: %q", ext))
		}
	}
}

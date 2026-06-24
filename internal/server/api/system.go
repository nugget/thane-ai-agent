package api

import (
	"net/http"
	"sort"

	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

// handleSystem returns a slim system-status rollup: overall status, dependency
// health, uptime, and version. Heavier snapshots (model registry, router
// stats, capabilities) have dedicated endpoints under /v1/models and
// /v1/insights. [GET /v1/system]
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	health := map[string]DependencyStatus{}
	if s.healthDeps != nil {
		health = s.healthDeps()
	}
	status := "healthy"
	for _, d := range health {
		if !d.Ready {
			status = "degraded"
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"status":         status,
		"health":         health,
		"uptime_seconds": int(buildinfo.Uptime().Seconds()),
		"version":        buildinfo.RuntimeInfo(),
	}, s.logger)
}

// handleSystemLogs tails the structured process log as a bare array, newest
// first, excluding the API/web handler logs to avoid a feedback loop. Honors
// ?level and ?limit (default 50, max 200). [GET /v1/system/logs]
func (s *Server) handleSystemLogs(w http.ResponseWriter, r *http.Request) {
	if s.logQuerier == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "log index not available")
		return
	}
	entries, err := s.logQuerier.Query(logging.QueryParams{
		Level: r.URL.Query().Get("level"),
		Limit: parseLogLimit(r.URL.Query().Get("limit")),
		ExcludeSourcePrefixes: []string{
			"internal/server/", // exclude API/web handler logs (feedback loop)
		},
	})
	if err != nil {
		s.logger.Warn("system log query failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "query failed")
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	if entries == nil {
		entries = []logging.LogEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, entries, s.logger)
}

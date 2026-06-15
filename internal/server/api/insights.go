package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// handleRouterInsights returns router statistics and the recent routing-audit
// trail in one object, consolidating the former /v1/router/stats and
// /v1/router/audit. The audit honors ?limit (default 20).
// [GET /v1/insights/router]
func (s *Server) handleRouterInsights(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "router not configured")
		return
	}
	stats := routerStatsResponse{Stats: s.router.GetStats()}
	if s.anthropicRateLimitSnapshot != nil {
		stats.AnthropicRateLimit = s.anthropicRateLimitSnapshot()
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	decisions := s.router.GetAuditLog(limit)
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"stats": stats,
		"audit": map[string]any{"count": len(decisions), "decisions": decisions},
	}, s.logger)
}

// handleToolInsights returns tool-call statistics plus the recent tool calls
// in one object, consolidating the former /v1/tools/stats and /v1/tools/calls.
// Recent calls honor ?tool, ?conversation_id, and ?limit (default 50).
// [GET /v1/insights/tools]
func (s *Server) handleToolInsights(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "memory store not configured")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	var calls []memory.ToolCall
	switch {
	case r.URL.Query().Get("tool") != "":
		calls = s.memoryStore.GetToolCallsByName(r.URL.Query().Get("tool"), limit)
	case r.URL.Query().Get("conversation_id") != "":
		calls = s.memoryStore.GetToolCalls(r.URL.Query().Get("conversation_id"), limit)
	default:
		calls = s.memoryStore.GetToolCalls("", limit)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"stats": s.memoryStore.ToolCallStats(),
		"calls": map[string]any{"tool_calls": calls, "count": len(calls)},
	}, s.logger)
}

// UseCapabilitySurface wires the capability-surface getter that backs the
// /v1/insights/capabilities endpoints.
func (s *Server) UseCapabilitySurface(getter func() []toolcatalog.CapabilitySurface) {
	s.capSurface = getter
}

// handleCapabilities returns the resolved capability-tag catalog. By default it
// includes only active tools per tag; ?include=excluded also surfaces
// operator-disabled tools. [GET /v1/insights/capabilities]
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.capSurface == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "capability catalog unavailable")
		return
	}
	surface := s.capSurface()
	if len(surface) == 0 {
		s.errorResponse(w, http.StatusServiceUnavailable, "capability catalog unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, toolcatalog.BuildCapabilityCatalogView(surface, parseCapabilityViewOptions(r)), s.logger)
}

// handleCapability returns the resolved view of one capability tag, 404ing when
// the tag is absent from the current surface. Honors the same ?include=excluded
// as the catalog. [GET /v1/insights/capabilities/{tag}]
func (s *Server) handleCapability(w http.ResponseWriter, r *http.Request) {
	if s.capSurface == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "capability catalog unavailable")
		return
	}
	tag := strings.TrimSpace(r.PathValue("tag"))
	if tag == "" {
		s.errorResponse(w, http.StatusBadRequest, "tag is required")
		return
	}
	opts := parseCapabilityViewOptions(r)
	for _, entry := range s.capSurface() {
		if entry.Tag == tag {
			rendered := toolcatalog.RenderCapabilityCatalogEntry(entry, opts)
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, rendered, s.logger)
			return
		}
	}
	s.errorResponse(w, http.StatusNotFound, "unknown capability tag")
}

// parseCapabilityViewOptions interprets the comma-separated ?include= query
// parameter. Only "excluded" is recognized today; unknown values are ignored.
func parseCapabilityViewOptions(r *http.Request) toolcatalog.CatalogViewOptions {
	opts := toolcatalog.CatalogViewOptions{IncludeDelegate: true}
	for _, token := range strings.Split(strings.ToLower(r.URL.Query().Get("include")), ",") {
		if strings.TrimSpace(token) == "excluded" {
			opts.IncludeExcluded = true
		}
	}
	return opts
}

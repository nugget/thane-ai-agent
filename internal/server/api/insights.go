package api

import (
	"net/http"
	"strconv"

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

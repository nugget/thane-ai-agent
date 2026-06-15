package api

import (
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

// RequestReader fetches live or retained per-request content — system prompt,
// messages, tool calls, and token metadata — for the /v1/requests endpoints.
// It is the read seam the API needs from the content/log stores, satisfied by
// the live request store (with a retained-content DB fallback).
type RequestReader interface {
	QueryRequestDetail(requestID string) (*logging.RequestDetail, error)
}

// UseRequestReader wires the request-content source that backs /v1/requests.
func (s *Server) UseRequestReader(r RequestReader) { s.requestReader = r }

// handleRequest returns the full retained detail for one model turn: system
// prompt, messages, tool calls, and token metadata. [GET /v1/requests/{id}]
func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.lookupRequest(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, detail, s.logger)
}

// handleRequestTools returns the tool calls made during one request as a bare
// JSON array, in invocation order. [GET /v1/requests/{id}/tools]
func (s *Server) handleRequestTools(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.lookupRequest(w, r)
	if !ok {
		return
	}
	// Always encode an array (never null) so the response shape is stable.
	tools := detail.ToolCalls
	if tools == nil {
		tools = []logging.ToolDetail{}
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, tools, s.logger)
}

// handleRequestRouting returns the router's decision trace for one request —
// why this model was chosen. Routing is a facet of the request, not a
// standalone subsystem; this replaces /v1/router/explain.
// [GET /v1/requests/{id}/routing]
func (s *Server) handleRequestRouting(w http.ResponseWriter, r *http.Request) {
	if s.router == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "router not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "id is required")
		return
	}
	decision := s.router.Explain(id)
	if decision == nil {
		s.errorResponse(w, http.StatusNotFound, "routing decision not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, decision, s.logger)
}

// lookupRequest resolves the {id} path value to a RequestDetail, writing the
// appropriate error response and returning ok=false when it cannot.
func (s *Server) lookupRequest(w http.ResponseWriter, r *http.Request) (*logging.RequestDetail, bool) {
	if s.requestReader == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "request detail not available")
		return nil, false
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "id is required")
		return nil, false
	}
	detail, err := s.requestReader.QueryRequestDetail(id)
	if err != nil {
		s.logger.Warn("request detail query failed", "request_id", id, "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "query failed")
		return nil, false
	}
	if detail == nil {
		s.errorResponse(w, http.StatusNotFound, "request not found")
		return nil, false
	}
	return detail, true
}

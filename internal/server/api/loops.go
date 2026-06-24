package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// LoopStatusReader exposes read access to the running-loop registry for the
// /v1/loops endpoints. It is the subset of *loop.Registry the API needs and
// is satisfied by that registry directly. Consolidates the running-loop
// surface that previously lived only in the web dashboard's /api/loops.
//
// StatusByID returns a Status value (not the live *loop.Loop) so the handlers
// depend only on the snapshot they render — and can be exercised with a fake.
type LoopStatusReader interface {
	Statuses() []looppkg.Status
	StatusByID(id string) (looppkg.Status, bool)
}

// LogQuerier queries the structured log index for loop-scoped log retrieval.
type LogQuerier interface {
	Query(params logging.QueryParams) ([]logging.LogEntry, error)
}

// UseLoopRegistry wires the running-loop registry that backs /v1/loops.
func (s *Server) UseLoopRegistry(r LoopStatusReader) { s.loopRegistry = r }

// UseLogQuerier wires the structured log index used by /v1/loops/{id}/logs.
func (s *Server) UseLogQuerier(q LogQuerier) { s.logQuerier = q }

// handleLoops returns running-loop status snapshots. An optional ?state=
// filter narrows by lifecycle state. [GET /v1/loops]
func (s *Server) handleLoops(w http.ResponseWriter, r *http.Request) {
	if s.loopRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop registry not configured")
		return
	}
	statuses := s.loopRegistry.Statuses()
	if state := strings.TrimSpace(r.URL.Query().Get("state")); state != "" {
		filtered := make([]looppkg.Status, 0, len(statuses))
		for _, st := range statuses {
			if string(st.State) == state {
				filtered = append(filtered, st)
			}
		}
		statuses = filtered
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, statuses, s.logger)
}

// handleLoop returns one running loop's status snapshot. [GET /v1/loops/{id}]
func (s *Server) handleLoop(w http.ResponseWriter, r *http.Request) {
	if s.loopRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop registry not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "id is required")
		return
	}
	status, ok := s.loopRegistry.StatusByID(id)
	if !ok {
		s.errorResponse(w, http.StatusNotFound, "loop not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, status, s.logger)
}

// handleLoopLogs returns log entries scoped to one loop's recent conversation
// IDs as a bare JSON array, newest first, per the native contract.
// [GET /v1/loops/{id}/logs]
func (s *Server) handleLoopLogs(w http.ResponseWriter, r *http.Request) {
	if s.logQuerier == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "log index not available")
		return
	}
	if s.loopRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop registry not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "id is required")
		return
	}
	status, ok := s.loopRegistry.StatusByID(id)
	if !ok {
		s.errorResponse(w, http.StatusNotFound, "loop not found")
		return
	}
	entries := s.mergeLoopLogs(status.RecentConvIDs, r.URL.Query().Get("level"), parseLogLimit(r.URL.Query().Get("limit")))
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, entries, s.logger)
}

// defaultLogLimit matches the native API's shared Limit parameter and
// logging.Query's own default (internal/server/openapi/native.yaml).
const defaultLogLimit = 50

// parseLogLimit reads a ?limit= value, falling back to defaultLogLimit when
// unset or invalid and capping at 200 to bound a single query.
func parseLogLimit(v string) int {
	if v == "" {
		return defaultLogLimit
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultLogLimit
	}
	if n > 200 {
		return 200
	}
	return n
}

// mergeLoopLogs fans out across a loop's recent conversation IDs, merges the
// results, sorts newest first, and truncates to limit. The returned slice is
// always non-nil so callers encode a JSON array, never null.
func (s *Server) mergeLoopLogs(convIDs []string, level string, limit int) []logging.LogEntry {
	entries := make([]logging.LogEntry, 0, limit)
	for _, convID := range convIDs {
		got, err := s.logQuerier.Query(logging.QueryParams{
			ConversationID: convID,
			Level:          level,
			Limit:          limit,
		})
		if err != nil {
			s.logger.Warn("loop log query failed", "conversation_id", convID, "error", err)
			continue
		}
		entries = append(entries, got...)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// loopEvent wraps an [events.Event] for SSE serialization with a top-level
// kind field for client-side dispatch.
type loopEvent struct {
	Kind string         `json:"kind"`
	Ts   time.Time      `json:"ts"`
	Data map[string]any `json:"data,omitempty"`
}

// handleLoopEvents serves an SSE stream of loop lifecycle events: an initial
// "snapshot" of the full registry, then incremental "loop"/"delegate" events.
// [GET /v1/loops/events]
func (s *Server) handleLoopEvents(w http.ResponseWriter, r *http.Request) {
	if s.eventBus == nil || s.loopRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "event stream not available")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Omit Connection: keep-alive — it's hop-by-hop and forbidden in HTTP/2.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)
	ch := s.eventBus.Subscribe(64)
	defer s.eventBus.Unsubscribe(ch)

	snapJSON, err := json.Marshal(s.loopRegistry.Statuses())
	if err != nil {
		s.logger.Warn("failed to marshal loop snapshot", "error", err)
		return
	}
	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snapJSON); err != nil {
		return
	}
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			var sseType string
			switch evt.Source {
			case events.SourceLoop:
				sseType = "loop"
			case events.SourceDelegate:
				sseType = "delegate"
			default:
				continue
			}
			data, err := json.Marshal(loopEvent{Kind: evt.Kind, Ts: evt.Timestamp, Data: evt.Data})
			if err != nil {
				s.logger.Warn("failed to marshal loop event", "error", err)
				continue
			}
			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sseType, data); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

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
	all := s.loopRegistry.Statuses()
	rows := all
	if state := strings.TrimSpace(r.URL.Query().Get("state")); state != "" {
		filtered := make([]looppkg.Status, 0, len(all))
		for _, st := range all {
			if string(st.State) == state {
				filtered = append(filtered, st)
			}
		}
		rows = filtered
	}
	w.Header().Set("Content-Type", "application/json")
	// The resolver is built over the FULL batch so parent_name/child_count/
	// ancestry stay correct even when ?state= filtered the returned rows.
	writeJSON(w, projectLoops(s.loopViewResolver(all), rows), s.logger)
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
	resolver := s.loopViewResolver(s.loopRegistry.Statuses())
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, loopWithView{Status: status, View: resolver.FromStatus(status)}, s.logger)
}

// loopWithView is a running-loop Status enriched with its canonical LoopView
// projection (the "ps auxwwww" row) under a `view` key, so the web console
// speaks the same schedule/structure/economics/policy vocabulary as the
// model-facing Loops table. Status fields stay promoted (flat) for existing
// clients; `view` is purely additive.
type loopWithView struct {
	looppkg.Status
	View looppkg.LoopView `json:"view"`
}

// loopViewResolver builds a LoopView resolver over the full status batch joined
// to the live definition policy, so one pass resolves parent_name, child_count,
// ancestry, and policy_state for every projected row.
func (s *Server) loopViewResolver(all []looppkg.Status) looppkg.LoopViewResolver {
	return looppkg.NewLoopViewResolver(all, s.loopPolicyByName(), time.Now())
}

// loopPolicyByName joins each loop name to its stored definition's policy and
// eligibility from the live registry view, so the projection reports
// active/paused/eligible rather than "ephemeral". Returns nil when no
// definition registry is wired, which the projector reads as ephemeral.
func (s *Server) loopPolicyByName() map[string]looppkg.LoopPolicyInfo {
	if s.loopDefinitionView == nil {
		return nil
	}
	view := s.loopDefinitionView()
	if view == nil {
		return nil
	}
	out := make(map[string]looppkg.LoopPolicyInfo, len(view.Definitions))
	for _, def := range view.Definitions {
		out[def.Name] = looppkg.LoopPolicyInfo{
			State:          string(def.PolicyState),
			Source:         string(def.PolicySource),
			Reason:         def.PolicyReason,
			UpdatedAt:      def.PolicyUpdatedAt,
			Eligible:       def.Eligibility.Eligible,
			EligibleReason: def.Eligibility.Reason,
			HasPolicy:      true,
		}
	}
	return out
}

// projectLoops wraps each row with its LoopView projection using one shared
// resolver.
func projectLoops(resolver looppkg.LoopViewResolver, rows []looppkg.Status) []loopWithView {
	out := make([]loopWithView, len(rows))
	for i, st := range rows {
		out[i] = loopWithView{Status: st, View: resolver.FromStatus(st)}
	}
	return out
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

	allStatuses := s.loopRegistry.Statuses()
	snapJSON, err := json.Marshal(projectLoops(s.loopViewResolver(allStatuses), allStatuses))
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

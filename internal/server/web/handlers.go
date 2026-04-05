package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/logging"
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
	s.writeJSON(w, body)
}

// handleLoops returns a JSON array of all loop statuses.
func (s *WebServer) handleLoops(w http.ResponseWriter, _ *http.Request) {
	statuses := s.registry.Statuses()
	s.writeJSON(w, statuses)
}

// handleLoopLogs returns log entries associated with a loop's recent
// conversation IDs, queried from the SQLite log index.
func (s *WebServer) handleLoopLogs(w http.ResponseWriter, r *http.Request) {
	if s.logQuerier == nil {
		http.Error(w, `{"error":"log index not available"}`, http.StatusServiceUnavailable)
		return
	}

	loopID := r.PathValue("id")
	if loopID == "" {
		http.Error(w, `{"error":"missing loop id"}`, http.StatusBadRequest)
		return
	}

	// Look up the loop to get its recent conversation IDs.
	l := s.registry.Get(loopID)
	if l == nil {
		http.Error(w, `{"error":"loop not found"}`, http.StatusNotFound)
		return
	}

	status := l.Status()
	if len(status.RecentConvIDs) == 0 {
		s.writeJSON(w, map[string]any{"entries": []any{}, "count": 0})
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

	// Query logs for each conversation ID and merge results.
	var allEntries []logging.LogEntry
	for _, convID := range status.RecentConvIDs {
		entries, err := s.logQuerier.Query(logging.QueryParams{
			ConversationID: convID,
			Level:          level,
			Limit:          limit,
		})
		if err != nil {
			s.logger.Warn("log query failed", "conversation_id", convID, "error", err)
			continue
		}
		allEntries = append(allEntries, entries...)
	}

	// Sort chronologically and keep the most recent entries.
	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].Timestamp.Before(allEntries[j].Timestamp)
	})
	if len(allEntries) > limit {
		allEntries = allEntries[len(allEntries)-limit:]
	}

	s.writeJSON(w, map[string]any{
		"entries": allEntries,
		"count":   len(allEntries),
	})
}

// handleRequestDetail returns the full live or retained content for a
// single request, including system prompt, user/assistant messages,
// tool call arguments and results, and token metadata.
func (s *WebServer) handleRequestDetail(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("id")
	if s.contentQuerier == nil {
		s.writeJSONError(w, "request detail not available", http.StatusServiceUnavailable)
		return
	}

	if requestID == "" {
		s.writeJSONError(w, "missing request id", http.StatusBadRequest)
		return
	}

	detail, err := s.contentQuerier.QueryRequestDetail(requestID)
	if err != nil {
		s.logger.Warn("request detail query failed", "request_id", requestID, "error", err)
		s.writeJSONError(w, "query failed", http.StatusInternalServerError)
		return
	}
	if detail == nil {
		s.writeJSONError(w, "request not found", http.StatusNotFound)
		return
	}

	s.writeJSON(w, detail)
}

func (s *WebServer) handleRequestDetailProbe(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Request-Detail-Available", strconv.FormatBool(s.contentQuerier != nil))
	w.WriteHeader(http.StatusNoContent)
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

// loopEvent wraps an [events.Event] for SSE serialization with a
// top-level kind field for easy client-side dispatch.
type loopEvent struct {
	Kind string         `json:"kind"`
	Ts   time.Time      `json:"ts"`
	Data map[string]any `json:"data,omitempty"`
}

// handleLoopEvents serves an SSE stream of loop lifecycle events.
// On connect, it sends a "snapshot" event with the full registry state,
// then streams incremental "loop" events as they occur.
func (s *WebServer) handleLoopEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Set SSE headers. Omit "Connection: keep-alive" — it's a hop-by-hop
	// header forbidden in HTTP/2 (RFC 9113 §8.2.2) and can cause
	// ERR_HTTP2_PROTOCOL_ERROR if a reverse proxy forwards it.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// Manage write deadlines to prevent the server's WriteTimeout from
	// killing long-lived SSE connections.
	rc := http.NewResponseController(w)

	// Subscribe to the event bus.
	ch := s.eventBus.Subscribe(64)
	defer s.eventBus.Unsubscribe(ch)

	// Send initial snapshot so the client has full state immediately.
	statuses := s.registry.Statuses()
	snapJSON, err := json.Marshal(statuses)
	if err != nil {
		s.logger.Warn("failed to marshal snapshot", "error", err)
		return
	}

	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snapJSON); err != nil {
		return
	}
	flusher.Flush()

	// Keepalive ticker prevents WriteTimeout and proxy idle disconnects.
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

			// Forward loop and delegate events to the client.
			var sseType string
			switch evt.Source {
			case events.SourceLoop:
				sseType = "loop"
			case events.SourceDelegate:
				sseType = "delegate"
			default:
				continue
			}

			le := loopEvent{
				Kind: evt.Kind,
				Ts:   evt.Timestamp,
				Data: evt.Data,
			}
			data, err := json.Marshal(le)
			if err != nil {
				s.logger.Warn("failed to marshal event", "error", err)
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

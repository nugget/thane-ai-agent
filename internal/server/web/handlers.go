package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/logging"
)

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

	// Cap total results.
	if len(allEntries) > limit {
		allEntries = allEntries[:limit]
	}

	s.writeJSON(w, map[string]any{
		"entries": allEntries,
		"count":   len(allEntries),
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

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
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
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snapJSON)
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

			// Only forward loop events to the client.
			if evt.Source != events.SourceLoop {
				continue
			}

			le := loopEvent{
				Kind: evt.Kind,
				Ts:   evt.Timestamp,
				Data: evt.Data,
			}
			data, err := json.Marshal(le)
			if err != nil {
				s.logger.Debug("failed to marshal loop event", "error", err)
				continue
			}

			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
			fmt.Fprintf(w, "event: loop\ndata: %s\n\n", data)
			flusher.Flush()

		case <-keepalive.C:
			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
			fmt.Fprint(w, ": keepalive\n\n")
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

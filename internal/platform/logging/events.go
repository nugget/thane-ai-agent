package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
)

const (
	maxEventAttrs        = 16
	maxEventMessageRunes = 2048
	maxEventStringRunes  = 1024
	maxEventValueBytes   = 1024
)

// EventHandler publishes WARN and ERROR records to the operational event bus
// after forwarding them to the canonical logging handler. It deliberately
// excludes lower levels: the bus is a live attention surface, while the
// datasets and SQLite index remain the complete forensic record.
type EventHandler struct {
	inner    slog.Handler
	bus      *events.Bus
	preAttrs []slog.Attr
	groups   []string
}

// NewEventHandler wraps inner and promotes WARN and ERROR records onto bus.
// A nil bus preserves ordinary logging behavior without event publication.
func NewEventHandler(inner slog.Handler, bus *events.Bus) *EventHandler {
	return &EventHandler{inner: inner, bus: bus}
}

// Enabled reports whether the wrapped handler accepts level.
func (h *EventHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle writes the record and independently publishes its bounded structured
// projection. A log-sink failure is returned, but it does not suppress the
// live attention event.
func (h *EventHandler) Handle(ctx context.Context, record slog.Record) error {
	innerErr := h.inner.Handle(ctx, record)
	if h.bus == nil || record.Level < slog.LevelWarn {
		return innerErr
	}

	projection := (&DatasetHandler{
		preAttrs: h.preAttrs,
		groups:   h.groups,
	}).projectRecord(record)
	data := make(map[string]any, min(len(projection.Attrs), maxEventAttrs)+5)
	data["level"] = projection.Severity
	message, truncated := truncateEventString(projection.Message, maxEventMessageRunes)
	data["message"] = message
	putString := func(key, value string) {
		if value == "" {
			return
		}
		bounded, clipped := truncateEventString(value, maxEventStringRunes)
		data[key] = bounded
		truncated = truncated || clipped
	}
	if projection.SourceFile != "" {
		putString("source_file", projection.SourceFile)
		data["source_line"] = projection.SourceLine
	}
	keys := make([]string, 0, len(projection.Attrs))
	seen := make(map[string]bool, len(projection.Attrs))
	for _, key := range []string{"error", "tool", "model", "reason"} {
		if _, ok := projection.Attrs[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	var remaining []string
	for key := range projection.Attrs {
		if !seen[key] {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	keys = append(keys, remaining...)
	if len(keys) > maxEventAttrs {
		keys = keys[:maxEventAttrs]
		truncated = true
	}
	for _, key := range keys {
		value, clipped := boundEventValue(projection.Attrs[key])
		data[key] = value
		truncated = truncated || clipped
	}
	putString("request_id", projection.RequestID)
	putString("session_id", projection.SessionID)
	putString("conversation_id", projection.ConversationID)
	putString("loop_id", projection.LoopID)
	putString("loop_name", projection.LoopName)
	putString("subsystem", projection.Subsystem)
	putString("component", projection.Component)
	putString("kind", projection.Kind)
	if truncated {
		data["truncated"] = true
	}

	h.bus.Publish(events.Event{
		Timestamp: record.Time,
		Source:    events.SourceLog,
		Kind:      events.KindLogRecord,
		Data:      data,
	})
	return innerErr
}

func boundEventValue(value any) (any, bool) {
	if text, ok := value.(string); ok {
		return truncateEventString(text, maxEventStringRunes)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "<unrenderable>", true
	}
	if len(encoded) <= maxEventValueBytes {
		return value, false
	}
	preview, _ := truncateEventString(string(encoded), maxEventStringRunes)
	return preview, true
}

func truncateEventString(value string, maxRunes int) (string, bool) {
	count := 0
	for byteIndex := range value {
		if count == maxRunes {
			return value[:byteIndex] + "…", true
		}
		count++
	}
	return value, false
}

// WithAttrs returns a derived handler carrying attrs to both sinks.
func (h *EventHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &EventHandler{
		inner:    h.inner.WithAttrs(attrs),
		bus:      h.bus,
		preAttrs: append(cloneAttrs(h.preAttrs), attrs...),
		groups:   h.groups,
	}
}

// WithGroup returns a derived handler carrying group context to both sinks.
func (h *EventHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &EventHandler{
		inner:    h.inner.WithGroup(name),
		bus:      h.bus,
		preAttrs: cloneAttrs(h.preAttrs),
		groups:   append(append([]string(nil), h.groups...), name),
	}
}

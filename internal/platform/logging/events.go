package logging

import (
	"context"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
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
	data := make(map[string]any, len(projection.Attrs)+4)
	data["level"] = projection.Severity
	data["message"] = projection.Message
	if projection.SourceFile != "" {
		data["source_file"] = projection.SourceFile
		data["source_line"] = projection.SourceLine
	}
	for key, value := range projection.Attrs {
		data[key] = value
	}
	if projection.RequestID != "" {
		data["request_id"] = projection.RequestID
	}
	if projection.SessionID != "" {
		data["session_id"] = projection.SessionID
	}
	if projection.ConversationID != "" {
		data["conversation_id"] = projection.ConversationID
	}
	if projection.LoopID != "" {
		data["loop_id"] = projection.LoopID
	}
	if projection.LoopName != "" {
		data["loop_name"] = projection.LoopName
	}
	if projection.Subsystem != "" {
		data["subsystem"] = projection.Subsystem
	}
	if projection.Component != "" {
		data["component"] = projection.Component
	}

	h.bus.Publish(events.Event{
		Timestamp: record.Time,
		Source:    events.SourceLog,
		Kind:      events.KindLogRecord,
		Data:      data,
	})
	return innerErr
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

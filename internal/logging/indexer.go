package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"
)

// promotedKeys lists log attribute keys that are extracted into their
// own indexed SQLite columns rather than being stored in the catch-all
// attrs JSON blob.
var promotedKeys = map[string]bool{
	"request_id":      true,
	"session_id":      true,
	"conversation_id": true,
	"subsystem":       true,
	"tool":            true,
	"model":           true,
}

// IndexHandler is an [slog.Handler] that wraps another handler and
// simultaneously indexes every log record into a SQLite database.
// The wrapped handler produces the canonical raw log output (file
// and/or stdout); the SQLite index enables fast queries by time,
// level, request ID, subsystem, etc.
//
// Writes to SQLite happen asynchronously via a buffered channel so
// that logging never blocks on database I/O. The channel is drained
// by a single background goroutine started by [NewIndexHandler].
// Call [IndexHandler.Close] to flush pending entries and stop the
// background goroutine.
type IndexHandler struct {
	inner   slog.Handler
	rotator *Rotator // for raw_file / raw_line; nil if file logging disabled

	// preAttrs are attributes added via WithAttrs that apply to every
	// record handled by this instance.
	preAttrs []slog.Attr
	groups   []string

	// shared is the mutable state shared across all handlers derived
	// from the same root via WithAttrs/WithGroup. Only the root handler
	// owns the goroutine and close channel.
	shared *indexShared
}

// indexShared holds the shared mutable state for an IndexHandler tree.
// All handlers derived from the same root via WithAttrs/WithGroup
// share a single indexShared.
type indexShared struct {
	db   *sql.DB
	ch   chan indexEntry
	done chan struct{}
	once sync.Once // guards Close
}

// indexEntry is the set of fields written to SQLite for one log record.
type indexEntry struct {
	Timestamp      time.Time
	Level          string
	Msg            string
	RequestID      string
	SessionID      string
	ConversationID string
	Subsystem      string
	Tool           string
	Model          string
	SourceFile     string
	SourceLine     int
	Attrs          string // JSON object of non-promoted attributes
	RawFile        string
	RawLine        int
}

// indexBufSize is the channel buffer for async SQLite writes. Sized to
// absorb short bursts without backpressure.
const indexBufSize = 4096

// NewIndexHandler wraps inner with a SQLite indexing handler. The db
// must be an open SQLite connection (typically from [database.Open]).
// If rotator is non-nil, each entry records the raw log filename and
// line number for back-linking.
//
// The caller must call [IndexHandler.Close] on shutdown to flush
// pending entries and release the background goroutine.
func NewIndexHandler(inner slog.Handler, db *sql.DB, rotator *Rotator) *IndexHandler {
	s := &indexShared{
		db:   db,
		ch:   make(chan indexEntry, indexBufSize),
		done: make(chan struct{}),
	}
	h := &IndexHandler{
		inner:   inner,
		rotator: rotator,
		shared:  s,
	}
	go h.drain()
	return h
}

// Enabled reports whether the handler handles records at the given level.
func (h *IndexHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle delegates to the wrapped handler and then asynchronously
// indexes the record into SQLite.
func (h *IndexHandler) Handle(ctx context.Context, r slog.Record) error {
	// Delegate to the wrapped handler first (raw log output).
	if err := h.inner.Handle(ctx, r); err != nil {
		return err
	}

	// Build the index entry.
	entry := indexEntry{
		Timestamp: r.Time,
		Level:     r.Level.String(),
		Msg:       r.Message,
	}

	// Source location from the record's PC.
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		entry.SourceFile = f.File
		entry.SourceLine = f.Line
	}

	// Raw file back-link.
	if h.rotator != nil {
		entry.RawFile = h.rotator.ActiveFile()
		entry.RawLine = h.rotator.LineCount()
	}

	// Collect all attributes: pre-set ones from WithAttrs, then per-record.
	extras := make(map[string]any)

	// Process pre-set attributes (from WithAttrs calls).
	for _, a := range h.preAttrs {
		h.classifyAttr(a, &entry, extras, h.groups)
	}

	// Process per-record attributes.
	r.Attrs(func(a slog.Attr) bool {
		h.classifyAttr(a, &entry, extras, h.groups)
		return true
	})

	// Marshal remaining (non-promoted) attributes as JSON.
	if len(extras) > 0 {
		b, _ := json.Marshal(extras)
		entry.Attrs = string(b)
	}

	// Non-blocking send; drop the entry if the channel is full (better
	// to lose an index entry than to block a log call).
	select {
	case h.shared.ch <- entry:
	default:
	}

	return nil
}

// WithAttrs returns a new handler with the given attributes pre-set.
func (h *IndexHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &IndexHandler{
		inner:    h.inner.WithAttrs(attrs),
		rotator:  h.rotator,
		preAttrs: append(cloneAttrs(h.preAttrs), attrs...),
		groups:   h.groups,
		shared:   h.shared,
	}
}

// WithGroup returns a new handler with the given group name.
func (h *IndexHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &IndexHandler{
		inner:    h.inner.WithGroup(name),
		rotator:  h.rotator,
		preAttrs: cloneAttrs(h.preAttrs),
		groups:   append(append([]string(nil), h.groups...), name),
		shared:   h.shared,
	}
}

// Close flushes pending index entries and stops the background
// goroutine. It is safe to call multiple times.
func (h *IndexHandler) Close() {
	h.shared.once.Do(func() {
		close(h.shared.ch)
		<-h.shared.done
	})
}

// Migrate creates or upgrades the log_entries table and indexes.
// Call this once after opening the database and before logging begins.
func Migrate(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS log_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		level TEXT NOT NULL,
		msg TEXT NOT NULL,
		request_id TEXT,
		session_id TEXT,
		conversation_id TEXT,
		subsystem TEXT,
		tool TEXT,
		model TEXT,
		source_file TEXT,
		source_line INTEGER,
		attrs TEXT,
		raw_file TEXT,
		raw_line INTEGER
	);

	CREATE INDEX IF NOT EXISTS idx_log_timestamp ON log_entries(timestamp);
	CREATE INDEX IF NOT EXISTS idx_log_level ON log_entries(level);
	CREATE INDEX IF NOT EXISTS idx_log_request ON log_entries(request_id);
	CREATE INDEX IF NOT EXISTS idx_log_session ON log_entries(session_id);
	CREATE INDEX IF NOT EXISTS idx_log_conversation ON log_entries(conversation_id);
	CREATE INDEX IF NOT EXISTS idx_log_subsystem ON log_entries(subsystem);
	CREATE INDEX IF NOT EXISTS idx_log_tool ON log_entries(tool);
	CREATE INDEX IF NOT EXISTS idx_log_model ON log_entries(model);
	`
	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate log index: %w", err)
	}
	return nil
}

// drain runs in a goroutine, reading entries from the channel and
// inserting them into SQLite. It exits when the channel is closed.
func (h *IndexHandler) drain() {
	defer close(h.shared.done)

	const insertSQL = `INSERT INTO log_entries
		(timestamp, level, msg, request_id, session_id, conversation_id,
		 subsystem, tool, model, source_file, source_line, attrs,
		 raw_file, raw_line)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	stmt, err := h.shared.db.Prepare(insertSQL)
	if err != nil {
		// If we can't prepare the statement, there's nothing useful we
		// can do. Log entries will be silently dropped.
		return
	}
	defer stmt.Close()

	for e := range h.shared.ch {
		_, _ = stmt.Exec(
			e.Timestamp.Format(time.RFC3339Nano),
			e.Level,
			e.Msg,
			nullString(e.RequestID),
			nullString(e.SessionID),
			nullString(e.ConversationID),
			nullString(e.Subsystem),
			nullString(e.Tool),
			nullString(e.Model),
			nullString(e.SourceFile),
			nullInt(e.SourceLine),
			nullString(e.Attrs),
			nullString(e.RawFile),
			nullInt(e.RawLine),
		)
	}
}

// classifyAttr routes a single attribute to either a promoted column
// or the extras map. Group prefixes are applied to non-promoted keys.
func (h *IndexHandler) classifyAttr(a slog.Attr, entry *indexEntry, extras map[string]any, groups []string) {
	// Resolve LogValuer interfaces to their final value.
	a.Value = a.Value.Resolve()

	// Skip empty attributes.
	if a.Equal(slog.Attr{}) {
		return
	}

	key := a.Key

	// Handle slog.Group attributes (nested key-value pairs) before
	// the group-prefix path so that group-valued attrs are always
	// recursively flattened regardless of the current group depth.
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		childGroups := groups
		if key != "" {
			childGroups = append(append([]string(nil), groups...), key)
		}
		for _, ga := range attrs {
			h.classifyAttr(ga, entry, extras, childGroups)
		}
		return
	}

	// Build the fully qualified key when inside a group.
	qualifiedKey := key
	if len(groups) > 0 {
		qualifiedKey = strings.Join(groups, ".") + "." + key
	}

	// Promote known keys into dedicated columns (top-level only).
	if len(groups) == 0 && promotedKeys[key] {
		v := a.Value.String()
		switch key {
		case "request_id":
			entry.RequestID = v
		case "session_id":
			entry.SessionID = v
		case "conversation_id":
			entry.ConversationID = v
		case "subsystem":
			entry.Subsystem = v
		case "tool":
			entry.Tool = v
		case "model":
			entry.Model = v
		}
		return
	}

	// Skip built-in keys that are already stored as dedicated fields.
	switch key {
	case slog.TimeKey, slog.LevelKey, slog.MessageKey, slog.SourceKey:
		return
	}

	extras[qualifiedKey] = attrValue(a)
}

// attrValue returns a JSON-friendly representation of the attribute's
// value. For error and fmt.Stringer types whose underlying struct
// would marshal as "{}", it returns the string representation instead.
func attrValue(a slog.Attr) any {
	v := a.Value.Any()
	switch tv := v.(type) {
	case error:
		return tv.Error()
	case fmt.Stringer:
		return tv.String()
	default:
		return v
	}
}

// cloneAttrs returns a shallow copy of the attribute slice.
func cloneAttrs(attrs []slog.Attr) []slog.Attr {
	if len(attrs) == 0 {
		return nil
	}
	c := make([]slog.Attr, len(attrs))
	copy(c, attrs)
	return c
}

// nullString returns a sql.NullString that is null when s is empty.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullInt returns a sql.NullInt64 that is null when n is zero.
func nullInt(n int) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}

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

	// Build the index entry. Normalize the level string so custom
	// levels (e.g., LevelTrace = -8) are stored as "TRACE" rather
	// than slog's default "DEBUG-4". This keeps the DB consistent
	// with log output, which uses ReplaceLogLevelNames.
	entry := indexEntry{
		Timestamp: r.Time,
		Level:     normalizeLevel(r.Level),
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
			e.Timestamp.UTC().Format(time.RFC3339Nano),
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

// normalizeLevel returns a human-readable level string. Custom levels
// (like LevelTrace = -8) are mapped to their canonical names rather
// than slog's default rendering (e.g., "DEBUG-4" → "TRACE").
func normalizeLevel(l slog.Level) string {
	switch l {
	case slog.LevelDebug - 4: // config.LevelTrace
		return "TRACE"
	default:
		return l.String()
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

// Prune deletes log index entries older than maxAge whose level is
// strictly below minKeepLevel. For example, passing [slog.LevelInfo]
// prunes DEBUG and TRACE entries while keeping INFO, WARN, and ERROR.
// Returns the number of rows deleted.
func Prune(db *sql.DB, maxAge time.Duration, minKeepLevel slog.Level) (int64, error) {
	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339Nano)

	// Build a list of levels to prune (those below minKeepLevel).
	// Uses normalizeLevel so level strings match the DB values
	// (e.g., "TRACE" not "DEBUG-4").
	var pruneLevels []string
	for _, l := range []slog.Level{slog.LevelDebug - 4, slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if l < minKeepLevel {
			pruneLevels = append(pruneLevels, normalizeLevel(l))
		}
	}
	if len(pruneLevels) == 0 {
		return 0, nil
	}

	// Build placeholders for the IN clause.
	placeholders := make([]string, len(pruneLevels))
	args := make([]any, 0, len(pruneLevels)+1)
	args = append(args, cutoff)
	for i, l := range pruneLevels {
		placeholders[i] = "?"
		args = append(args, l)
	}

	query := fmt.Sprintf(
		"DELETE FROM log_entries WHERE timestamp < ? AND level IN (%s)",
		strings.Join(placeholders, ", "),
	)

	res, err := db.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("prune log index: %w", err)
	}
	return res.RowsAffected()
}

// LogEntry is an exported representation of a log index row suitable
// for display in the web dashboard and tool queries.
type LogEntry struct {
	ID             int64
	Timestamp      time.Time
	Level          string
	Msg            string
	RequestID      string
	SessionID      string
	ConversationID string
	Subsystem      string
	Tool           string
	Model          string
	Attrs          string
	SourceFile     string
	SourceLine     int
}

// QueryParams holds filter criteria for querying the log index.
// All fields are optional — zero values are ignored. Level is treated
// as a minimum severity: WARN returns WARN and ERROR entries, DEBUG
// returns everything including TRACE.
type QueryParams struct {
	SessionID        string
	ConversationID   string
	RequestID        string
	Subsystem        string
	Tool             string
	Model            string
	Level            string    // minimum level: ERROR > WARN > INFO > DEBUG
	Since            time.Time // zero = no lower bound
	Until            time.Time // zero = defaults to now
	Pattern          string    // substring match on msg
	SourceFilePrefix string    // prefix match on source_file (e.g., "cmd/thane/")
	Limit            int       // default 50, max 200
}

// QueryBySession returns log entries matching the given session ID,
// ordered by timestamp ascending (chronological). When limit is
// positive, only the most recent limit entries are returned — the
// query selects newest-first and then reverses in Go so callers
// always receive chronological order. Optional filters narrow by
// level and/or subsystem.
func QueryBySession(db *sql.DB, sessionID, level, subsystem string, limit int) ([]LogEntry, error) {
	query := `SELECT id, timestamp, level, msg, request_id, session_id,
		conversation_id, subsystem, tool, model, attrs, source_file, source_line
		FROM log_entries WHERE session_id = ?`
	args := []any{sessionID}

	if level != "" {
		query += " AND level = ?"
		args = append(args, level)
	}
	if subsystem != "" {
		query += " AND subsystem = ?"
		args = append(args, subsystem)
	}

	// Select newest entries first so LIMIT returns the tail of the
	// log rather than the head. We reverse in Go below to restore
	// chronological order.
	query += " ORDER BY timestamp DESC"

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query log entries: %w", err)
	}
	defer rows.Close()

	entries, err := scanLogEntries(rows)
	if err != nil {
		return nil, err
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	return entries, nil
}

// Query returns log entries matching the given filter parameters,
// ordered by timestamp ascending (chronological). The limit defaults
// to 50 and is capped at 200. Level filtering is "minimum level" —
// e.g., WARN returns WARN and ERROR entries.
func Query(db *sql.DB, params QueryParams) ([]LogEntry, error) {
	query := `SELECT id, timestamp, level, msg, request_id, session_id,
		conversation_id, subsystem, tool, model, attrs, source_file, source_line
		FROM log_entries WHERE 1=1`
	var args []any

	if params.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, params.SessionID)
	}
	if params.ConversationID != "" {
		query += " AND conversation_id = ?"
		args = append(args, params.ConversationID)
	}
	if params.RequestID != "" {
		query += " AND request_id = ?"
		args = append(args, params.RequestID)
	}
	if params.Subsystem != "" {
		query += " AND subsystem = ?"
		args = append(args, params.Subsystem)
	}
	if params.Tool != "" {
		query += " AND tool = ?"
		args = append(args, params.Tool)
	}
	if params.Model != "" {
		query += " AND model = ?"
		args = append(args, params.Model)
	}
	if params.Level != "" {
		levels := levelsAtOrAbove(params.Level)
		if len(levels) > 0 {
			placeholders := make([]string, len(levels))
			for i, l := range levels {
				placeholders[i] = "?"
				args = append(args, l)
			}
			query += " AND level IN (" + strings.Join(placeholders, ", ") + ")"
		}
	}
	if !params.Since.IsZero() {
		query += " AND timestamp >= ?"
		args = append(args, params.Since.UTC().Format(time.RFC3339Nano))
	}
	until := params.Until
	if until.IsZero() {
		until = time.Now()
	}
	query += " AND timestamp <= ?"
	args = append(args, until.UTC().Format(time.RFC3339Nano))

	if params.Pattern != "" {
		query += " AND msg LIKE '%' || ? || '%'"
		args = append(args, params.Pattern)
	}
	if params.SourceFilePrefix != "" {
		query += " AND source_file LIKE ? || '%'"
		args = append(args, params.SourceFilePrefix)
	}

	// Default and cap limit.
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	// Select newest first so LIMIT returns the most recent entries,
	// then reverse to chronological order.
	query += fmt.Sprintf(" ORDER BY timestamp DESC LIMIT %d", limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query log entries: %w", err)
	}
	defer rows.Close()

	entries, err := scanLogEntries(rows)
	if err != nil {
		return nil, err
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	return entries, nil
}

// scanLogEntries reads LogEntry rows from a *sql.Rows. The SELECT must
// return columns in the order: id, timestamp, level, msg, request_id,
// session_id, conversation_id, subsystem, tool, model, attrs,
// source_file, source_line.
func scanLogEntries(rows *sql.Rows) ([]LogEntry, error) {
	var entries []LogEntry
	for rows.Next() {
		var e LogEntry
		var ts string
		var reqID, sessID, convID sql.NullString
		var sub, tool, model, attrs, srcFile sql.NullString
		var srcLine sql.NullInt64

		if err := rows.Scan(
			&e.ID, &ts, &e.Level, &e.Msg,
			&reqID, &sessID, &convID,
			&sub, &tool, &model, &attrs,
			&srcFile, &srcLine,
		); err != nil {
			return nil, fmt.Errorf("scan log entry: %w", err)
		}

		parsed, parseErr := time.Parse(time.RFC3339Nano, ts)
		if parseErr != nil {
			return nil, fmt.Errorf("parse log timestamp %q: %w", ts, parseErr)
		}
		e.Timestamp = parsed
		e.RequestID = reqID.String
		e.SessionID = sessID.String
		e.ConversationID = convID.String
		e.Subsystem = sub.String
		e.Tool = tool.String
		e.Model = model.String
		e.Attrs = attrs.String
		e.SourceFile = srcFile.String
		e.SourceLine = int(srcLine.Int64)

		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// levelsAtOrAbove returns all log level strings at or above the given
// minimum severity. For example, "WARN" returns ["WARN", "ERROR"].
// DEBUG includes TRACE. Unknown levels return nil.
func levelsAtOrAbove(minLevel string) []string {
	all := []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR"}
	switch strings.ToUpper(minLevel) {
	case "TRACE":
		return all
	case "DEBUG":
		return all // DEBUG includes TRACE
	case "INFO":
		return []string{"INFO", "WARN", "ERROR"}
	case "WARN":
		return []string{"WARN", "ERROR"}
	case "ERROR":
		return []string{"ERROR"}
	default:
		return nil
	}
}

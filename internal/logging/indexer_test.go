package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// openTestDB creates a temporary SQLite database and runs Migrate.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/test.db?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestIndexHandler_BasicWrite(t *testing.T) {
	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, nil)
	h := NewIndexHandler(inner, db, nil)
	defer h.Close()

	logger := slog.New(h)
	logger.Info("hello world", "subsystem", "test", "request_id", "r_abc123")

	// Close to flush the channel.
	h.Close()

	var (
		msg       string
		level     string
		subsystem sql.NullString
		reqID     sql.NullString
	)
	err := db.QueryRow(`SELECT msg, level, subsystem, request_id FROM log_entries LIMIT 1`).
		Scan(&msg, &level, &subsystem, &reqID)
	if err != nil {
		t.Fatal(err)
	}

	if msg != "hello world" {
		t.Errorf("msg = %q, want %q", msg, "hello world")
	}
	if level != "INFO" {
		t.Errorf("level = %q, want %q", level, "INFO")
	}
	if !subsystem.Valid || subsystem.String != "test" {
		t.Errorf("subsystem = %v, want %q", subsystem, "test")
	}
	if !reqID.Valid || reqID.String != "r_abc123" {
		t.Errorf("request_id = %v, want %q", reqID, "r_abc123")
	}
}

func TestIndexHandler_PromotedFields(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		value  string
		column string
	}{
		{"request_id", "request_id", "r_001", "request_id"},
		{"session_id", "session_id", "s_002", "session_id"},
		{"conversation_id", "conversation_id", "c_003", "conversation_id"},
		{"subsystem", "subsystem", "agent", "subsystem"},
		{"tool", "tool", "web_search", "tool"},
		{"model", "model", "claude-3-opus", "model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			inner := slog.NewJSONHandler(discardWriter{}, nil)
			h := NewIndexHandler(inner, db, nil)

			logger := slog.New(h)
			logger.Info("test", tt.key, tt.value)
			h.Close()

			var got sql.NullString
			err := db.QueryRow(`SELECT ` + tt.column + ` FROM log_entries LIMIT 1`).Scan(&got)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Valid || got.String != tt.value {
				t.Errorf("%s = %v, want %q", tt.column, got, tt.value)
			}
		})
	}
}

func TestIndexHandler_WithAttrsPreserved(t *testing.T) {
	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, nil)
	h := NewIndexHandler(inner, db, nil)

	// Create a child handler with pre-set attributes.
	child := h.WithAttrs([]slog.Attr{
		slog.String("subsystem", "delegate"),
		slog.String("session_id", "s_pre"),
	})

	logger := slog.New(child)
	logger.Info("delegated work", "tool", "shell_exec")
	h.Close()

	var (
		subsystem sql.NullString
		sessionID sql.NullString
		tool      sql.NullString
	)
	err := db.QueryRow(`SELECT subsystem, session_id, tool FROM log_entries LIMIT 1`).
		Scan(&subsystem, &sessionID, &tool)
	if err != nil {
		t.Fatal(err)
	}

	if !subsystem.Valid || subsystem.String != "delegate" {
		t.Errorf("subsystem = %v, want %q", subsystem, "delegate")
	}
	if !sessionID.Valid || sessionID.String != "s_pre" {
		t.Errorf("session_id = %v, want %q", sessionID, "s_pre")
	}
	if !tool.Valid || tool.String != "shell_exec" {
		t.Errorf("tool = %v, want %q", tool, "shell_exec")
	}
}

func TestIndexHandler_NonPromotedGoToAttrs(t *testing.T) {
	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, nil)
	h := NewIndexHandler(inner, db, nil)

	logger := slog.New(h)
	logger.Info("custom fields", "elapsed", "1.5s", "entity_id", "light.kitchen")
	h.Close()

	var attrs sql.NullString
	err := db.QueryRow(`SELECT attrs FROM log_entries LIMIT 1`).Scan(&attrs)
	if err != nil {
		t.Fatal(err)
	}

	if !attrs.Valid {
		t.Fatal("attrs should not be null")
	}
	// The attrs JSON should contain both custom fields.
	if got := attrs.String; got == "" || got == "{}" {
		t.Errorf("attrs = %q, expected non-empty JSON", got)
	}
}

func TestIndexHandler_ErrorValueInAttrs(t *testing.T) {
	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, nil)
	h := NewIndexHandler(inner, db, nil)

	logger := slog.New(h)
	logger.Info("operation failed", "error", fmt.Errorf("connection refused"))
	h.Close()

	var attrs sql.NullString
	err := db.QueryRow(`SELECT attrs FROM log_entries LIMIT 1`).Scan(&attrs)
	if err != nil {
		t.Fatal(err)
	}

	if !attrs.Valid {
		t.Fatal("attrs should not be null")
	}

	// The error should be stored as its message string, not "{}".
	var parsed map[string]any
	if err := json.Unmarshal([]byte(attrs.String), &parsed); err != nil {
		t.Fatalf("parse attrs JSON: %v", err)
	}
	got, ok := parsed["error"]
	if !ok {
		t.Fatal("attrs missing 'error' key")
	}
	if got != "connection refused" {
		t.Errorf("error = %v, want %q", got, "connection refused")
	}
}

func TestIndexHandler_WithGroupPrefix(t *testing.T) {
	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, nil)
	h := NewIndexHandler(inner, db, nil)

	// Create a handler inside a group.
	grouped := h.WithGroup("source")
	logger := slog.New(grouped)
	logger.Info("grouped entry", "file", "main.go", "line", 42)
	h.Close()

	var attrs sql.NullString
	err := db.QueryRow(`SELECT attrs FROM log_entries LIMIT 1`).Scan(&attrs)
	if err != nil {
		t.Fatal(err)
	}

	if !attrs.Valid {
		t.Fatal("attrs should not be null")
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(attrs.String), &parsed); err != nil {
		t.Fatalf("parse attrs JSON: %v", err)
	}

	// Keys should be prefixed with the group name.
	if _, ok := parsed["source.file"]; !ok {
		t.Errorf("expected key 'source.file', got attrs: %v", parsed)
	}
	if _, ok := parsed["source.line"]; !ok {
		t.Errorf("expected key 'source.line', got attrs: %v", parsed)
	}
}

func TestIndexHandler_WithRotator(t *testing.T) {
	dir := t.TempDir()
	rotator, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer rotator.Close()

	// Write some lines through the rotator to advance the counter.
	for range 5 {
		if _, err := rotator.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
	}

	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, nil)
	h := NewIndexHandler(inner, db, rotator)

	logger := slog.New(h)
	logger.Info("with rotator context")
	h.Close()

	var (
		rawFile sql.NullString
		rawLine sql.NullInt64
	)
	err = db.QueryRow(`SELECT raw_file, raw_line FROM log_entries LIMIT 1`).
		Scan(&rawFile, &rawLine)
	if err != nil {
		t.Fatal(err)
	}

	if !rawFile.Valid || rawFile.String != activeLogName {
		t.Errorf("raw_file = %v, want %q", rawFile, activeLogName)
	}
	if !rawLine.Valid || rawLine.Int64 != 5 {
		t.Errorf("raw_line = %v, want 5", rawLine)
	}
}

func TestIndexHandler_MultipleEntries(t *testing.T) {
	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, nil)
	h := NewIndexHandler(inner, db, nil)

	logger := slog.New(h)
	for i := range 10 {
		logger.Info("entry", "i", i)
	}
	h.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM log_entries`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 10 {
		t.Errorf("count = %d, want 10", count)
	}
}

func TestIndexHandler_Enabled(t *testing.T) {
	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})
	h := NewIndexHandler(inner, db, nil)
	defer h.Close()

	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("should not be enabled for INFO when inner is WARN")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("should be enabled for ERROR when inner is WARN")
	}
}

func TestIndexHandler_TimestampStored(t *testing.T) {
	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, nil)
	h := NewIndexHandler(inner, db, nil)

	logger := slog.New(h)
	before := time.Now()
	logger.Info("timestamp test")
	h.Close()

	var ts string
	if err := db.QueryRow(`SELECT timestamp FROM log_entries LIMIT 1`).Scan(&ts); err != nil {
		t.Fatal(err)
	}

	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("parse timestamp %q: %v", ts, err)
	}
	if parsed.Before(before.Add(-time.Second)) {
		t.Errorf("timestamp %v is too old (before %v)", parsed, before)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db := openTestDB(t)
	// Migrate is already called by openTestDB; calling again should be a no-op.
	if err := Migrate(db); err != nil {
		t.Errorf("second Migrate failed: %v", err)
	}
}

// discardWriter is an io.Writer that discards all data.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestPrune_DebugAndTrace(t *testing.T) {
	db := openTestDB(t)

	old := time.Now().UTC().Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	recent := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339Nano)

	for _, e := range []struct {
		ts, level, msg string
	}{
		{old, "TRACE", "old trace"},
		{old, "DEBUG", "old debug"},
		{old, "INFO", "old info"},
		{old, "ERROR", "old error"},
		{recent, "DEBUG", "recent debug"},
	} {
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
			e.ts, e.level, e.msg,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	// minKeepLevel=INFO prunes old TRACE and DEBUG, keeps INFO+.
	deleted, err := Prune(db, 90*24*time.Hour, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2 (old TRACE + old DEBUG)", deleted)
	}

	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM log_entries`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 3 {
		t.Errorf("remaining = %d, want 3 (old INFO, old ERROR, recent DEBUG)", remaining)
	}
}

func TestPrune_WarnMinLevel(t *testing.T) {
	db := openTestDB(t)

	old := time.Now().UTC().Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)

	for _, e := range []struct {
		ts, level, msg string
	}{
		{old, "TRACE", "old trace"},
		{old, "DEBUG", "old debug"},
		{old, "INFO", "old info"},
		{old, "WARN", "old warn"},
		{old, "ERROR", "old error"},
	} {
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
			e.ts, e.level, e.msg,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	// minKeepLevel=WARN prunes old TRACE, DEBUG, and INFO.
	deleted, err := Prune(db, 90*24*time.Hour, slog.LevelWarn)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3 (TRACE, DEBUG, INFO)", deleted)
	}

	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM log_entries`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Errorf("remaining = %d, want 2 (old WARN, old ERROR)", remaining)
	}
}

func TestPrune_NothingToPrune(t *testing.T) {
	db := openTestDB(t)

	recent := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339Nano)

	_, err := db.Exec(
		`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
		recent, "DEBUG", "recent debug",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Recent entry should not be pruned.
	deleted, err := Prune(db, 7*24*time.Hour, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
}

func TestNormalizeLevel(t *testing.T) {
	tests := []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelDebug - 4, "TRACE"},
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelWarn, "WARN"},
		{slog.LevelError, "ERROR"},
	}
	for _, tt := range tests {
		got := normalizeLevel(tt.level)
		if got != tt.want {
			t.Errorf("normalizeLevel(%d) = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestQuery_Filters(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	ts1 := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	ts2 := now.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	ts3 := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)

	entries := []struct {
		ts, level, msg, reqID, sessID, convID, subsystem, tool, model string
	}{
		{ts1, "INFO", "agent loop started", "r_001", "s_aaa", "c_111", "agent", "", "claude-3-opus"},
		{ts2, "WARN", "rate limited", "r_001", "s_aaa", "c_111", "agent", "web_search", "claude-3-opus"},
		{ts3, "ERROR", "connection refused", "r_002", "s_bbb", "c_222", "delegate", "shell_exec", "claude-3-haiku"},
		{ts3, "DEBUG", "cache hit", "r_003", "s_aaa", "c_111", "metacog", "", ""},
	}
	for _, e := range entries {
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg, request_id, session_id,
				conversation_id, subsystem, tool, model) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ts, e.level, e.msg, e.reqID, e.sessID, e.convID, e.subsystem, e.tool, e.model,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("session filter", func(t *testing.T) {
		got, err := Query(db, QueryParams{SessionID: "s_bbb"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Msg != "connection refused" {
			t.Errorf("got %d entries, want 1 with msg 'connection refused'", len(got))
		}
	})

	t.Run("conversation filter", func(t *testing.T) {
		got, err := Query(db, QueryParams{ConversationID: "c_111"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Errorf("got %d entries, want 3", len(got))
		}
	})

	t.Run("request filter", func(t *testing.T) {
		got, err := Query(db, QueryParams{RequestID: "r_001"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("got %d entries, want 2", len(got))
		}
	})

	t.Run("subsystem filter", func(t *testing.T) {
		got, err := Query(db, QueryParams{Subsystem: "delegate"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("got %d entries, want 1", len(got))
		}
	})

	t.Run("tool filter", func(t *testing.T) {
		got, err := Query(db, QueryParams{Tool: "web_search"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Msg != "rate limited" {
			t.Errorf("got %d entries, want 1 with msg 'rate limited'", len(got))
		}
	})

	t.Run("model filter", func(t *testing.T) {
		got, err := Query(db, QueryParams{Model: "claude-3-haiku"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("got %d entries, want 1", len(got))
		}
	})

	t.Run("combined filters", func(t *testing.T) {
		got, err := Query(db, QueryParams{SessionID: "s_aaa", Subsystem: "agent"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("got %d entries, want 2", len(got))
		}
	})

	t.Run("no results", func(t *testing.T) {
		got, err := Query(db, QueryParams{SessionID: "nonexistent"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %d entries, want 0", len(got))
		}
	})
}

func TestQuery_SourceFilePrefix(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	for i, e := range []struct {
		srcFile, msg string
	}{
		{"cmd/thane/main.go", "startup"},
		{"cmd/thane/main.go", "wiring complete"},
		{"internal/connwatch/connwatch.go", "service connected"},
		{"internal/agent/loop.go", "agent iteration"},
	} {
		ts := now.Add(-time.Duration(4-i) * time.Second).Format(time.RFC3339Nano)
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg, source_file) VALUES (?, ?, ?, ?)`,
			ts, "INFO", e.msg, e.srcFile,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := Query(db, QueryParams{SourceFilePrefix: "cmd/thane/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2 from cmd/thane/", len(got))
	}
	for _, e := range got {
		if e.SourceFile != "cmd/thane/main.go" {
			t.Errorf("unexpected source_file = %q", e.SourceFile)
		}
	}
}

func TestQuery_LevelMinimum(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	for i, level := range []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR"} {
		ts := now.Add(-time.Duration(5-i) * time.Second).Format(time.RFC3339Nano)
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
			ts, level, level+" message",
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		level string
		want  int
	}{
		{"ERROR", 1},
		{"WARN", 2},
		{"INFO", 3},
		{"DEBUG", 5}, // includes TRACE
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			got, err := Query(db, QueryParams{Level: tt.level})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.want {
				t.Errorf("level=%s: got %d entries, want %d", tt.level, len(got), tt.want)
			}
		})
	}
}

func TestQuery_ChronologicalOrder(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	for i := range 5 {
		ts := now.Add(-time.Duration(5-i) * time.Second).Format(time.RFC3339Nano)
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
			ts, "INFO", fmt.Sprintf("msg-%d", i),
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := Query(db, QueryParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d entries, want 5", len(got))
	}

	// Verify chronological order (oldest first).
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Errorf("entry %d (%v) is before entry %d (%v)", i, got[i].Timestamp, i-1, got[i-1].Timestamp)
		}
	}
}

func TestQuery_DefaultAndMaxLimit(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	for i := range 100 {
		ts := now.Add(-time.Duration(100-i) * time.Millisecond).Format(time.RFC3339Nano)
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
			ts, "INFO", fmt.Sprintf("msg-%d", i),
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Default limit is 50.
	got, err := Query(db, QueryParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Errorf("default limit: got %d entries, want 50", len(got))
	}

	// Explicit limit of 10.
	got, err = Query(db, QueryParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Errorf("limit=10: got %d entries, want 10", len(got))
	}

	// Cap at 200.
	got, err = Query(db, QueryParams{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		// Only 100 exist, so we get all of them (200 cap > 100 entries).
		t.Errorf("limit=500: got %d entries, want 100 (all available)", len(got))
	}
}

func TestQuery_PatternMatch(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	for i, msg := range []string{"agent loop started", "rate limited", "agent loop completed"} {
		ts := now.Add(-time.Duration(3-i) * time.Second).Format(time.RFC3339Nano)
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
			ts, "INFO", msg,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := Query(db, QueryParams{Pattern: "agent loop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2", len(got))
	}
}

func TestQuery_TimestampRange(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	mid := now.Add(-1 * time.Hour)
	recent := now.Add(-5 * time.Minute)

	for _, e := range []struct {
		ts  time.Time
		msg string
	}{
		{old, "old entry"},
		{mid, "mid entry"},
		{recent, "recent entry"},
	} {
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
			e.ts.Format(time.RFC3339Nano), "INFO", e.msg,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Since 2 hours ago should include mid and recent.
	got, err := Query(db, QueryParams{Since: now.Add(-2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("since 2h: got %d entries, want 2", len(got))
	}

	// Until 30 min ago should include old and mid.
	got, err = Query(db, QueryParams{Until: now.Add(-30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("until 30m: got %d entries, want 2", len(got))
	}
}

func TestQuery_ExtendedLogEntry(t *testing.T) {
	db := openTestDB(t)

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO log_entries (timestamp, level, msg, request_id, session_id, conversation_id, subsystem)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts, "INFO", "test entry", "r_xyz", "s_abc", "c_def", "agent",
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Query(db, QueryParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}

	e := got[0]
	if e.RequestID != "r_xyz" {
		t.Errorf("RequestID = %q, want %q", e.RequestID, "r_xyz")
	}
	if e.SessionID != "s_abc" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "s_abc")
	}
	if e.ConversationID != "c_def" {
		t.Errorf("ConversationID = %q, want %q", e.ConversationID, "c_def")
	}
}

func TestLevelsAtOrAbove(t *testing.T) {
	tests := []struct {
		level string
		want  int
	}{
		{"ERROR", 1},
		{"WARN", 2},
		{"INFO", 3},
		{"DEBUG", 5},
		{"TRACE", 5},
		{"UNKNOWN", 0},
	}

	for _, tt := range tests {
		got := levelsAtOrAbove(tt.level)
		if len(got) != tt.want {
			t.Errorf("levelsAtOrAbove(%q) = %v (len %d), want len %d", tt.level, got, len(got), tt.want)
		}
	}
}

func TestQueryBySession(t *testing.T) {
	db := openTestDB(t)

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	for _, e := range []struct {
		session, level, subsystem, msg string
	}{
		{"sess-1", "INFO", "agent", "msg1"},
		{"sess-1", "DEBUG", "agent", "msg2"},
		{"sess-1", "WARN", "tool", "msg3"},
		{"sess-2", "INFO", "agent", "other session"},
	} {
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg, session_id, subsystem) VALUES (?, ?, ?, ?, ?)`,
			ts, e.level, e.msg, e.session, e.subsystem,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	// All entries for sess-1.
	entries, err := QueryBySession(db, "sess-1", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("got %d entries, want 3", len(entries))
	}

	// Filter by level.
	entries, err = QueryBySession(db, "sess-1", "INFO", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("level filter: got %d entries, want 1", len(entries))
	}

	// Filter by subsystem.
	entries, err = QueryBySession(db, "sess-1", "", "tool", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("subsystem filter: got %d entries, want 1", len(entries))
	}

	// Limit.
	entries, err = QueryBySession(db, "sess-1", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("limit: got %d entries, want 2", len(entries))
	}

	// No results for unknown session.
	entries, err = QueryBySession(db, "sess-unknown", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("unknown session: got %d entries, want 0", len(entries))
	}
}

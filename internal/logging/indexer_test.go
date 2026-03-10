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

func TestPrune_RemovesOldDebugEntries(t *testing.T) {
	db := openTestDB(t)

	// Insert entries at various levels with timestamps.
	old := time.Now().Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)

	for _, e := range []struct {
		ts    string
		level string
		msg   string
	}{
		{old, "DEBUG", "old debug"},
		{old, "INFO", "old info"},
		{old, "ERROR", "old error"},
		{recent, "DEBUG", "recent debug"},
		{recent, "INFO", "recent info"},
	} {
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
			e.ts, e.level, e.msg,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Prune DEBUG entries older than 90 days.
	deleted, err := Prune(db, 90*24*time.Hour, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	// Verify remaining entries.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM log_entries`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Errorf("remaining = %d, want 4", count)
	}

	// Old INFO and ERROR should still be present.
	var oldInfoCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM log_entries WHERE level = 'INFO' AND timestamp = ?`, old,
	).Scan(&oldInfoCount); err != nil {
		t.Fatal(err)
	}
	if oldInfoCount != 1 {
		t.Errorf("old INFO entries = %d, want 1", oldInfoCount)
	}
}

func TestPrune_NothingToPrune(t *testing.T) {
	db := openTestDB(t)

	// Insert only INFO entries.
	_, err := db.Exec(
		`INSERT INTO log_entries (timestamp, level, msg) VALUES (?, ?, ?)`,
		time.Now().Add(-200*24*time.Hour).Format(time.RFC3339Nano), "INFO", "old info",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Prune DEBUG below INFO — should delete nothing.
	deleted, err := Prune(db, 90*24*time.Hour, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
}

// discardWriter is an io.Writer that discards all data.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

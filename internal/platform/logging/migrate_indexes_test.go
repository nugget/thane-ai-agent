package logging

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// expectedLogEntryIndexes is every index the schema declares for
// log_entries. Migrate must leave all of them present — a migration
// that reports success while declared indexes are missing is the
// silent failure mode behind the 2026-08 telemetry outage.
var expectedLogEntryIndexes = []string{
	"idx_log_timestamp",
	"idx_log_level",
	"idx_log_request",
	"idx_log_session",
	"idx_log_conversation",
	"idx_log_subsystem",
	"idx_log_tool",
	"idx_log_model",
	"idx_log_loop_id",
	"idx_log_loop_name",
}

// TestMigrateCreatesEveryDeclaredIndex pins the contract on a fresh
// database: after Migrate, every declared index exists.
func TestMigrateCreatesEveryDeclaredIndex(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	present := make(map[string]bool)
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='log_entries'`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		present[name] = true
	}
	var missing []string
	for _, want := range expectedLogEntryIndexes {
		if !present[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Errorf("Migrate reported success but these indexes are missing: %s", strings.Join(missing, ", "))
	}
}

// TestMigrateHealsLegacyShapedDatabase pins the self-heal contract: a
// database holding the table and only the newer indexes (the shape an
// affected production install exhibited) gains the missing indexes on
// the next Migrate.
func TestMigrateHealsLegacyShapedDatabase(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	legacy := `
	CREATE TABLE log_entries (
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
		loop_id TEXT,
		loop_name TEXT,
		source_file TEXT,
		source_line INTEGER,
		attrs TEXT,
		raw_file TEXT,
		raw_line INTEGER
	);
	CREATE INDEX idx_log_subsystem ON log_entries(subsystem);
	CREATE INDEX idx_log_tool ON log_entries(tool);
	CREATE INDEX idx_log_model ON log_entries(model);
	CREATE INDEX idx_log_loop_id ON log_entries(loop_id);
	CREATE INDEX idx_log_loop_name ON log_entries(loop_name);
	`
	for _, stmt := range strings.Split(legacy, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("legacy setup %q: %v", strings.TrimSpace(stmt)[:30], err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate over legacy shape: %v", err)
	}

	present := make(map[string]bool)
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='log_entries'`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		present[name] = true
	}
	var missing []string
	for _, want := range expectedLogEntryIndexes {
		if !present[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Errorf("legacy-shaped database not healed; missing: %s", strings.Join(missing, ", "))
	}
}

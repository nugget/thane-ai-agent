package database

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMigrateAppliesStepsInOrder(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	schema := Schema{
		Name: "test",
		Steps: []MigrationStep{
			TableCreate{
				Table: "items",
				SQL: `CREATE TABLE IF NOT EXISTS items (
					id TEXT PRIMARY KEY,
					name TEXT NOT NULL
				)`,
			},
			ColumnAdd{Table: "items", Column: "added_at", Typedef: "TEXT"},
			IndexCreate{
				Name: "idx_items_name",
				SQL:  `CREATE INDEX IF NOT EXISTS idx_items_name ON items(name)`,
			},
		},
	}

	if err := Migrate(db, schema, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if !HasColumn(db, "items", "added_at") {
		t.Error("ColumnAdd step did not run")
	}

	var indexCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_items_name'`,
	).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Errorf("IndexCreate step did not run: indexCount=%d", indexCount)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	schema := Schema{
		Name: "test",
		Steps: []MigrationStep{
			TableCreate{
				Table: "items",
				SQL:   `CREATE TABLE IF NOT EXISTS items (id TEXT PRIMARY KEY)`,
			},
			ColumnAdd{Table: "items", Column: "name", Typedef: "TEXT"},
		},
	}

	if err := Migrate(db, schema, discardLogger()); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := Migrate(db, schema, discardLogger()); err != nil {
		t.Errorf("second Migrate should be no-op, got: %v", err)
	}
}

func TestMigrateHaltsOnFirstError(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	schema := Schema{
		Name: "broken",
		Steps: []MigrationStep{
			TableCreate{Table: "ok", SQL: `CREATE TABLE IF NOT EXISTS ok (id TEXT PRIMARY KEY)`},
			Raw{Description: "broken", SQL: `THIS IS NOT SQL`},
			TableCreate{Table: "after", SQL: `CREATE TABLE IF NOT EXISTS after (id TEXT PRIMARY KEY)`},
		},
	}

	err = Migrate(db, schema, discardLogger())
	if err == nil {
		t.Fatal("expected error from broken step")
	}
	if !strings.Contains(err.Error(), "broken migrate") {
		t.Errorf("error should be wrapped with schema name, got: %v", err)
	}

	// The "after" table must not have been created — Migrate halts on
	// the first failure.
	var n int
	row := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='after'`)
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("Migrate should not have continued past the failing step")
	}
}

func TestMigrateNilLoggerUsesDefault(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	schema := Schema{
		Name: "nil-logger",
		Steps: []MigrationStep{
			TableCreate{Table: "x", SQL: `CREATE TABLE IF NOT EXISTS x (id TEXT PRIMARY KEY)`},
		},
	}
	if err := Migrate(db, schema, nil); err != nil {
		t.Errorf("nil logger should not break Migrate: %v", err)
	}
}

func TestStepDescribe(t *testing.T) {
	tests := []struct {
		step MigrationStep
		want string
	}{
		{TableCreate{Table: "users"}, "table users"},
		{ColumnAdd{Table: "users", Column: "email"}, "column users.email"},
		{IndexCreate{Name: "idx_users_email"}, "index idx_users_email"},
		{Raw{Description: "rebuild fts"}, "rebuild fts"},
	}
	for _, tc := range tests {
		if got := tc.step.Describe(); got != tc.want {
			t.Errorf("Describe = %q, want %q", got, tc.want)
		}
	}
}

func TestRawApplyError(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	step := Raw{Description: "bad", SQL: `NOT VALID SQL`}
	err = step.Apply(db)
	if err == nil {
		t.Fatal("expected error")
	}
	// Description should be wrapped into the error so failed migrations
	// are easy to diagnose.
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should mention description, got: %v", err)
	}
}

func TestMigrateRejectsNilDB(t *testing.T) {
	schema := Schema{
		Name: "guarded",
		Steps: []MigrationStep{
			TableCreate{Table: "x", SQL: `CREATE TABLE IF NOT EXISTS x (id TEXT PRIMARY KEY)`},
		},
	}
	err := Migrate(nil, schema, discardLogger())
	if err == nil {
		t.Fatal("expected error for nil db")
	}
	if !strings.Contains(err.Error(), "guarded") || !strings.Contains(err.Error(), "nil database") {
		t.Errorf("error should name the schema and mention nil db, got: %v", err)
	}
}

func TestMigrateRejectsEmptyName(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	err = Migrate(db, Schema{Steps: nil}, discardLogger())
	if err == nil {
		t.Fatal("expected error for empty schema name")
	}
	if !strings.Contains(err.Error(), "schema name") {
		t.Errorf("error should mention schema name, got: %v", err)
	}
}

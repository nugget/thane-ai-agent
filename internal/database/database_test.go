package database

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestHasColumn(t *testing.T) {
	db := openTestDB(t)

	tests := []struct {
		name   string
		table  string
		column string
		want   bool
	}{
		{"existing column", "test", "name", true},
		{"missing column", "test", "missing", false},
		{"nonexistent table", "nope", "id", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasColumn(db, tc.table, tc.column); got != tc.want {
				t.Errorf("HasColumn(%q, %q) = %v, want %v", tc.table, tc.column, got, tc.want)
			}
		})
	}
}

func TestAddColumn(t *testing.T) {
	t.Run("new column", func(t *testing.T) {
		db := openTestDB(t)
		if err := AddColumn(db, "test", "age", "INTEGER DEFAULT 0"); err != nil {
			t.Fatalf("AddColumn: %v", err)
		}
		if !HasColumn(db, "test", "age") {
			t.Error("column not added")
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		db := openTestDB(t)
		if err := AddColumn(db, "test", "age", "INTEGER"); err != nil {
			t.Fatal(err)
		}
		if err := AddColumn(db, "test", "age", "INTEGER"); err != nil {
			t.Errorf("second AddColumn should be no-op, got: %v", err)
		}
	})

	t.Run("nonexistent table", func(t *testing.T) {
		db := openTestDB(t)
		if err := AddColumn(db, "nope", "col", "TEXT"); err == nil {
			t.Error("expected error for nonexistent table")
		}
	})
}

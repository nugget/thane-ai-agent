// Package database provides shared SQLite helpers for schema migration
// and connection management. All stores that open or migrate SQLite
// databases should use these helpers for consistency.
package database

import (
	"database/sql"
	"fmt"
)

// Open opens a SQLite database at the given path with standard
// production settings: WAL journal mode and a 5-second busy timeout.
// Callers must ensure the github.com/mattn/go-sqlite3 driver is
// registered (typically via a blank import in the binary's main
// package or test file).
func Open(path string) (*sql.DB, error) {
	return sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
}

// HasColumn reports whether the given table contains a column with the
// specified name. It uses a lightweight SELECT probe that avoids
// scanning any rows. Identifiers are left unquoted because SQLite
// treats double-quoted unknown identifiers as string literals, which
// would make the probe always succeed.
func HasColumn(db *sql.DB, table, column string) bool {
	_, err := db.Exec("SELECT " + column + " FROM " + table + " LIMIT 0")
	return err == nil
}

// AddColumn idempotently adds a column to an existing table. If the
// column already exists the call is a no-op. The typedef parameter is
// the SQL type and optional constraints (e.g. "TEXT NOT NULL DEFAULT ”").
func AddColumn(db *sql.DB, table, column, typedef string) error {
	if HasColumn(db, table, column) {
		return nil
	}
	_, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, typedef))
	if err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

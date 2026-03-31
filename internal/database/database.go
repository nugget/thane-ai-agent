// Package database provides shared SQLite helpers for schema migration
// and connection management. All stores that open or migrate SQLite
// databases should use these helpers for consistency.
package database

import (
	"database/sql"
	"fmt"
	"sync/atomic"
)

// Open opens a SQLite database at the given path with standard
// production settings: WAL journal mode and a 5-second busy timeout.
// Callers must ensure the github.com/mattn/go-sqlite3 driver is
// registered (typically via a blank import in the binary's main
// package or test file).
func Open(path string) (*sql.DB, error) {
	return sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
}

// memoryDBSeq generates unique names for in-memory test databases.
var memoryDBSeq uint64

// OpenMemory opens an isolated shared-cache in-memory SQLite database
// suitable for tests. Each call gets a unique database name so parallel
// test packages cannot contaminate each other. MaxOpenConns and
// MaxIdleConns are set to 1 to prevent the pool from dropping the last
// connection and silently losing the in-memory state.
func OpenMemory() (*sql.DB, error) {
	id := atomic.AddUint64(&memoryDBSeq, 1)
	dsn := fmt.Sprintf("file:thane_test_%d?mode=memory&cache=shared&_busy_timeout=5000", id)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	return db, nil
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
// the SQL type and optional constraints, for example "TEXT" or
// "INTEGER DEFAULT 0".
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

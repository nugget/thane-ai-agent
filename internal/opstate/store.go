// Package opstate provides a namespaced key-value store for persistent
// operational state. It is intended for lightweight data that needs to
// survive restarts — poller high-water marks, feature toggles, session
// preferences — not for structured domain data that deserves its own
// schema (contacts, conversations, facts). Those get their own stores.
package opstate

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store is a namespaced key-value store backed by SQLite. All public
// methods are safe for concurrent use (SQLite serializes writes).
type Store struct {
	db *sql.DB
}

// NewStore creates an operational state store at the given database path.
// The schema is created automatically on first use.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS operational_state (
		namespace  TEXT NOT NULL,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (namespace, key)
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

// Get returns the stored value for a namespace/key pair. Returns empty
// string and nil error if the key does not exist.
func (s *Store) Get(namespace, key string) (string, error) {
	var value string
	err := s.db.QueryRow(
		`SELECT value FROM operational_state WHERE namespace = ? AND key = ?`,
		namespace, key,
	).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get %s/%s: %w", namespace, key, err)
	}
	return value, nil
}

// Set upserts a namespace/key/value triple. Existing values are
// overwritten and the updated_at timestamp is refreshed.
func (s *Store) Set(namespace, key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO operational_state (namespace, key, value, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (namespace, key) DO UPDATE
		 SET value = excluded.value, updated_at = excluded.updated_at`,
		namespace, key, value, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("set %s/%s: %w", namespace, key, err)
	}
	return nil
}

// Delete removes a namespace/key entry. No error is returned if the
// key does not exist.
func (s *Store) Delete(namespace, key string) error {
	_, err := s.db.Exec(
		`DELETE FROM operational_state WHERE namespace = ? AND key = ?`,
		namespace, key,
	)
	if err != nil {
		return fmt.Errorf("delete %s/%s: %w", namespace, key, err)
	}
	return nil
}

// DeleteNamespace removes all entries for a namespace. No error is
// returned if the namespace has no entries.
func (s *Store) DeleteNamespace(namespace string) error {
	_, err := s.db.Exec(
		`DELETE FROM operational_state WHERE namespace = ?`,
		namespace,
	)
	if err != nil {
		return fmt.Errorf("delete namespace %s: %w", namespace, err)
	}
	return nil
}

// List returns all key/value pairs for a namespace. Returns an empty
// (non-nil) map if the namespace has no entries.
func (s *Store) List(namespace string) (map[string]string, error) {
	rows, err := s.db.Query(
		`SELECT key, value FROM operational_state WHERE namespace = ? ORDER BY key`,
		namespace,
	)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", namespace, err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan %s: %w", namespace, err)
		}
		result[k] = v
	}
	return result, rows.Err()
}

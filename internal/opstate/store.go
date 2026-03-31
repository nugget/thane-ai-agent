// Package opstate provides a namespaced key-value store for persistent
// operational state. It is intended for lightweight data that needs to
// survive restarts — poller high-water marks, feature toggles, session
// preferences — not for structured domain data that deserves its own
// schema (contacts, conversations, facts). Those get their own stores.
package opstate

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/database"
)

// Store is a namespaced key-value store backed by SQLite. All public
// methods are safe for concurrent use (SQLite serializes writes).
type Store struct {
	db *sql.DB
}

// NewStore creates an operational state store using the given database
// connection. The caller owns the connection — Store does not close it.
// The schema is created automatically on first use.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("nil database connection")
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
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
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Add expires_at column for TTL support (issue #457).
	return database.AddColumn(s.db, "operational_state", "expires_at", "TEXT")
}

// Get returns the stored value for a namespace/key pair. Returns empty
// string and nil error if the key does not exist or has expired.
func (s *Store) Get(namespace, key string) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var value string
	err := s.db.QueryRow(
		`SELECT value FROM operational_state
		 WHERE namespace = ? AND key = ?
		   AND (expires_at IS NULL OR expires_at > ?)`,
		namespace, key, now,
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
// overwritten and the updated_at timestamp is refreshed. The key
// never expires (expires_at is cleared on upsert).
func (s *Store) Set(namespace, key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO operational_state (namespace, key, value, updated_at, expires_at)
		 VALUES (?, ?, ?, ?, NULL)
		 ON CONFLICT (namespace, key) DO UPDATE
		 SET value = excluded.value, updated_at = excluded.updated_at, expires_at = NULL`,
		namespace, key, value, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("set %s/%s: %w", namespace, key, err)
	}
	return nil
}

// SetWithTTL upserts a namespace/key/value triple that expires after
// the given duration. Expired keys are filtered out by [Get] and
// [List] and periodically removed by [DeleteExpired].
func (s *Store) SetWithTTL(namespace, key, value string, ttl time.Duration) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO operational_state (namespace, key, value, updated_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (namespace, key) DO UPDATE
		 SET value = excluded.value, updated_at = excluded.updated_at, expires_at = excluded.expires_at`,
		namespace, key, value,
		now.Format(time.RFC3339),
		now.Add(ttl).Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("set %s/%s (ttl=%s): %w", namespace, key, ttl, err)
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

// List returns all non-expired key/value pairs for a namespace.
// Returns an empty (non-nil) map if the namespace has no live entries.
func (s *Store) List(namespace string) (map[string]string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.db.Query(
		`SELECT key, value FROM operational_state
		 WHERE namespace = ?
		   AND (expires_at IS NULL OR expires_at > ?)
		 ORDER BY key`,
		namespace, now,
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

// DeleteExpired removes all rows whose expires_at timestamp has passed.
// Returns the number of rows deleted. This is best-effort cleanup —
// expired rows are already invisible to [Get] and [List]. The context
// allows callers to bound execution time or cancel during shutdown.
func (s *Store) DeleteExpired(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM operational_state WHERE expires_at IS NOT NULL AND expires_at <= ?`,
		now,
	)
	if err != nil {
		return 0, fmt.Errorf("delete expired: %w", err)
	}
	return result.RowsAffected()
}

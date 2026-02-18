// Package watchlist manages a dynamic set of Home Assistant entities whose
// state is injected into the agent's system prompt each turn. Entities are
// persisted in SQLite so the watchlist survives restarts.
package watchlist

import (
	"database/sql"
	"fmt"
)

// Store persists the set of watched entity IDs in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore creates a watchlist store, running migrations on first use.
func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate watchlist: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS watched_entities (
			entity_id TEXT PRIMARY KEY,
			added_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

// Add inserts an entity into the watchlist. Duplicates are silently ignored.
func (s *Store) Add(entityID string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO watched_entities (entity_id) VALUES (?)`,
		entityID,
	)
	return err
}

// Remove deletes an entity from the watchlist. Non-existent IDs are a no-op.
func (s *Store) Remove(entityID string) error {
	_, err := s.db.Exec(
		`DELETE FROM watched_entities WHERE entity_id = ?`,
		entityID,
	)
	return err
}

// List returns all watched entity IDs in insertion order.
func (s *Store) List() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT entity_id FROM watched_entities ORDER BY added_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

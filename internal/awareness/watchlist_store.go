package awareness

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// WatchedEntity represents a single watchlist entry with its options.
type WatchedEntity struct {
	EntityID string
	Tags     []string // capability tags — empty means always visible
	History  []int    // delta offsets in seconds (e.g., [600, 3600, 86400])
}

// WatchlistStore persists the set of watched entity IDs in SQLite.
type WatchlistStore struct {
	db *sql.DB
}

// NewWatchlistStore creates a watchlist store, running migrations on first use.
func NewWatchlistStore(db *sql.DB) (*WatchlistStore, error) {
	s := &WatchlistStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate watchlist: %w", err)
	}
	return s, nil
}

func (s *WatchlistStore) migrate() error {
	// Create table if it doesn't exist.
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS watched_entities (
			entity_id TEXT PRIMARY KEY,
			added_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			tags      TEXT NOT NULL DEFAULT '',
			options   TEXT NOT NULL DEFAULT '{}'
		)
	`)
	if err != nil {
		return err
	}

	// Add columns for existing databases (idempotent). Only the
	// "duplicate column" error is expected and ignored; other ALTER
	// failures are surfaced.
	for _, col := range []struct{ name, def string }{
		{"tags", "TEXT NOT NULL DEFAULT ''"},
		{"options", "TEXT NOT NULL DEFAULT '{}'"},
	} {
		_, err = s.db.Exec(fmt.Sprintf(
			`ALTER TABLE watched_entities ADD COLUMN %s %s`, col.name, col.def,
		))
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("alter watched_entities add column %s: %w", col.name, err)
		}
	}

	return nil
}

// Add inserts an entity into the watchlist with no tags or options.
// Duplicates are silently ignored. Use [AddWithOptions] for richer
// subscriptions.
func (s *WatchlistStore) Add(entityID string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO watched_entities (entity_id) VALUES (?)`,
		entityID,
	)
	return err
}

// AddWithOptions inserts or updates an entity with tags and history
// offsets. On conflict (duplicate entity_id), the tags and options are
// updated to the new values.
func (s *WatchlistStore) AddWithOptions(entityID string, tags []string, history []int) error {
	tagsStr := strings.Join(tags, ",")

	opts := map[string]any{}
	if len(history) > 0 {
		opts["history"] = history
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return fmt.Errorf("marshal options: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO watched_entities (entity_id, tags, options)
		VALUES (?, ?, ?)
		ON CONFLICT(entity_id) DO UPDATE SET tags = excluded.tags, options = excluded.options
	`, entityID, tagsStr, string(optsJSON))
	return err
}

// Remove deletes an entity from the watchlist. Non-existent IDs are a no-op.
func (s *WatchlistStore) Remove(entityID string) error {
	_, err := s.db.Exec(
		`DELETE FROM watched_entities WHERE entity_id = ?`,
		entityID,
	)
	return err
}

// List returns all watched entity IDs in insertion order (unfiltered).
func (s *WatchlistStore) List() ([]string, error) {
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

// ListUntagged returns watched entities that have no capability tags
// (always visible in context regardless of active tags).
func (s *WatchlistStore) ListUntagged() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT entity_id FROM watched_entities WHERE tags = '' ORDER BY added_at ASC`,
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

// ListByTag returns watched entities that include the given capability
// tag. Used by the tag context assembler to inject entity context only
// when a tag is active.
func (s *WatchlistStore) ListByTag(tag string) ([]WatchedEntity, error) {
	// SQLite doesn't have array types, so tags are stored as
	// comma-separated values. We match with LIKE for single-tag
	// queries and post-filter for exactness.
	rows, err := s.db.Query(
		`SELECT entity_id, tags, options FROM watched_entities
		 WHERE tags LIKE ? ORDER BY added_at ASC`,
		"%"+tag+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entities []WatchedEntity
	for rows.Next() {
		var id, tagsStr, optsJSON string
		if err := rows.Scan(&id, &tagsStr, &optsJSON); err != nil {
			return nil, err
		}
		tags := splitTags(tagsStr)
		if !containsTag(tags, tag) {
			continue // LIKE matched a substring, not the exact tag
		}
		entities = append(entities, WatchedEntity{
			EntityID: id,
			Tags:     tags,
			History:  parseHistory(optsJSON),
		})
	}
	return entities, rows.Err()
}

// DistinctTags returns all unique tags across all watched entities.
// Used at startup to register tag-scoped watchlist providers.
func (s *WatchlistStore) DistinctTags() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT tags FROM watched_entities WHERE tags != '' ORDER BY tags ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)
	for rows.Next() {
		var tagsStr string
		if err := rows.Scan(&tagsStr); err != nil {
			return nil, err
		}
		for _, tag := range splitTags(tagsStr) {
			seen[tag] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags, nil
}

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	var tags []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func parseHistory(optsJSON string) []int {
	if optsJSON == "" || optsJSON == "{}" {
		return nil
	}
	var opts struct {
		History []int `json:"history"`
	}
	if err := json.Unmarshal([]byte(optsJSON), &opts); err != nil {
		return nil
	}
	return opts.History
}

// Package facts provides long-term memory storage for learned information.
package facts

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Category groups related facts.
type Category string

const (
	CategoryUser       Category = "user"       // User preferences and info
	CategoryHome       Category = "home"       // Home layout, room names
	CategoryDevice     Category = "device"     // Device mappings and traits
	CategoryRoutine    Category = "routine"    // Observed patterns
	CategoryPreference Category = "preference" // How the user likes things
)

// Fact represents a piece of long-term memory.
type Fact struct {
	ID         uuid.UUID `json:"id"`
	Category   Category  `json:"category"`
	Key        string    `json:"key"`                  // Unique within category
	Value      string    `json:"value"`                // The actual information
	Source     string    `json:"source,omitempty"`     // Where we learned this
	Confidence float64   `json:"confidence,omitempty"` // 0-1, how certain
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	AccessedAt time.Time `json:"accessed_at"` // For LRU-style relevance
}

// Store manages fact persistence.
type Store struct {
	db *sql.DB
}

// NewStore creates a fact store using the given database path.
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

// NewStoreWithDB creates a fact store using an existing database connection.
func NewStoreWithDB(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS facts (
			id TEXT PRIMARY KEY,
			category TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			source TEXT,
			confidence REAL DEFAULT 1.0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			accessed_at TEXT NOT NULL,
			UNIQUE(category, key)
		);

		CREATE INDEX IF NOT EXISTS idx_facts_category ON facts(category);
		CREATE INDEX IF NOT EXISTS idx_facts_key ON facts(key);
		CREATE INDEX IF NOT EXISTS idx_facts_accessed ON facts(accessed_at DESC);
	`)
	return err
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Set creates or updates a fact.
func (s *Store) Set(category Category, key, value, source string, confidence float64) (*Fact, error) {
	now := time.Now().UTC()

	// Check if exists
	var existingID string
	err := s.db.QueryRow(`SELECT id FROM facts WHERE category = ? AND key = ?`, category, key).Scan(&existingID)

	if err == sql.ErrNoRows {
		// Create new
		id, _ := uuid.NewV7()
		fact := &Fact{
			ID:         id,
			Category:   category,
			Key:        key,
			Value:      value,
			Source:     source,
			Confidence: confidence,
			CreatedAt:  now,
			UpdatedAt:  now,
			AccessedAt: now,
		}

		_, err = s.db.Exec(`
			INSERT INTO facts (id, category, key, value, source, confidence, created_at, updated_at, accessed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id.String(), category, key, value, source, confidence,
			now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339))
		if err != nil {
			return nil, fmt.Errorf("insert: %w", err)
		}
		return fact, nil
	} else if err != nil {
		return nil, fmt.Errorf("check existing: %w", err)
	}

	// Update existing
	_, err = s.db.Exec(`
		UPDATE facts SET value = ?, source = ?, confidence = ?, updated_at = ?, accessed_at = ?
		WHERE category = ? AND key = ?
	`, value, source, confidence, now.Format(time.RFC3339), now.Format(time.RFC3339), category, key)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}

	id, _ := uuid.Parse(existingID)
	return &Fact{
		ID:         id,
		Category:   category,
		Key:        key,
		Value:      value,
		Source:     source,
		Confidence: confidence,
		UpdatedAt:  now,
		AccessedAt: now,
	}, nil
}

// Get retrieves a fact by category and key.
func (s *Store) Get(category Category, key string) (*Fact, error) {
	fact, err := s.scanFact(s.db.QueryRow(`
		SELECT id, category, key, value, source, confidence, created_at, updated_at, accessed_at
		FROM facts WHERE category = ? AND key = ?
	`, category, key))
	if err != nil {
		return nil, err
	}

	// Update accessed_at
	now := time.Now().UTC()
	_, _ = s.db.Exec(`UPDATE facts SET accessed_at = ? WHERE id = ?`, now.Format(time.RFC3339), fact.ID.String())
	fact.AccessedAt = now

	return fact, nil
}

// GetByCategory retrieves all facts in a category.
func (s *Store) GetByCategory(category Category) ([]*Fact, error) {
	rows, err := s.db.Query(`
		SELECT id, category, key, value, source, confidence, created_at, updated_at, accessed_at
		FROM facts WHERE category = ? ORDER BY key
	`, category)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var facts []*Fact
	for rows.Next() {
		fact, err := s.scanFactRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

// GetAll retrieves all facts.
func (s *Store) GetAll() ([]*Fact, error) {
	rows, err := s.db.Query(`
		SELECT id, category, key, value, source, confidence, created_at, updated_at, accessed_at
		FROM facts ORDER BY category, key
	`)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var facts []*Fact
	for rows.Next() {
		fact, err := s.scanFactRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

// Search finds facts containing the query in key or value.
func (s *Store) Search(query string) ([]*Fact, error) {
	pattern := "%" + query + "%"
	rows, err := s.db.Query(`
		SELECT id, category, key, value, source, confidence, created_at, updated_at, accessed_at
		FROM facts 
		WHERE key LIKE ? OR value LIKE ?
		ORDER BY accessed_at DESC
		LIMIT 50
	`, pattern, pattern)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var facts []*Fact
	for rows.Next() {
		fact, err := s.scanFactRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

// Delete removes a fact.
func (s *Store) Delete(category Category, key string) error {
	result, err := s.db.Exec(`DELETE FROM facts WHERE category = ? AND key = ?`, category, key)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("fact not found: %s/%s", category, key)
	}
	return nil
}

// Stats returns fact statistics.
func (s *Store) Stats() map[string]any {
	var total int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM facts`).Scan(&total)

	// Count by category
	cats := make(map[string]int)
	rows, _ := s.db.Query(`SELECT category, COUNT(*) FROM facts GROUP BY category`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var cat string
			var count int
			if err := rows.Scan(&cat, &count); err != nil {
				continue
			}
			cats[cat] = count
		}
	}

	return map[string]any{
		"total":      total,
		"categories": cats,
	}
}

func (s *Store) scanFact(row *sql.Row) (*Fact, error) {
	var f Fact
	var idStr, catStr, createdStr, updatedStr, accessedStr string
	var source sql.NullString

	err := row.Scan(&idStr, &catStr, &f.Key, &f.Value, &source, &f.Confidence, &createdStr, &updatedStr, &accessedStr)
	if err != nil {
		return nil, err
	}

	f.ID, _ = uuid.Parse(idStr)
	f.Category = Category(catStr)
	if source.Valid {
		f.Source = source.String
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	f.AccessedAt, _ = time.Parse(time.RFC3339, accessedStr)

	return &f, nil
}

func (s *Store) scanFactRow(rows *sql.Rows) (*Fact, error) {
	var f Fact
	var idStr, catStr, createdStr, updatedStr, accessedStr string
	var source sql.NullString

	err := rows.Scan(&idStr, &catStr, &f.Key, &f.Value, &source, &f.Confidence, &createdStr, &updatedStr, &accessedStr)
	if err != nil {
		return nil, err
	}

	f.ID, _ = uuid.Parse(idStr)
	f.Category = Category(catStr)
	if source.Valid {
		f.Source = source.String
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	f.AccessedAt, _ = time.Parse(time.RFC3339, accessedStr)

	return &f, nil
}

// Package facts provides long-term memory storage for learned information.
package facts

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// Category groups related facts.
type Category string

const (
	CategoryUser         Category = "user"         // User preferences and info
	CategoryHome         Category = "home"         // Home layout, room names
	CategoryDevice       Category = "device"       // Device mappings and traits
	CategoryRoutine      Category = "routine"      // Observed patterns
	CategoryPreference   Category = "preference"   // How the user likes things
	CategoryArchitecture Category = "architecture" // System design knowledge
)

// SQL fragments for query building.
const (
	// Base columns for fact queries (without embedding).
	factColumns = "id, category, key, value, source, confidence, created_at, updated_at, accessed_at"
	// Columns including embedding.
	factColumnsWithEmbed = "id, category, key, value, source, confidence, embedding, created_at, updated_at, accessed_at"
	// Standard filter for active (non-deleted) facts.
	notDeleted = "deleted_at IS NULL"
)

// Fact represents a piece of long-term memory.
type Fact struct {
	ID         uuid.UUID `json:"id"`
	Category   Category  `json:"category"`
	Key        string    `json:"key"`                  // Unique within category
	Value      string    `json:"value"`                // The actual information
	Source     string    `json:"source,omitempty"`     // Where we learned this
	Confidence float64   `json:"confidence,omitempty"` // 0-1, how certain
	Embedding  []float32 `json:"embedding,omitempty"`  // Vector embedding for semantic search
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
			embedding BLOB,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			accessed_at TEXT NOT NULL,
			deleted_at TEXT,
			UNIQUE(category, key)
		);

		CREATE INDEX IF NOT EXISTS idx_facts_category ON facts(category);
		CREATE INDEX IF NOT EXISTS idx_facts_key ON facts(key);
		CREATE INDEX IF NOT EXISTS idx_facts_accessed ON facts(accessed_at DESC);
		CREATE INDEX IF NOT EXISTS idx_facts_deleted ON facts(deleted_at);
	`)
	if err != nil {
		return err
	}

	// Add columns if they don't exist (migrations for existing DBs)
	_, _ = s.db.Exec(`ALTER TABLE facts ADD COLUMN embedding BLOB`)
	_, _ = s.db.Exec(`ALTER TABLE facts ADD COLUMN deleted_at TEXT`)

	return nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Set creates or updates a fact. Resurrects soft-deleted facts if they exist.
func (s *Store) Set(category Category, key, value, source string, confidence float64) (*Fact, error) {
	now := time.Now().UTC()

	// Check if exists (including soft-deleted)
	var existingID string
	var isDeleted bool
	err := s.db.QueryRow(`SELECT id, deleted_at IS NOT NULL FROM facts WHERE category = ? AND key = ?`, category, key).Scan(&existingID, &isDeleted)

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

	// Update existing (resurrect if soft-deleted)
	_, err = s.db.Exec(`
		UPDATE facts SET value = ?, source = ?, confidence = ?, updated_at = ?, accessed_at = ?, deleted_at = NULL
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
	fact, err := s.scanFact(s.db.QueryRow(
		`SELECT `+factColumns+` FROM facts WHERE `+notDeleted+` AND category = ? AND key = ?`,
		category, key))
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
	rows, err := s.db.Query(
		`SELECT `+factColumns+` FROM facts WHERE `+notDeleted+` AND category = ? ORDER BY key`,
		category)
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
	rows, err := s.db.Query(
		`SELECT ` + factColumns + ` FROM facts WHERE ` + notDeleted + ` ORDER BY category, key`)
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
	rows, err := s.db.Query(
		`SELECT `+factColumns+` FROM facts WHERE `+notDeleted+` AND (key LIKE ? OR value LIKE ?) ORDER BY accessed_at DESC LIMIT 50`,
		pattern, pattern)
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

// Delete soft-deletes a fact (sets deleted_at timestamp).
func (s *Store) Delete(category Category, key string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.Exec(`UPDATE facts SET deleted_at = ? WHERE category = ? AND key = ? AND deleted_at IS NULL`, now, category, key)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("fact not found: %s/%s", category, key)
	}
	return nil
}

// DeleteBySource soft-deletes all facts from a given source.
// Used for re-importing documents without duplicates.
func (s *Store) DeleteBySource(source string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`UPDATE facts SET deleted_at = ? WHERE source = ? AND deleted_at IS NULL`, now, source)
	return err
}

// Stats returns fact statistics.
func (s *Store) Stats() map[string]any {
	var total int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM facts WHERE ` + notDeleted).Scan(&total)

	// Count by category
	cats := make(map[string]int)
	rows, _ := s.db.Query(`SELECT category, COUNT(*) FROM facts WHERE ` + notDeleted + ` GROUP BY category`)
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

// SetEmbedding updates a fact's embedding vector.
func (s *Store) SetEmbedding(id uuid.UUID, embedding []float32) error {
	blob := encodeEmbedding(embedding)
	_, err := s.db.Exec(`UPDATE facts SET embedding = ? WHERE id = ?`, blob, id.String())
	return err
}

// GetAllWithEmbeddings returns all facts that have embeddings.
func (s *Store) GetAllWithEmbeddings() ([]*Fact, error) {
	rows, err := s.db.Query(
		`SELECT ` + factColumnsWithEmbed + ` FROM facts WHERE ` + notDeleted + ` AND embedding IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []*Fact
	for rows.Next() {
		f, err := s.scanFactWithEmbedding(rows)
		if err != nil {
			continue
		}
		facts = append(facts, f)
	}
	return facts, nil
}

// SemanticSearch finds facts similar to the query embedding.
func (s *Store) SemanticSearch(queryEmbedding []float32, limit int) ([]*Fact, []float32, error) {
	facts, err := s.GetAllWithEmbeddings()
	if err != nil {
		return nil, nil, err
	}

	if len(facts) == 0 {
		return nil, nil, nil
	}

	// Calculate similarities
	type scored struct {
		fact  *Fact
		score float32
	}
	scores := make([]scored, 0, len(facts))
	for _, f := range facts {
		if len(f.Embedding) > 0 {
			sim := cosineSimilarity(queryEmbedding, f.Embedding)
			scores = append(scores, scored{fact: f, score: sim})
		}
	}

	// Sort by score descending (simple selection sort)
	for i := 0; i < limit && i < len(scores); i++ {
		maxIdx := i
		for j := i + 1; j < len(scores); j++ {
			if scores[j].score > scores[maxIdx].score {
				maxIdx = j
			}
		}
		scores[i], scores[maxIdx] = scores[maxIdx], scores[i]
	}

	// Return top k
	resultFacts := make([]*Fact, 0, limit)
	resultScores := make([]float32, 0, limit)
	for i := 0; i < limit && i < len(scores); i++ {
		resultFacts = append(resultFacts, scores[i].fact)
		resultScores = append(resultScores, scores[i].score)
	}

	return resultFacts, resultScores, nil
}

// GetFactsWithoutEmbeddings returns facts that need embeddings generated.
func (s *Store) GetFactsWithoutEmbeddings() ([]*Fact, error) {
	rows, err := s.db.Query(
		`SELECT ` + factColumns + ` FROM facts WHERE ` + notDeleted + ` AND embedding IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []*Fact
	for rows.Next() {
		f, err := s.scanFactRow(rows)
		if err != nil {
			continue
		}
		facts = append(facts, f)
	}
	return facts, nil
}

func (s *Store) scanFactWithEmbedding(rows *sql.Rows) (*Fact, error) {
	var f Fact
	var idStr, catStr, createdStr, updatedStr, accessedStr string
	var source sql.NullString
	var embeddingBlob []byte

	err := rows.Scan(&idStr, &catStr, &f.Key, &f.Value, &source, &f.Confidence, &embeddingBlob, &createdStr, &updatedStr, &accessedStr)
	if err != nil {
		return nil, err
	}

	f.ID, _ = uuid.Parse(idStr)
	f.Category = Category(catStr)
	if source.Valid {
		f.Source = source.String
	}
	f.Embedding = decodeEmbedding(embeddingBlob)
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	f.AccessedAt, _ = time.Parse(time.RFC3339, accessedStr)

	return &f, nil
}

// encodeEmbedding converts float32 slice to bytes for storage.
func encodeEmbedding(embedding []float32) []byte {
	if len(embedding) == 0 {
		return nil
	}
	buf := make([]byte, len(embedding)*4)
	for i, v := range embedding {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// decodeEmbedding converts bytes back to float32 slice.
func decodeEmbedding(data []byte) []float32 {
	if len(data) == 0 {
		return nil
	}
	result := make([]float32, len(data)/4)
	for i := range result {
		result[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return result
}

// cosineSimilarity computes similarity between two vectors.
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, normA, normB float32
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

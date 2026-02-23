// Package facts provides long-term memory storage for learned information.
package facts

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
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
	factColumns = "id, category, key, value, source, confidence, subjects, created_at, updated_at, accessed_at, ref"
	// Columns including embedding.
	factColumnsWithEmbed = "id, category, key, value, source, confidence, subjects, embedding, created_at, updated_at, accessed_at, ref"
	// Qualified columns for FTS5 JOIN queries where facts and facts_fts
	// share column names (key, value, source). Without table prefixes,
	// SQLite raises "ambiguous column name" errors.
	factColumnsFTS = "facts.id, facts.category, facts.key, facts.value, facts.source, facts.confidence, facts.subjects, facts.created_at, facts.updated_at, facts.accessed_at, facts.ref"
	// Filter for active facts (currently: not soft-deleted).
	activeFilter = "deleted_at IS NULL"
)

// Fact represents a piece of long-term memory.
type Fact struct {
	ID         uuid.UUID `json:"id"`
	Category   Category  `json:"category"`
	Key        string    `json:"key"`                  // Unique within category
	Value      string    `json:"value"`                // The actual information
	Source     string    `json:"source,omitempty"`     // Where we learned this
	Confidence float64   `json:"confidence,omitempty"` // 0-1, how certain
	Subjects   []string  `json:"subjects,omitempty"`   // Subject keys (e.g., "entity:foo", "zone:bar")
	Ref        string    `json:"ref,omitempty"`        // Knowledge base relative path (e.g., "dossiers/openclawssy.md")
	Embedding  []float32 `json:"embedding,omitempty"`  // Vector embedding for semantic search
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	AccessedAt time.Time `json:"accessed_at"` // For LRU-style relevance
}

// Store manages fact persistence.
type Store struct {
	db         *sql.DB
	ftsEnabled bool
	logger     *slog.Logger
}

// NewStore creates a fact store using the given database path.
func NewStore(dbPath string, logger *slog.Logger) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	s := &Store{db: db, logger: logger}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

// NewStoreWithDB creates a fact store using an existing database connection.
func NewStoreWithDB(db *sql.DB, logger *slog.Logger) (*Store, error) {
	s := &Store{db: db, logger: logger}
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
	_, _ = s.db.Exec(`ALTER TABLE facts ADD COLUMN subjects TEXT`)
	_, _ = s.db.Exec(`ALTER TABLE facts ADD COLUMN ref TEXT`)

	s.tryEnableFTS()

	return nil
}

// tryEnableFTS creates the FTS5 virtual table for full-text search.
// If FTS5 is not available (e.g., compiled without it), the store
// falls back to LIKE-based search gracefully.
func (s *Store) tryEnableFTS() {
	_, err := s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(
			key,
			value,
			source,
			content=facts,
			content_rowid=rowid
		)
	`)
	if err != nil {
		s.logger.Warn("FTS5 not available for facts, using LIKE fallback", "error", err)
		return
	}
	s.ftsEnabled = true

	// Populate FTS index from existing data.
	// This is idempotent — rebuilding is safe on every startup.
	_, err = s.db.Exec(`INSERT INTO facts_fts(facts_fts) VALUES('rebuild')`)
	if err != nil {
		s.logger.Warn("failed to rebuild facts FTS index", "error", err)
		s.ftsEnabled = false
	}
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Set creates or updates a fact. Resurrects soft-deleted facts if they exist.
// Subjects is an optional list of subject keys (e.g., "entity:foo",
// "zone:bar") stored as a JSON array. Pass nil to leave subjects unset.
// Ref is an optional knowledge-base-relative path (e.g., "dossiers/foo.md").
// Pass "" to leave ref unset.
func (s *Store) Set(category Category, key, value, source string, confidence float64, subjects []string, ref string) (*Fact, error) {
	now := time.Now().UTC()

	var subjectsJSON *string
	if len(subjects) > 0 {
		b, err := json.Marshal(subjects)
		if err != nil {
			return nil, fmt.Errorf("marshal subjects: %w", err)
		}
		s := string(b)
		subjectsJSON = &s
	}

	var refSQL *string
	if ref != "" {
		refSQL = &ref
	}

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
			Subjects:   subjects,
			Ref:        ref,
			CreatedAt:  now,
			UpdatedAt:  now,
			AccessedAt: now,
		}

		_, err = s.db.Exec(`
			INSERT INTO facts (id, category, key, value, source, confidence, subjects, ref, created_at, updated_at, accessed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id.String(), category, key, value, source, confidence, subjectsJSON, refSQL,
			now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339))
		if err != nil {
			return nil, fmt.Errorf("insert: %w", err)
		}
		s.syncFTS(category, key, value, source)
		return fact, nil
	} else if err != nil {
		return nil, fmt.Errorf("check existing: %w", err)
	}

	// Update existing (resurrect if soft-deleted)
	_, err = s.db.Exec(`
		UPDATE facts SET value = ?, source = ?, confidence = ?, subjects = ?, ref = ?, updated_at = ?, accessed_at = ?, deleted_at = NULL
		WHERE category = ? AND key = ?
	`, value, source, confidence, subjectsJSON, refSQL, now.Format(time.RFC3339), now.Format(time.RFC3339), category, key)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	s.syncFTS(category, key, value, source)

	id, _ := uuid.Parse(existingID)
	return &Fact{
		ID:         id,
		Category:   category,
		Key:        key,
		Value:      value,
		Source:     source,
		Confidence: confidence,
		Subjects:   subjects,
		Ref:        ref,
		UpdatedAt:  now,
		AccessedAt: now,
	}, nil
}

// Get retrieves a fact by category and key.
func (s *Store) Get(category Category, key string) (*Fact, error) {
	fact, err := s.scanFact(s.db.QueryRow(
		`SELECT `+factColumns+` FROM facts WHERE `+activeFilter+` AND category = ? AND key = ?`,
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
		`SELECT `+factColumns+` FROM facts WHERE `+activeFilter+` AND category = ? ORDER BY key`,
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
		`SELECT ` + factColumns + ` FROM facts WHERE ` + activeFilter + ` ORDER BY category, key`)
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

// GetBySubjects retrieves all active facts associated with any of the
// given subject keys. Subjects are stored as JSON arrays and queried
// using SQLite's json_each() function. Returns nil when no subjects are
// provided or no facts match.
func (s *Store) GetBySubjects(subjects []string) ([]*Fact, error) {
	if len(subjects) == 0 {
		return nil, nil
	}

	// Build query with IN clause for json_each matching.
	placeholders := make([]string, len(subjects))
	args := make([]any, len(subjects))
	for i, sub := range subjects {
		placeholders[i] = "?"
		args[i] = sub
	}

	query := `SELECT ` + factColumns + ` FROM facts WHERE ` + activeFilter + ` AND subjects IS NOT NULL AND EXISTS (
		SELECT 1 FROM json_each(facts.subjects) WHERE value IN (` + strings.Join(placeholders, ",") + `)
	) ORDER BY updated_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query by subjects: %w", err)
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

// Search finds facts matching the query. Uses FTS5 full-text search when
// available, falling back to LIKE-based search otherwise.
func (s *Store) Search(query string) ([]*Fact, error) {
	if s.ftsEnabled {
		return s.searchFTS(query)
	}
	return s.searchLIKE(query)
}

// searchFTS uses FTS5 full-text search with BM25 ranking.
func (s *Store) searchFTS(query string) ([]*Fact, error) {
	sanitized := sanitizeFTS5Query(query)
	if sanitized == "" {
		return nil, nil
	}

	rows, err := s.db.Query(`
		SELECT `+factColumnsFTS+`
		FROM facts_fts
		JOIN facts ON facts_fts.rowid = facts.rowid
		WHERE facts_fts MATCH ? AND facts.`+activeFilter+`
		ORDER BY rank
		LIMIT 50
	`, sanitized)
	if err != nil {
		// FTS query failed — fall back to LIKE search.
		s.logger.Warn("FTS5 search failed, falling back to LIKE", "error", err, "query", query)
		return s.searchLIKE(query)
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

// searchLIKE uses simple pattern matching as a fallback.
func (s *Store) searchLIKE(query string) ([]*Fact, error) {
	pattern := "%" + query + "%"
	rows, err := s.db.Query(
		`SELECT `+factColumns+` FROM facts WHERE `+activeFilter+` AND (key LIKE ? OR value LIKE ?) ORDER BY accessed_at DESC LIMIT 50`,
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

// syncFTS updates the FTS index after a fact is inserted or updated.
// Uses the external content table pattern: delete old entry, insert new.
func (s *Store) syncFTS(category Category, key, value, source string) {
	if !s.ftsEnabled {
		return
	}
	// Rebuild is the simplest correct approach for an external content table
	// with soft deletes. The facts table is small enough that this is fast.
	s.rebuildFTS()
}

// rebuildFTS reconstructs the FTS index from the facts table.
func (s *Store) rebuildFTS() {
	if !s.ftsEnabled {
		return
	}
	if _, err := s.db.Exec(`INSERT INTO facts_fts(facts_fts) VALUES('rebuild')`); err != nil {
		s.logger.Warn("failed to rebuild facts FTS index", "error", err)
	}
}

// sanitizeFTS5Query wraps each search term in double quotes to prevent FTS5
// syntax errors from special characters, then joins terms with OR so that
// broader recall is possible. BM25 ranking ensures results matching more
// terms score higher.
func sanitizeFTS5Query(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return ""
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		w = strings.ReplaceAll(w, `"`, `""`)
		quoted[i] = `"` + w + `"`
	}
	return strings.Join(quoted, " OR ")
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

	// Rebuild FTS to exclude soft-deleted facts.
	s.rebuildFTS()
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
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM facts WHERE ` + activeFilter).Scan(&total)

	// Count by category
	cats := make(map[string]int)
	rows, _ := s.db.Query(`SELECT category, COUNT(*) FROM facts WHERE ` + activeFilter + ` GROUP BY category`)
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
	var source, subjectsRaw, refRaw sql.NullString

	err := row.Scan(&idStr, &catStr, &f.Key, &f.Value, &source, &f.Confidence, &subjectsRaw, &createdStr, &updatedStr, &accessedStr, &refRaw)
	if err != nil {
		return nil, err
	}

	f.ID, _ = uuid.Parse(idStr)
	f.Category = Category(catStr)
	if source.Valid {
		f.Source = source.String
	}
	if subjectsRaw.Valid {
		_ = json.Unmarshal([]byte(subjectsRaw.String), &f.Subjects)
	}
	if refRaw.Valid {
		f.Ref = refRaw.String
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	f.AccessedAt, _ = time.Parse(time.RFC3339, accessedStr)

	return &f, nil
}

func (s *Store) scanFactRow(rows *sql.Rows) (*Fact, error) {
	var f Fact
	var idStr, catStr, createdStr, updatedStr, accessedStr string
	var source, subjectsRaw, refRaw sql.NullString

	err := rows.Scan(&idStr, &catStr, &f.Key, &f.Value, &source, &f.Confidence, &subjectsRaw, &createdStr, &updatedStr, &accessedStr, &refRaw)
	if err != nil {
		return nil, err
	}

	f.ID, _ = uuid.Parse(idStr)
	f.Category = Category(catStr)
	if source.Valid {
		f.Source = source.String
	}
	if subjectsRaw.Valid {
		_ = json.Unmarshal([]byte(subjectsRaw.String), &f.Subjects)
	}
	if refRaw.Valid {
		f.Ref = refRaw.String
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
		`SELECT ` + factColumnsWithEmbed + ` FROM facts WHERE ` + activeFilter + ` AND embedding IS NOT NULL`)
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
		`SELECT ` + factColumns + ` FROM facts WHERE ` + activeFilter + ` AND embedding IS NULL`)
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
	var source, subjectsRaw, refRaw sql.NullString
	var embeddingBlob []byte

	err := rows.Scan(&idStr, &catStr, &f.Key, &f.Value, &source, &f.Confidence, &subjectsRaw, &embeddingBlob, &createdStr, &updatedStr, &accessedStr, &refRaw)
	if err != nil {
		return nil, err
	}

	f.ID, _ = uuid.Parse(idStr)
	f.Category = Category(catStr)
	if source.Valid {
		f.Source = source.String
	}
	if subjectsRaw.Valid {
		_ = json.Unmarshal([]byte(subjectsRaw.String), &f.Subjects)
	}
	if refRaw.Valid {
		f.Ref = refRaw.String
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

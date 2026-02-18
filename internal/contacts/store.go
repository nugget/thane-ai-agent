// Package contacts provides structured storage for people and organizations.
package contacts

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SQL fragments for query building.
const (
	contactColumns          = "id, name, kind, relationship, summary, details, last_interaction, created_at, updated_at"
	qualifiedContactColumns = "contacts.id, contacts.name, contacts.kind, contacts.relationship, contacts.summary, contacts.details, contacts.last_interaction, contacts.created_at, contacts.updated_at"
	contactColumnsWithEmbed = "id, name, kind, relationship, summary, details, embedding, last_interaction, created_at, updated_at"
	activeFilter            = "deleted_at IS NULL"
)

// Contact represents a person, company, or organization.
type Contact struct {
	ID              uuid.UUID           `json:"id"`
	Name            string              `json:"name"`
	Kind            string              `json:"kind"`                   // person, company, organization
	Relationship    string              `json:"relationship,omitempty"` // friend, colleague, family, vendor
	Summary         string              `json:"summary,omitempty"`      // one-line context
	Details         string              `json:"details,omitempty"`      // markdown blob
	Embedding       []float32           `json:"embedding,omitempty"`    // vector for semantic search
	LastInteraction time.Time           `json:"last_interaction,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	Facts           map[string][]string `json:"facts,omitempty"` // populated by GetWithFacts
}

// Store manages contact persistence in SQLite.
type Store struct {
	db         *sql.DB
	ftsEnabled bool
	logger     *slog.Logger
}

// NewStore creates a contact store using the given database path.
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

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS contacts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT 'person',
			relationship TEXT,
			summary TEXT,
			details TEXT,
			embedding BLOB,
			last_interaction TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_contacts_name ON contacts(name);
		CREATE INDEX IF NOT EXISTS idx_contacts_kind ON contacts(kind);
		CREATE INDEX IF NOT EXISTS idx_contacts_deleted ON contacts(deleted_at);
	`)
	if err != nil {
		return err
	}

	// Enforce active name uniqueness (case-insensitive). Allows soft-deleted
	// duplicates but prevents two active contacts with the same name.
	_, _ = s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_name_active ON contacts(LOWER(name)) WHERE deleted_at IS NULL`)

	// contact_facts supports multiple values per key (e.g., two phone
	// numbers). If the table was created with the old UNIQUE(contact_id, key)
	// constraint, rebuild it without the constraint.
	s.migrateContactFacts()

	s.tryEnableFTS()
	return nil
}

// migrateContactFacts creates or migrates the contact_facts table.
// Older schema had UNIQUE(contact_id, key); the new schema allows
// multiple values per key.
func (s *Store) migrateContactFacts() {
	// Try creating the table. If it already exists this is a no-op.
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS contact_facts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			contact_id TEXT NOT NULL REFERENCES contacts(id),
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)
	`)
	if err != nil {
		s.logger.Warn("failed to create contact_facts table", "error", err)
		return
	}

	// Check if the old unique constraint exists and migrate away from it.
	var uniqueCount int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('contact_facts') WHERE "unique" = 1 AND origin = 'u'`).Scan(&uniqueCount)
	if uniqueCount > 0 {
		s.logger.Info("migrating contact_facts to multi-value schema")
		tx, err := s.db.Begin()
		if err != nil {
			s.logger.Warn("contact_facts migration failed", "error", err)
			return
		}
		defer func() { _ = tx.Rollback() }()

		stmts := []string{
			`ALTER TABLE contact_facts RENAME TO contact_facts_old`,
			`CREATE TABLE contact_facts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				contact_id TEXT NOT NULL REFERENCES contacts(id),
				key TEXT NOT NULL,
				value TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`INSERT INTO contact_facts (contact_id, key, value, updated_at) SELECT contact_id, key, value, updated_at FROM contact_facts_old`,
			`DROP TABLE contact_facts_old`,
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(stmt); err != nil {
				s.logger.Warn("contact_facts migration step failed", "error", err, "stmt", stmt)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			s.logger.Warn("contact_facts migration commit failed", "error", err)
			return
		}
		s.logger.Info("contact_facts migration complete")
	}

	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_contact_facts_contact_id ON contact_facts(contact_id)`)
}

// tryEnableFTS creates the FTS5 virtual table for full-text search.
// Falls back to LIKE-based search when FTS5 is not available.
func (s *Store) tryEnableFTS() {
	_, err := s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS contacts_fts USING fts5(
			name,
			relationship,
			summary,
			details,
			content=contacts,
			content_rowid=rowid
		)
	`)
	if err != nil {
		s.logger.Warn("FTS5 not available for contacts, using LIKE fallback", "error", err)
		return
	}
	s.ftsEnabled = true

	_, err = s.db.Exec(`INSERT INTO contacts_fts(contacts_fts) VALUES('rebuild')`)
	if err != nil {
		s.logger.Warn("failed to rebuild contacts FTS index", "error", err)
		s.ftsEnabled = false
	}
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Upsert creates or updates a contact. If the contact has no ID, a new
// UUIDv7 is assigned. Soft-deleted contacts with the same ID are
// resurrected.
func (s *Store) Upsert(c *Contact) (*Contact, error) {
	now := time.Now().UTC()

	if c.Kind == "" {
		c.Kind = "person"
	}

	if c.ID == uuid.Nil {
		// New contact.
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("generate id: %w", err)
		}
		c.ID = id
		c.CreatedAt = now
		c.UpdatedAt = now

		_, err = s.db.Exec(`
			INSERT INTO contacts (id, name, kind, relationship, summary, details, last_interaction, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, c.ID.String(), c.Name, c.Kind, nullStr(c.Relationship), nullStr(c.Summary),
			nullStr(c.Details), nullTime(c.LastInteraction),
			now.Format(time.RFC3339), now.Format(time.RFC3339))
		if err != nil {
			return nil, fmt.Errorf("insert: %w", err)
		}
		s.rebuildFTS()
		return c, nil
	}

	// Update existing (resurrect if soft-deleted).
	c.UpdatedAt = now
	_, err := s.db.Exec(`
		UPDATE contacts SET name = ?, kind = ?, relationship = ?, summary = ?, details = ?,
			last_interaction = ?, updated_at = ?, deleted_at = NULL
		WHERE id = ?
	`, c.Name, c.Kind, nullStr(c.Relationship), nullStr(c.Summary),
		nullStr(c.Details), nullTime(c.LastInteraction),
		now.Format(time.RFC3339), c.ID.String())
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	s.rebuildFTS()
	return c, nil
}

// FindByName returns the first active contact with a case-insensitive
// name match. Returns sql.ErrNoRows if not found.
func (s *Store) FindByName(name string) (*Contact, error) {
	return s.scanContact(s.db.QueryRow(
		`SELECT `+contactColumns+` FROM contacts WHERE `+activeFilter+` AND LOWER(name) = LOWER(?)`,
		name))
}

// Get retrieves a contact by ID.
func (s *Store) Get(id uuid.UUID) (*Contact, error) {
	return s.scanContact(s.db.QueryRow(
		`SELECT `+contactColumns+` FROM contacts WHERE `+activeFilter+` AND id = ?`,
		id.String()))
}

// GetWithFacts retrieves a contact by ID and populates its Facts map.
func (s *Store) GetWithFacts(id uuid.UUID) (*Contact, error) {
	c, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	c.Facts, err = s.GetFacts(id)
	if err != nil {
		return nil, fmt.Errorf("get facts: %w", err)
	}
	return c, nil
}

// Search finds contacts matching the query using FTS5 or LIKE fallback.
func (s *Store) Search(query string) ([]*Contact, error) {
	if s.ftsEnabled {
		return s.searchFTS(query)
	}
	return s.searchLIKE(query)
}

func (s *Store) searchFTS(query string) ([]*Contact, error) {
	sanitized := sanitizeFTS5Query(query)
	if sanitized == "" {
		return nil, nil
	}

	rows, err := s.db.Query(`
		SELECT `+contactColumns+`
		FROM contacts_fts
		JOIN contacts ON contacts_fts.rowid = contacts.rowid
		WHERE contacts_fts MATCH ? AND `+activeFilter+`
		ORDER BY rank
		LIMIT 50
	`, sanitized)
	if err != nil {
		s.logger.Warn("FTS5 search failed, falling back to LIKE", "error", err, "query", query)
		return s.searchLIKE(query)
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

func (s *Store) searchLIKE(query string) ([]*Contact, error) {
	pattern := "%" + query + "%"
	rows, err := s.db.Query(
		`SELECT `+contactColumns+` FROM contacts WHERE `+activeFilter+
			` AND (name LIKE ? OR relationship LIKE ? OR summary LIKE ? OR details LIKE ?) ORDER BY updated_at DESC LIMIT 50`,
		pattern, pattern, pattern, pattern)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

// ListByKind returns all active contacts of the given kind.
func (s *Store) ListByKind(kind string) ([]*Contact, error) {
	rows, err := s.db.Query(
		`SELECT `+contactColumns+` FROM contacts WHERE `+activeFilter+` AND kind = ? ORDER BY name`,
		kind)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

// ListAll returns all active contacts.
func (s *Store) ListAll() ([]*Contact, error) {
	rows, err := s.db.Query(
		`SELECT ` + contactColumns + ` FROM contacts WHERE ` + activeFilter + ` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

// Delete soft-deletes a contact by ID.
func (s *Store) Delete(id uuid.UUID) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.Exec(
		`UPDATE contacts SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, id.String())
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("contact not found: %s", id)
	}

	s.rebuildFTS()
	return nil
}

// DeleteByName soft-deletes a contact by case-insensitive name match.
func (s *Store) DeleteByName(name string) error {
	c, err := s.FindByName(name)
	if err != nil {
		return fmt.Errorf("find contact: %w", err)
	}
	return s.Delete(c.ID)
}

// SetFact adds a structured attribute to a contact. If the exact
// (contact_id, key, value) triple already exists, this is a no-op.
// Multiple values per key are supported (e.g., two phone numbers).
func (s *Store) SetFact(contactID uuid.UUID, key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339)

	var exists int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM contact_facts WHERE contact_id = ? AND key = ? AND value = ?`,
		contactID.String(), key, value).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check existing fact: %w", err)
	}
	if exists > 0 {
		return nil
	}

	_, err = s.db.Exec(
		`INSERT INTO contact_facts (contact_id, key, value, updated_at) VALUES (?, ?, ?, ?)`,
		contactID.String(), key, value, now)
	if err != nil {
		return fmt.Errorf("set fact: %w", err)
	}
	return nil
}

// ReplaceFact replaces all values for a key with a single new value.
// Use this for "update phone number" semantics where only one value
// should exist per key.
func (s *Store) ReplaceFact(contactID uuid.UUID, key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(
		`DELETE FROM contact_facts WHERE contact_id = ? AND key = ?`,
		contactID.String(), key)
	if err != nil {
		return fmt.Errorf("delete old facts: %w", err)
	}

	_, err = tx.Exec(
		`INSERT INTO contact_facts (contact_id, key, value, updated_at) VALUES (?, ?, ?, ?)`,
		contactID.String(), key, value, now)
	if err != nil {
		return fmt.Errorf("insert fact: %w", err)
	}

	return tx.Commit()
}

// DeleteFact removes a specific fact value from a contact.
func (s *Store) DeleteFact(contactID uuid.UUID, key, value string) error {
	result, err := s.db.Exec(
		`DELETE FROM contact_facts WHERE contact_id = ? AND key = ? AND value = ?`,
		contactID.String(), key, value)
	if err != nil {
		return fmt.Errorf("delete fact: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("fact not found: %s=%s", key, value)
	}
	return nil
}

// GetFacts returns all structured attributes for a contact. Multiple
// values per key are returned as a string slice.
func (s *Store) GetFacts(contactID uuid.UUID) (map[string][]string, error) {
	rows, err := s.db.Query(
		`SELECT key, value FROM contact_facts WHERE contact_id = ? ORDER BY key, value`,
		contactID.String())
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	facts := make(map[string][]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		facts[key] = append(facts[key], value)
	}
	return facts, rows.Err()
}

// FindByFact returns contacts that have a matching key-value attribute.
// The value is matched with LIKE for partial matching.
func (s *Store) FindByFact(key, value string) ([]*Contact, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT `+qualifiedContactColumns+`
		FROM contacts
		JOIN contact_facts ON contacts.id = contact_facts.contact_id
		WHERE contacts.`+activeFilter+` AND contact_facts.key = ? AND contact_facts.value LIKE ?
		ORDER BY contacts.name
	`, key, "%"+value+"%")
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

// SetEmbedding updates a contact's embedding vector.
func (s *Store) SetEmbedding(id uuid.UUID, embedding []float32) error {
	blob := encodeEmbedding(embedding)
	_, err := s.db.Exec(`UPDATE contacts SET embedding = ? WHERE id = ?`, blob, id.String())
	return err
}

// SemanticSearch finds contacts similar to the query embedding.
func (s *Store) SemanticSearch(queryEmbedding []float32, limit int) ([]*Contact, []float32, error) {
	if limit <= 0 {
		return nil, nil, nil
	}

	rows, err := s.db.Query(
		`SELECT ` + contactColumnsWithEmbed + ` FROM contacts WHERE ` + activeFilter + ` AND embedding IS NOT NULL`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	type scored struct {
		contact *Contact
		score   float32
	}
	var scores []scored

	for rows.Next() {
		c, err := s.scanContactWithEmbedding(rows)
		if err != nil {
			continue
		}
		if len(c.Embedding) > 0 {
			sim := cosineSimilarity(queryEmbedding, c.Embedding)
			scores = append(scores, scored{contact: c, score: sim})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Sort by score descending (partial selection sort for top-k).
	for i := 0; i < limit && i < len(scores); i++ {
		maxIdx := i
		for j := i + 1; j < len(scores); j++ {
			if scores[j].score > scores[maxIdx].score {
				maxIdx = j
			}
		}
		scores[i], scores[maxIdx] = scores[maxIdx], scores[i]
	}

	resultContacts := make([]*Contact, 0, limit)
	resultScores := make([]float32, 0, limit)
	for i := 0; i < limit && i < len(scores); i++ {
		resultContacts = append(resultContacts, scores[i].contact)
		resultScores = append(resultScores, scores[i].score)
	}

	return resultContacts, resultScores, nil
}

// GetContactsWithoutEmbeddings returns contacts that need embeddings.
func (s *Store) GetContactsWithoutEmbeddings() ([]*Contact, error) {
	rows, err := s.db.Query(
		`SELECT ` + contactColumns + ` FROM contacts WHERE ` + activeFilter + ` AND embedding IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

// Stats returns contact statistics.
func (s *Store) Stats() map[string]any {
	var total int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM contacts WHERE ` + activeFilter).Scan(&total)

	kinds := make(map[string]int)
	rows, _ := s.db.Query(`SELECT kind, COUNT(*) FROM contacts WHERE ` + activeFilter + ` GROUP BY kind`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var kind string
			var count int
			if err := rows.Scan(&kind, &count); err != nil {
				continue
			}
			kinds[kind] = count
		}
	}

	return map[string]any{
		"total": total,
		"kinds": kinds,
	}
}

// --- scan helpers ---

func (s *Store) scanContact(row *sql.Row) (*Contact, error) {
	var c Contact
	var idStr string
	var relationship, summary, details, lastInteraction sql.NullString
	var createdStr, updatedStr string

	err := row.Scan(&idStr, &c.Name, &c.Kind, &relationship, &summary,
		&details, &lastInteraction, &createdStr, &updatedStr)
	if err != nil {
		return nil, err
	}

	return populateContact(&c, idStr, relationship, summary, details, lastInteraction, createdStr, updatedStr)
}

func (s *Store) scanContactRow(rows *sql.Rows) (*Contact, error) {
	var c Contact
	var idStr string
	var relationship, summary, details, lastInteraction sql.NullString
	var createdStr, updatedStr string

	err := rows.Scan(&idStr, &c.Name, &c.Kind, &relationship, &summary,
		&details, &lastInteraction, &createdStr, &updatedStr)
	if err != nil {
		return nil, err
	}

	return populateContact(&c, idStr, relationship, summary, details, lastInteraction, createdStr, updatedStr)
}

func (s *Store) scanContactWithEmbedding(rows *sql.Rows) (*Contact, error) {
	var c Contact
	var idStr string
	var relationship, summary, details, lastInteraction sql.NullString
	var createdStr, updatedStr string
	var embeddingBlob []byte

	err := rows.Scan(&idStr, &c.Name, &c.Kind, &relationship, &summary,
		&details, &embeddingBlob, &lastInteraction, &createdStr, &updatedStr)
	if err != nil {
		return nil, err
	}

	c.Embedding = decodeEmbedding(embeddingBlob)
	return populateContact(&c, idStr, relationship, summary, details, lastInteraction, createdStr, updatedStr)
}

// populateContact fills parsed fields into a Contact, returning errors
// for corrupt UUID or timestamp data.
func populateContact(c *Contact, idStr string, relationship, summary, details, lastInteraction sql.NullString, createdStr, updatedStr string) (*Contact, error) {
	var err error
	c.ID, err = uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse contact id: %w", err)
	}

	c.Relationship = relationship.String
	c.Summary = summary.String
	c.Details = details.String

	if lastInteraction.Valid {
		c.LastInteraction, _ = time.Parse(time.RFC3339, lastInteraction.String)
	}

	c.CreatedAt, err = time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	c.UpdatedAt, err = time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	return c, nil
}

func (s *Store) scanContacts(rows *sql.Rows) ([]*Contact, error) {
	var contacts []*Contact
	for rows.Next() {
		c, err := s.scanContactRow(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// --- FTS helpers ---

func (s *Store) rebuildFTS() {
	if !s.ftsEnabled {
		return
	}
	if _, err := s.db.Exec(`INSERT INTO contacts_fts(contacts_fts) VALUES('rebuild')`); err != nil {
		s.logger.Warn("failed to rebuild contacts FTS index", "error", err)
	}
}

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
	return strings.Join(quoted, " ")
}

// --- embedding helpers ---

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

// --- SQL helpers ---

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullTime(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: t.Format(time.RFC3339), Valid: true}
}

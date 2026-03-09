// Package contacts provides vCard-aligned structured storage for people,
// organizations, groups, and locations.
package contacts

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/knowledge"
)

// SQL fragments for query building. Column order must match scan helpers.
const (
	contactColumns = `id, kind, formatted_name, family_name, given_name,
		additional_names, name_prefix, name_suffix, nickname,
		birthday, anniversary, gender, org, title, role,
		note, photo_uri, trust_zone, ai_summary, rev, etag,
		last_interaction, created_at, updated_at`

	qualifiedContactColumns = `contacts.id, contacts.kind, contacts.formatted_name,
		contacts.family_name, contacts.given_name, contacts.additional_names,
		contacts.name_prefix, contacts.name_suffix, contacts.nickname,
		contacts.birthday, contacts.anniversary, contacts.gender,
		contacts.org, contacts.title, contacts.role,
		contacts.note, contacts.photo_uri, contacts.trust_zone,
		contacts.ai_summary, contacts.rev, contacts.etag,
		contacts.last_interaction, contacts.created_at, contacts.updated_at`

	contactColumnsWithEmbed = `id, kind, formatted_name, family_name, given_name,
		additional_names, name_prefix, name_suffix, nickname,
		birthday, anniversary, gender, org, title, role,
		note, photo_uri, trust_zone, ai_summary, rev, etag,
		embedding, last_interaction, created_at, updated_at`

	activeFilter = "deleted_at IS NULL"
)

// ValidTrustZones is the set of allowed trust zone values.
var ValidTrustZones = map[string]bool{
	"owner":   true,
	"trusted": true,
	"known":   true,
}

// ValidKinds is the set of allowed contact kind values (vCard KIND).
var ValidKinds = map[string]bool{
	"individual": true,
	"group":      true,
	"org":        true,
	"location":   true,
}

// Contact represents a vCard-aligned contact record. Fields map to
// vCard 4.0 (RFC 6350) properties unless noted as Thane extensions.
type Contact struct {
	ID              uuid.UUID  `json:"id"`
	Kind            string     `json:"kind"`                       // vCard KIND: individual, group, org, location
	FormattedName   string     `json:"formatted_name"`             // vCard FN (display name)
	FamilyName      string     `json:"family_name,omitempty"`      // vCard N component
	GivenName       string     `json:"given_name,omitempty"`       // vCard N component
	AdditionalNames string     `json:"additional_names,omitempty"` // vCard N component
	NamePrefix      string     `json:"name_prefix,omitempty"`      // vCard N component
	NameSuffix      string     `json:"name_suffix,omitempty"`      // vCard N component
	Nickname        string     `json:"nickname,omitempty"`         // vCard NICKNAME
	Birthday        string     `json:"birthday,omitempty"`         // vCard BDAY (ISO 8601)
	Anniversary     string     `json:"anniversary,omitempty"`      // vCard ANNIVERSARY
	Gender          string     `json:"gender,omitempty"`           // vCard GENDER
	Org             string     `json:"org,omitempty"`              // vCard ORG
	Title           string     `json:"title,omitempty"`            // vCard TITLE
	Role            string     `json:"role,omitempty"`             // vCard ROLE
	Note            string     `json:"note,omitempty"`             // vCard NOTE
	PhotoURI        string     `json:"photo_uri,omitempty"`        // vCard PHOTO URI
	TrustZone       string     `json:"trust_zone"`                 // X-THANE-TRUST-ZONE
	AISummary       string     `json:"ai_summary,omitempty"`       // X-THANE-AI-SUMMARY
	Rev             string     `json:"rev"`                        // vCard REV (ISO 8601)
	ETag            string     `json:"etag,omitempty"`             // CardDAV sync
	Embedding       []float32  `json:"embedding,omitempty"`        // semantic search vector
	LastInteraction time.Time  `json:"last_interaction,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Properties      []Property `json:"properties,omitempty"` // populated by GetWithProperties
}

// Property represents a structured vCard property on a contact.
// Repeatable, parameterised properties (EMAIL, TEL, ADR, IMPP, URL,
// KEY, CATEGORIES, RELATED, MEMBER, etc.) are stored here rather than
// on the Contact struct directly.
type Property struct {
	ID        int64     `json:"id"`
	ContactID uuid.UUID `json:"contact_id"`
	Property  string    `json:"property"` // EMAIL, TEL, ADR, IMPP, URL, KEY, etc.
	Value     string    `json:"value"`
	Type      string    `json:"type,omitempty"`      // TYPE param: work, home, cell, etc.
	Pref      int       `json:"pref,omitempty"`      // PREF param: 1-100, 0 = unset
	Label     string    `json:"label,omitempty"`     // LABEL param
	MediaType string    `json:"mediatype,omitempty"` // MEDIATYPE param
	Verified  bool      `json:"verified,omitempty"`  // has Thane verified traffic from this?
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store manages contact persistence in SQLite.
type Store struct {
	db         *sql.DB
	ftsEnabled bool
	logger     *slog.Logger
}

// NewStore creates a contact store using the given database path.
func NewStore(dbPath string, logger *slog.Logger) (*Store, error) {
	db, err := database.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable foreign key enforcement so ON DELETE CASCADE on
	// contact_properties actually fires.
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
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
			kind TEXT NOT NULL DEFAULT 'individual',
			formatted_name TEXT NOT NULL,
			family_name TEXT,
			given_name TEXT,
			additional_names TEXT,
			name_prefix TEXT,
			name_suffix TEXT,
			nickname TEXT,
			birthday TEXT,
			anniversary TEXT,
			gender TEXT,
			org TEXT,
			title TEXT,
			role TEXT,
			note TEXT,
			photo_uri TEXT,
			trust_zone TEXT NOT NULL DEFAULT 'known',
			ai_summary TEXT,
			rev TEXT NOT NULL,
			etag TEXT,
			embedding BLOB,
			last_interaction TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_contacts_kind ON contacts(kind);
		CREATE INDEX IF NOT EXISTS idx_contacts_fn ON contacts(formatted_name);
		CREATE INDEX IF NOT EXISTS idx_contacts_deleted ON contacts(deleted_at);
		CREATE INDEX IF NOT EXISTS idx_contacts_trust_zone ON contacts(trust_zone);
	`)
	if err != nil {
		return err
	}

	// Enforce active name uniqueness (case-insensitive).
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_contacts_fn_active ON contacts(LOWER(formatted_name)) WHERE deleted_at IS NULL`); err != nil {
		s.logger.Warn("unique active name index not created", "error", err)
	}

	// contact_properties: multi-value vCard properties with parameters.
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS contact_properties (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			contact_id TEXT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
			property TEXT NOT NULL,
			value TEXT NOT NULL,
			type TEXT,
			pref INTEGER,
			label TEXT,
			mediatype TEXT,
			verified INTEGER DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_cp_contact ON contact_properties(contact_id);
		CREATE INDEX IF NOT EXISTS idx_cp_property ON contact_properties(property);
		CREATE INDEX IF NOT EXISTS idx_cp_property_value ON contact_properties(property, value);
		CREATE INDEX IF NOT EXISTS idx_cp_value ON contact_properties(value);
	`)
	if err != nil {
		return err
	}

	s.tryEnableFTS()
	return nil
}

// tryEnableFTS creates the FTS5 virtual table for full-text search.
// Falls back to LIKE-based search when FTS5 is not available.
func (s *Store) tryEnableFTS() {
	_, err := s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS contacts_fts USING fts5(
			formatted_name,
			nickname,
			note,
			ai_summary,
			org,
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
// resurrected. Rev is automatically set to the current timestamp.
func (s *Store) Upsert(c *Contact) (*Contact, error) {
	now := time.Now().UTC()

	if c.Kind == "" {
		c.Kind = "individual"
	}
	if !ValidKinds[c.Kind] {
		return nil, fmt.Errorf("invalid kind %q (valid: individual, group, org, location)", c.Kind)
	}
	if c.TrustZone == "" {
		c.TrustZone = "known"
	}
	if !ValidTrustZones[c.TrustZone] {
		return nil, fmt.Errorf("invalid trust zone %q (valid: owner, trusted, known)", c.TrustZone)
	}

	c.Rev = now.Format(time.RFC3339)

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
			INSERT INTO contacts (id, kind, formatted_name, family_name, given_name,
				additional_names, name_prefix, name_suffix, nickname,
				birthday, anniversary, gender, org, title, role,
				note, photo_uri, trust_zone, ai_summary, rev, etag,
				last_interaction, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, c.ID.String(), c.Kind, c.FormattedName,
			nullStr(c.FamilyName), nullStr(c.GivenName), nullStr(c.AdditionalNames),
			nullStr(c.NamePrefix), nullStr(c.NameSuffix), nullStr(c.Nickname),
			nullStr(c.Birthday), nullStr(c.Anniversary), nullStr(c.Gender),
			nullStr(c.Org), nullStr(c.Title), nullStr(c.Role),
			nullStr(c.Note), nullStr(c.PhotoURI),
			c.TrustZone, nullStr(c.AISummary), c.Rev, nullStr(c.ETag),
			nullTime(c.LastInteraction),
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
		UPDATE contacts SET kind = ?, formatted_name = ?, family_name = ?, given_name = ?,
			additional_names = ?, name_prefix = ?, name_suffix = ?, nickname = ?,
			birthday = ?, anniversary = ?, gender = ?, org = ?, title = ?, role = ?,
			note = ?, photo_uri = ?, trust_zone = ?, ai_summary = ?, rev = ?, etag = ?,
			last_interaction = ?, updated_at = ?, deleted_at = NULL
		WHERE id = ?
	`, c.Kind, c.FormattedName,
		nullStr(c.FamilyName), nullStr(c.GivenName), nullStr(c.AdditionalNames),
		nullStr(c.NamePrefix), nullStr(c.NameSuffix), nullStr(c.Nickname),
		nullStr(c.Birthday), nullStr(c.Anniversary), nullStr(c.Gender),
		nullStr(c.Org), nullStr(c.Title), nullStr(c.Role),
		nullStr(c.Note), nullStr(c.PhotoURI),
		c.TrustZone, nullStr(c.AISummary), c.Rev, nullStr(c.ETag),
		nullTime(c.LastInteraction),
		now.Format(time.RFC3339),
		c.ID.String())
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	s.rebuildFTS()
	return c, nil
}

// FindByName returns the first active contact with a case-insensitive
// formatted name match. Returns sql.ErrNoRows if not found.
func (s *Store) FindByName(name string) (*Contact, error) {
	return s.scanContact(s.db.QueryRow(
		`SELECT `+contactColumns+` FROM contacts WHERE `+activeFilter+` AND LOWER(formatted_name) = LOWER(?)`,
		name))
}

// FindByNickname returns the first active contact with a case-insensitive
// nickname match. Returns sql.ErrNoRows if not found.
func (s *Store) FindByNickname(name string) (*Contact, error) {
	return s.scanContact(s.db.QueryRow(
		`SELECT `+contactColumns+` FROM contacts WHERE `+activeFilter+` AND LOWER(nickname) = LOWER(?)`,
		name))
}

// ResolveContact finds a contact by name using cascading resolution
// strategies: exact formatted name → nickname → search fallback.
// Returns [sql.ErrNoRows] if no match is found, or an error listing
// ambiguous matches if search returns multiple results.
func (s *Store) ResolveContact(name string) (*Contact, error) {
	// 1. Exact formatted name match (fast, indexed).
	c, err := s.FindByName(name)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("find by name %q: %w", name, err)
	}

	// 2. Nickname match (direct column query).
	c, err = s.FindByNickname(name)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("find by nickname %q: %w", name, err)
	}

	// 3. Search fallback (FTS or LIKE).
	results, err := s.Search(name)
	if err != nil {
		return nil, fmt.Errorf("search fallback for %q: %w", name, err)
	}
	if len(results) == 1 {
		return results[0], nil
	}
	if len(results) > 1 {
		names := make([]string, len(results))
		for i, c := range results {
			names[i] = c.FormattedName
		}
		return nil, fmt.Errorf("ambiguous contact %q: matches %v", name, names)
	}

	return nil, sql.ErrNoRows
}

// Get retrieves a contact by ID.
func (s *Store) Get(id uuid.UUID) (*Contact, error) {
	return s.scanContact(s.db.QueryRow(
		`SELECT `+contactColumns+` FROM contacts WHERE `+activeFilter+` AND id = ?`,
		id.String()))
}

// GetWithProperties retrieves a contact by ID and populates its
// Properties slice.
func (s *Store) GetWithProperties(id uuid.UUID) (*Contact, error) {
	c, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	c.Properties, err = s.GetProperties(id)
	if err != nil {
		return nil, fmt.Errorf("get properties: %w", err)
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
		SELECT `+qualifiedContactColumns+`
		FROM contacts_fts
		JOIN contacts ON contacts_fts.rowid = contacts.rowid
		WHERE contacts_fts MATCH ? AND contacts.`+activeFilter+`
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
			` AND (formatted_name LIKE ? OR nickname LIKE ? OR note LIKE ? OR ai_summary LIKE ? OR org LIKE ?) ORDER BY updated_at DESC LIMIT 50`,
		pattern, pattern, pattern, pattern, pattern)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

// ListByKind returns all active contacts of the given kind.
func (s *Store) ListByKind(kind string) ([]*Contact, error) {
	rows, err := s.db.Query(
		`SELECT `+contactColumns+` FROM contacts WHERE `+activeFilter+` AND kind = ? ORDER BY formatted_name LIMIT 100`,
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
		`SELECT ` + contactColumns + ` FROM contacts WHERE ` + activeFilter + ` ORDER BY formatted_name LIMIT 100`)
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

// DeleteByName soft-deletes a contact by name, using [ResolveContact]
// for cascading name resolution.
func (s *Store) DeleteByName(name string) error {
	c, err := s.ResolveContact(name)
	if err != nil {
		return fmt.Errorf("find contact: %w", err)
	}
	return s.Delete(c.ID)
}

// --- Property CRUD ---

// AddProperty adds a vCard property to a contact. If the exact
// (contact_id, property, value) triple already exists (case-insensitive
// on value), this is a no-op. Multiple values per property are supported.
func (s *Store) AddProperty(contactID uuid.UUID, p *Property) error {
	var exists int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM contact_properties WHERE contact_id = ? AND property = ? AND LOWER(value) = LOWER(?)`,
		contactID.String(), p.Property, p.Value).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check existing property: %w", err)
	}
	if exists > 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)

	result, err := s.db.Exec(`
		INSERT INTO contact_properties (contact_id, property, value, type, pref, label, mediatype, verified, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, contactID.String(), p.Property, p.Value,
		nullStr(p.Type), nullInt(p.Pref), nullStr(p.Label), nullStr(p.MediaType),
		boolToInt(p.Verified), now, now)
	if err != nil {
		return fmt.Errorf("add property: %w", err)
	}

	id, _ := result.LastInsertId()
	p.ID = id
	p.ContactID = contactID
	return nil
}

// GetProperties returns all properties for a contact, ordered by
// property name then preference.
func (s *Store) GetProperties(contactID uuid.UUID) ([]Property, error) {
	rows, err := s.db.Query(`
		SELECT id, contact_id, property, value, type, pref, label, mediatype, verified, created_at, updated_at
		FROM contact_properties
		WHERE contact_id = ?
		ORDER BY property, pref NULLS LAST, id
	`, contactID.String())
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var props []Property
	for rows.Next() {
		p, err := scanProperty(rows)
		if err != nil {
			return nil, err
		}
		props = append(props, p)
	}
	return props, rows.Err()
}

// GetPropertiesMap returns all properties for a contact grouped by
// property name as a map of name→values. This is a convenience view
// for callers that don't need the full Property metadata.
func (s *Store) GetPropertiesMap(contactID uuid.UUID) (map[string][]string, error) {
	rows, err := s.db.Query(
		`SELECT property, value FROM contact_properties WHERE contact_id = ? ORDER BY property, pref NULLS LAST, id`,
		contactID.String())
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	m := make(map[string][]string)
	for rows.Next() {
		var prop, val string
		if err := rows.Scan(&prop, &val); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		m[prop] = append(m[prop], val)
	}
	return m, rows.Err()
}

// FindByPropertyExact returns contacts with an exact property-value
// match. The value comparison is case-insensitive.
func (s *Store) FindByPropertyExact(property, value string) ([]*Contact, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT `+qualifiedContactColumns+`
		FROM contacts
		JOIN contact_properties ON contacts.id = contact_properties.contact_id
		WHERE contacts.`+activeFilter+`
		  AND contact_properties.property = ?
		  AND LOWER(contact_properties.value) = LOWER(?)
		ORDER BY contacts.formatted_name
		LIMIT 50
	`, property, value)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

// FindByProperty returns contacts with a LIKE-matched property value.
func (s *Store) FindByProperty(property, value string) ([]*Contact, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT `+qualifiedContactColumns+`
		FROM contacts
		JOIN contact_properties ON contacts.id = contact_properties.contact_id
		WHERE contacts.`+activeFilter+`
		  AND contact_properties.property = ?
		  AND contact_properties.value LIKE ?
		ORDER BY contacts.formatted_name
		LIMIT 50
	`, property, "%"+value+"%")
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

// DeleteProperty removes a single property row by its ID.
func (s *Store) DeleteProperty(id int64) error {
	result, err := s.db.Exec(`DELETE FROM contact_properties WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete property: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("property not found: %d", id)
	}
	return nil
}

// DeleteContactProperties removes all properties of a given type from
// a contact.
func (s *Store) DeleteContactProperties(contactID uuid.UUID, property string) error {
	_, err := s.db.Exec(
		`DELETE FROM contact_properties WHERE contact_id = ? AND property = ?`,
		contactID.String(), property)
	if err != nil {
		return fmt.Errorf("delete properties: %w", err)
	}
	return nil
}

// --- Trust zones and filtering ---

// FindByTrustZone returns all active contacts in the given trust zone.
func (s *Store) FindByTrustZone(zone string) ([]*Contact, error) {
	rows, err := s.db.Query(
		`SELECT `+contactColumns+` FROM contacts WHERE `+activeFilter+` AND trust_zone = ? ORDER BY formatted_name`,
		zone)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	return s.scanContacts(rows)
}

// --- Embeddings ---

// SetEmbedding updates a contact's embedding vector.
func (s *Store) SetEmbedding(id uuid.UUID, embedding []float32) error {
	blob := knowledge.EncodeEmbedding(embedding)
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
			sim := knowledge.CosineSimilarity(queryEmbedding, c.Embedding)
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

// scanTarget holds intermediate scan destinations for a contact row.
// Keeps the Scan call sites clean despite 24+ columns.
type scanTarget struct {
	idStr           string
	kind            string
	formattedName   string
	familyName      sql.NullString
	givenName       sql.NullString
	additionalNames sql.NullString
	namePrefix      sql.NullString
	nameSuffix      sql.NullString
	nickname        sql.NullString
	birthday        sql.NullString
	anniversary     sql.NullString
	gender          sql.NullString
	org             sql.NullString
	title           sql.NullString
	role            sql.NullString
	note            sql.NullString
	photoURI        sql.NullString
	trustZone       string
	aiSummary       sql.NullString
	rev             string
	etag            sql.NullString
	lastInteraction sql.NullString
	createdStr      string
	updatedStr      string
	embeddingBlob   []byte
}

// dests returns scan destinations matching contactColumns order.
func (t *scanTarget) dests() []any {
	return []any{
		&t.idStr, &t.kind, &t.formattedName,
		&t.familyName, &t.givenName, &t.additionalNames,
		&t.namePrefix, &t.nameSuffix, &t.nickname,
		&t.birthday, &t.anniversary, &t.gender,
		&t.org, &t.title, &t.role,
		&t.note, &t.photoURI,
		&t.trustZone, &t.aiSummary, &t.rev, &t.etag,
		&t.lastInteraction, &t.createdStr, &t.updatedStr,
	}
}

// destsWithEmbedding returns scan destinations matching
// contactColumnsWithEmbed order (embedding between etag and
// last_interaction).
func (t *scanTarget) destsWithEmbedding() []any {
	return []any{
		&t.idStr, &t.kind, &t.formattedName,
		&t.familyName, &t.givenName, &t.additionalNames,
		&t.namePrefix, &t.nameSuffix, &t.nickname,
		&t.birthday, &t.anniversary, &t.gender,
		&t.org, &t.title, &t.role,
		&t.note, &t.photoURI,
		&t.trustZone, &t.aiSummary, &t.rev, &t.etag,
		&t.embeddingBlob,
		&t.lastInteraction, &t.createdStr, &t.updatedStr,
	}
}

// toContact converts scanned values into a Contact.
func (t *scanTarget) toContact() (*Contact, error) {
	c := &Contact{
		Kind:            t.kind,
		FormattedName:   t.formattedName,
		FamilyName:      t.familyName.String,
		GivenName:       t.givenName.String,
		AdditionalNames: t.additionalNames.String,
		NamePrefix:      t.namePrefix.String,
		NameSuffix:      t.nameSuffix.String,
		Nickname:        t.nickname.String,
		Birthday:        t.birthday.String,
		Anniversary:     t.anniversary.String,
		Gender:          t.gender.String,
		Org:             t.org.String,
		Title:           t.title.String,
		Role:            t.role.String,
		Note:            t.note.String,
		PhotoURI:        t.photoURI.String,
		TrustZone:       t.trustZone,
		AISummary:       t.aiSummary.String,
		Rev:             t.rev,
		ETag:            t.etag.String,
	}

	var err error
	c.ID, err = uuid.Parse(t.idStr)
	if err != nil {
		return nil, fmt.Errorf("parse contact id: %w", err)
	}

	if t.lastInteraction.Valid {
		c.LastInteraction, _ = time.Parse(time.RFC3339, t.lastInteraction.String)
	}

	c.CreatedAt, err = time.Parse(time.RFC3339, t.createdStr)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	c.UpdatedAt, err = time.Parse(time.RFC3339, t.updatedStr)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}

	if len(t.embeddingBlob) > 0 {
		c.Embedding = knowledge.DecodeEmbedding(t.embeddingBlob)
	}

	return c, nil
}

func (s *Store) scanContact(row *sql.Row) (*Contact, error) {
	var t scanTarget
	if err := row.Scan(t.dests()...); err != nil {
		return nil, err
	}
	return t.toContact()
}

func (s *Store) scanContactRow(rows *sql.Rows) (*Contact, error) {
	var t scanTarget
	if err := rows.Scan(t.dests()...); err != nil {
		return nil, err
	}
	return t.toContact()
}

func (s *Store) scanContactWithEmbedding(rows *sql.Rows) (*Contact, error) {
	var t scanTarget
	if err := rows.Scan(t.destsWithEmbedding()...); err != nil {
		return nil, err
	}
	return t.toContact()
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

// scanProperty scans a single row from the contact_properties table.
func scanProperty(rows *sql.Rows) (Property, error) {
	var p Property
	var contactIDStr string
	var typ, label, mediatype sql.NullString
	var pref sql.NullInt64
	var verified int
	var createdStr, updatedStr string

	err := rows.Scan(&p.ID, &contactIDStr, &p.Property, &p.Value,
		&typ, &pref, &label, &mediatype, &verified,
		&createdStr, &updatedStr)
	if err != nil {
		return Property{}, fmt.Errorf("scan property: %w", err)
	}

	p.ContactID, err = uuid.Parse(contactIDStr)
	if err != nil {
		return Property{}, fmt.Errorf("parse property contact_id: %w", err)
	}
	p.Type = typ.String
	if pref.Valid {
		p.Pref = int(pref.Int64)
	}
	p.Label = label.String
	p.MediaType = mediatype.String
	p.Verified = verified != 0
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)

	return p, nil
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

// sanitizeFTS5Query wraps each search term in double quotes to prevent
// FTS5 syntax errors, then joins with OR for broader recall.
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

func nullInt(n int) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

package contacts

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// haPersonEntityRE pins the full domain.object_id shape — a bare
// "person." or a dotted suffix is not a valid Home Assistant entity.
var haPersonEntityRE = regexp.MustCompile(`^person\.[a-z0-9_]+$`)

// SetHAPersonEntity binds a contact to a Home Assistant person entity
// (e.g. "person.alice") — the counterparty edge presence flows through
// (#1450). An empty entity clears the binding.
//
// Custody: no model-facing tool exposes this mutation, deliberately.
// Bindings participate in counterparty attribution, and their write
// paths stay operator-gated; wire this only from configuration-driven
// or operator-initiated code.
func (s *Store) SetHAPersonEntity(id uuid.UUID, entity string) error {
	return applyHAPersonEntity(s.db, id, entity)
}

// ReplaceHAPersonBindings atomically makes bindings the exact set of
// active contact to Home Assistant person edges. Existing bindings not in
// the map are cleared. Every referenced contact must exist and every person
// entity must be valid and unique; otherwise the transaction rolls back
// without changing any binding.
//
// This is the signed-configuration reconciliation boundary. Model-facing
// contact tools must not call it.
func (s *Store) ReplaceHAPersonBindings(bindings map[uuid.UUID]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replacing ha person bindings: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on defer

	ids := make([]uuid.UUID, 0, len(bindings))
	for id := range bindings {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })

	desired := make(map[string]string, len(bindings))
	claimed := make(map[string]uuid.UUID, len(bindings))
	for _, id := range ids {
		rawEntity := bindings[id]
		entity := strings.TrimSpace(rawEntity)
		if !haPersonEntityRE.MatchString(entity) {
			return fmt.Errorf("ha person entity for %s must match person.<object_id> (lowercase letters, digits, underscores), got %q", id, rawEntity)
		}
		if holder, exists := claimed[entity]; exists {
			return fmt.Errorf("ha person entity %q is assigned to both %s and %s", entity, holder, id)
		}
		claimed[entity] = id
		desired[id.String()] = entity
	}

	current := make(map[string]string)
	rows, err := tx.Query(`SELECT id, COALESCE(ha_person_entity, '') FROM contacts WHERE deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("list contacts while replacing ha person bindings: %w", err)
	}
	for rows.Next() {
		var id, entity string
		if err := rows.Scan(&id, &entity); err != nil {
			rows.Close()
			return fmt.Errorf("scan contact while replacing ha person bindings: %w", err)
		}
		current[id] = entity
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close contacts while replacing ha person bindings: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list contacts while replacing ha person bindings: %w", err)
	}

	for _, id := range ids {
		if _, exists := current[id.String()]; !exists {
			return fmt.Errorf("replace ha person bindings: contact %s not found", id)
		}
	}

	changed := make([]string, 0)
	for id, currentEntity := range current {
		if currentEntity != desired[id] {
			changed = append(changed, id)
		}
	}
	if len(changed) == 0 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit unchanged ha person bindings: %w", err)
		}
		return nil
	}
	sort.Strings(changed)

	// Clear changed rows first so swapping two existing person claims is
	// legal under the unique index. One timestamp across the transaction
	// gives CardDAV a stable ETag/CTag change without churning unchanged
	// contacts on every restart.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range changed {
		if _, err := tx.Exec(
			`UPDATE contacts SET ha_person_entity = NULL, rev = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
			now, now, id,
		); err != nil {
			return fmt.Errorf("clear ha person binding for %s: %w", id, err)
		}
	}
	for _, id := range changed {
		entity := desired[id]
		if entity == "" {
			continue
		}
		parsed, err := uuid.Parse(id)
		if err != nil {
			return fmt.Errorf("parse contact id %q while replacing ha person bindings: %w", id, err)
		}
		if err := applyHAPersonEntity(tx, parsed, entity); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replacing ha person bindings: %w", err)
	}
	return nil
}

// sqlRunner is the subset of database/sql shared by *sql.DB and
// *sql.Tx, so the binding write can run standalone or inside the
// atomic CardDAV upsert transaction.
type sqlRunner interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// applyHAPersonEntity validates and writes one contact's HA person
// binding, mapping the uniqueness constraint to an error that names
// the current holder.
func applyHAPersonEntity(run sqlRunner, id uuid.UUID, entity string) error {
	entity = strings.TrimSpace(entity)
	if entity != "" && !haPersonEntityRE.MatchString(entity) {
		return fmt.Errorf("ha person entity must match person.<object_id> (lowercase letters, digits, underscores), got %q", entity)
	}
	res, err := run.Exec(
		`UPDATE contacts SET ha_person_entity = ? WHERE id = ? AND deleted_at IS NULL`,
		entity, id.String(),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			var name, holder string
			if ferr := run.QueryRow(
				`SELECT formatted_name, id FROM contacts WHERE ha_person_entity = ? AND deleted_at IS NULL`,
				entity,
			).Scan(&name, &holder); ferr == nil {
				return fmt.Errorf("ha person entity %q is already bound to contact %q (%s); clear that binding first — presence must attach to exactly one counterparty", entity, name, holder)
			}
			return fmt.Errorf("ha person entity %q is already bound to another contact; clear that binding first", entity)
		}
		return fmt.Errorf("set ha person entity for %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set ha person entity for %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("set ha person entity for %s: contact not found", id)
	}
	return nil
}

// HAPersonEntity returns the contact's bound Home Assistant person
// entity; empty when unbound. A missing contact is not an error — the
// boolean reports existence.
func (s *Store) HAPersonEntity(id uuid.UUID) (string, bool, error) {
	var entity sql.NullString
	err := s.db.QueryRow(
		`SELECT ha_person_entity FROM contacts WHERE id = ? AND deleted_at IS NULL`,
		id.String(),
	).Scan(&entity)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("ha person entity for %s: %w", id, err)
	}
	return entity.String, true, nil
}

// FindByHAPersonEntity returns the contact bound to the given Home
// Assistant person entity, or nil when no contact claims it.
func (s *Store) FindByHAPersonEntity(entity string) (*Contact, error) {
	entity = strings.TrimSpace(entity)
	if entity == "" {
		return nil, fmt.Errorf("entity is required")
	}
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM contacts WHERE ha_person_entity = ? AND deleted_at IS NULL`,
		entity,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find contact by ha person entity %q: %w", entity, err)
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("find contact by ha person entity %q: %w", entity, err)
	}
	return s.Get(parsed)
}

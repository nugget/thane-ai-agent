package contacts

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// SetHAPersonEntity binds a contact to a Home Assistant person entity
// (e.g. "person.alice") — the counterparty edge presence flows through
// (#1450). An empty entity clears the binding.
//
// Custody: no model-facing tool exposes this mutation, deliberately.
// Bindings participate in counterparty attribution, and their write
// paths stay operator-gated; wire this only from configuration-driven
// or operator-initiated code.
func (s *Store) SetHAPersonEntity(id uuid.UUID, entity string) error {
	entity = strings.TrimSpace(entity)
	if entity != "" && !strings.HasPrefix(entity, "person.") {
		return fmt.Errorf("ha person entity must be a person.* entity id, got %q", entity)
	}
	res, err := s.db.Exec(
		`UPDATE contacts SET ha_person_entity = ? WHERE id = ? AND deleted_at IS NULL`,
		entity, id.String(),
	)
	if err != nil {
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

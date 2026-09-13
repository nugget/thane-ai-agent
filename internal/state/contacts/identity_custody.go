package contacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Identity custody.
//
// EMAIL, TEL and IMPP are the properties the channel resolvers match a
// sender or recipient on: EMAIL for email, TEL and IMPP signal: for
// Signal. The record that holds an address lends its trust zone, and
// the operator's IsOwner, to whoever writes from that address, so
// which record holds one is authority, not contact data.
//
// The store applies no identity custody. CardDAV, /v1/contacts and
// init are operator writers and keep full power. Every model-facing
// writer (contact_save, contact_import_vcf, contact_forget) goes
// through the checks in this file.

const (
	identityEmail = "EMAIL"
	identityTel   = "TEL"
	identityIMPP  = "IMPP"
)

// identityPropertyFor canonicalizes a fact key. It returns the vCard
// property an identity key lands on (EMAIL, TEL or IMPP) and true, or
// "" and false for any other key. The lower-cased aliases in
// propertyKeys (email, phone, signal, matrix) are checked first, then
// the upper-cased key itself, so "Email", "tel" and "IMPP" are caught
// in any case.
func identityPropertyFor(key string) (string, bool) {
	trimmed := strings.TrimSpace(key)
	if property, ok := propertyKeys[strings.ToLower(trimmed)]; ok {
		return property, true
	}
	switch upper := strings.ToUpper(trimmed); upper {
	case identityEmail, identityTel, identityIMPP:
		return upper, true
	}
	return "", false
}

// imppSchemeFor returns the IMPP URI scheme an alias key implies
// (signal or matrix), or "" when the key names IMPP directly and the
// value carries its own scheme.
func imppSchemeFor(key string) string {
	switch alias := strings.ToLower(strings.TrimSpace(key)); alias {
	case "signal", "matrix":
		return alias
	}
	return ""
}

// isIdentityProperty reports whether a stored or decoded property name
// is an identity property, in any case.
func isIdentityProperty(property string) bool {
	switch strings.ToUpper(strings.TrimSpace(property)) {
	case identityEmail, identityTel, identityIMPP:
		return true
	}
	return false
}

// Identity custody reasons, one per rule a refused value broke.
const (
	// IdentityReasonZone means the target record is above known, or its
	// zone is malformed, so an address on it would carry that zone.
	IdentityReasonZone = "zone"

	// IdentityReasonOperator means the target is the operator's own
	// record, whose addresses make a turn the operator's own.
	IdentityReasonOperator = "operator"

	// IdentityReasonHolder means a record that carries authority (above
	// known, or the operator's) already holds the value, and a second
	// holder would unmatch it.
	IdentityReasonHolder = "holder"
)

// IdentityViolation is one address or number a model-facing writer may
// not add to a contact.
type IdentityViolation struct {
	// Key is the fact key as the model wrote it. It is empty on the
	// vCard import path, where values arrive as decoded properties.
	Key string

	// Property is the canonical identity property (EMAIL, TEL or IMPP)
	// the refused value would have been stored under.
	Property string

	// Value is the refused value.
	Value string

	// Reason is one of IdentityReasonZone, IdentityReasonOperator or
	// IdentityReasonHolder.
	Reason string

	// Holder is the record that already holds the value. It is set only
	// for IdentityReasonHolder.
	Holder *IdentityHolder
}

// IdentityHolder is an existing record that holds an address or number
// with authority behind it.
type IdentityHolder struct {
	// ID is the holder's contact UUID.
	ID uuid.UUID

	// Name is the holder's formatted name.
	Name string

	// Zone is the holder's trust zone.
	Zone string

	// Operator reports whether the holder is the operator's own record.
	Operator bool

	// Property and Value are the row the holder carries, which may be
	// an equivalent spelling of the refused value (TEL without '+' for
	// a signal number, for example).
	Property string
	Value    string
}

// IdentityCustodyError is a model-facing contact write refused by
// identity custody. It lists every refused value; nothing was written.
type IdentityCustodyError struct {
	// Violations lists every refused value, in the order the writer
	// offered them.
	Violations []IdentityViolation
}

// Error summarizes the refusal. contact_save renders the teaching form.
func (e *IdentityCustodyError) Error() string {
	return fmt.Sprintf("identity custody refused %d address(es)/number(s)", len(e.Violations))
}

// errContactChangedConcurrently reports that the record a model-facing
// save read had its trust zone reassigned, or was deleted, before the
// save committed. Nothing was written.
var errContactChangedConcurrently = errors.New("contact changed while the save was in flight")

// identityGuard carries what a model-facing write needs to apply
// identity custody to one target record.
type identityGuard struct {
	// operatorID is the operator's own record: the configured
	// operator_contact_id, or the resolved legacy owner name. uuid.Nil
	// when neither is configured.
	operatorID uuid.UUID

	// snapshotZone is the target's trust zone as the writer read it.
	// applyContactSave aborts when the stored zone differs, so the
	// target rule always judges the zone that is committed.
	snapshotZone string

	// liftTargetCustody lifts the target rule for the operator's own
	// contact_save turn. The holder rule still applies.
	liftTargetCustody bool
}

// queryFunc is the shape shared by (*sql.DB).Query and (*sql.Tx).Query,
// so the same checks run inside applyContactSave's transaction and on
// the import path.
type queryFunc func(query string, args ...any) (*sql.Rows, error)

// identityViolations returns every identity value in props that a
// model-facing writer may not add to target (uuid.Nil for a record
// being created). Non-identity properties and values the target
// already holds under the same property are skipped, so re-saving a
// held address stays a no-op.
//
// Target rule: nothing is added to an existing record whose zone is not
// known, or to the operator's record at any zone, unless the guard
// lifts it. Holder rule: nothing is added that another active record
// holds when that record is above known or is the operator's.
func identityViolations(query queryFunc, target uuid.UUID, guard identityGuard, props []Property) ([]IdentityViolation, error) {
	var violations []IdentityViolation
	for _, p := range props {
		if !isIdentityProperty(p.Property) {
			continue
		}
		property := strings.ToUpper(strings.TrimSpace(p.Property))
		if target != uuid.Nil {
			held, err := targetHolds(query, target, property, p.Value)
			if err != nil {
				return nil, err
			}
			if held {
				continue
			}
			if reason := targetCustodyReason(target, guard); reason != "" {
				violations = append(violations, IdentityViolation{Property: property, Value: p.Value, Reason: reason})
				continue
			}
		}
		holder, err := authorityHolder(query, target, guard.operatorID, property, p.Value)
		if err != nil {
			return nil, err
		}
		if holder != nil {
			violations = append(violations, IdentityViolation{Property: property, Value: p.Value, Reason: IdentityReasonHolder, Holder: holder})
		}
	}
	return violations, nil
}

// targetCustodyReason applies the target rule to an existing record.
func targetCustodyReason(target uuid.UUID, guard identityGuard) string {
	if guard.liftTargetCustody {
		return ""
	}
	if guard.operatorID != uuid.Nil && target == guard.operatorID {
		return IdentityReasonOperator
	}
	if guard.snapshotZone != ZoneKnown {
		return IdentityReasonZone
	}
	return ""
}

// targetHolds reports whether the target already carries this exact
// value under the canonical property. A legacy row stored under another
// spelling does not count, so a save cannot turn it live.
func targetHolds(query queryFunc, target uuid.UUID, property, value string) (bool, error) {
	rows, err := query(`
		SELECT COUNT(*) FROM contact_properties
		WHERE contact_id = ? AND property = ? AND LOWER(value) = LOWER(?)
	`, target.String(), property, value)
	if err != nil {
		return false, fmt.Errorf("check held %s: %w", property, err)
	}
	defer rows.Close()
	var count int
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return false, fmt.Errorf("check held %s: %w", property, err)
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("check held %s: %w", property, err)
	}
	return count > 0, nil
}

// authorityHolder returns the first record other than target that holds
// an equivalent of the value and carries authority: a zone other than
// known (a malformed zone counts) or the operator's own record. Holder
// rows match in any property spelling, because a CardDAV round trip
// makes a verbatim legacy spelling live.
func authorityHolder(query queryFunc, target, operatorID uuid.UUID, property, value string) (*IdentityHolder, error) {
	for _, eq := range identityEquivalents(property, value) {
		candidates, err := identityHolders(query, target, eq)
		if err != nil {
			return nil, err
		}
		for _, h := range candidates {
			h.Operator = operatorID != uuid.Nil && h.ID == operatorID
			if h.Zone != ZoneKnown || h.Operator {
				return &h, nil
			}
		}
	}
	return nil, nil
}

// identityHolders lists the active records other than target that hold
// one equivalent spelling, read fully so no cursor stays open inside a
// transaction.
func identityHolders(query queryFunc, target uuid.UUID, eq Property) ([]IdentityHolder, error) {
	rows, err := query(`
		SELECT c.id, c.formatted_name, COALESCE(c.trust_zone, ''), p.property, p.value
		FROM contact_properties p
		JOIN contacts c ON c.id = p.contact_id
		WHERE c.deleted_at IS NULL
		  AND c.id <> ?
		  AND UPPER(p.property) = ?
		  AND LOWER(p.value) = LOWER(?)
		ORDER BY c.formatted_name, c.id
	`, target.String(), eq.Property, eq.Value)
	if err != nil {
		return nil, fmt.Errorf("find %s holders: %w", eq.Property, err)
	}
	defer rows.Close()
	var holders []IdentityHolder
	for rows.Next() {
		var id string
		var h IdentityHolder
		if err := rows.Scan(&id, &h.Name, &h.Zone, &h.Property, &h.Value); err != nil {
			return nil, fmt.Errorf("scan %s holder: %w", eq.Property, err)
		}
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("parse %s holder id %q: %w", eq.Property, id, err)
		}
		h.ID = parsed
		holders = append(holders, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find %s holders: %w", eq.Property, err)
	}
	return holders, nil
}

// identityEquivalents lists the spellings under which a resolver would
// match the same identity, mirroring resolveContactByChannelLink in
// internal/app/adapters.go: EMAIL matches itself case-insensitively,
// and a phone number is looked up as TEL and as IMPP signal:, each with
// and without a leading '+'. Other IMPP values match themselves.
func identityEquivalents(property, value string) []Property {
	value = strings.TrimSpace(value)
	switch property {
	case identityTel:
		return phoneEquivalents(value)
	case identityIMPP:
		const signalScheme = "signal:"
		if len(value) >= len(signalScheme) && strings.EqualFold(value[:len(signalScheme)], signalScheme) {
			return phoneEquivalents(value[len(signalScheme):])
		}
	}
	return []Property{{Property: property, Value: value}}
}

func phoneEquivalents(number string) []Property {
	n := strings.TrimPrefix(strings.TrimSpace(number), "+")
	return []Property{
		{Property: identityTel, Value: n},
		{Property: identityTel, Value: "+" + n},
		{Property: identityIMPP, Value: "signal:" + n},
		{Property: identityIMPP, Value: "signal:+" + n},
	}
}

// checkSaveSnapshot re-reads the target inside the save transaction and
// returns errContactChangedConcurrently when it is gone, soft-deleted,
// or no longer at the zone the writer read. Without it the snapshot
// re-upsert would revert a concurrent operator zone change or resurrect
// a deleted contact.
func checkSaveSnapshot(tx *sql.Tx, id uuid.UUID, snapshotZone string) error {
	var zone, deletedAt sql.NullString
	err := tx.QueryRow(`SELECT trust_zone, deleted_at FROM contacts WHERE id = ?`, id.String()).Scan(&zone, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errContactChangedConcurrently
	}
	if err != nil {
		return fmt.Errorf("re-read contact %s: %w", id, err)
	}
	if deletedAt.Valid || zone.String != snapshotZone {
		return errContactChangedConcurrently
	}
	return nil
}

// deleteIfUncustodied soft-deletes a contact only while it is still at
// known and bound to no Home Assistant person, so an operator promotion
// or binding that lands between contact_forget's check and its write is
// never undone by the model. It reports whether a record was deleted. A
// ctx that has already ended deletes nothing, but a ctx that ends
// mid-statement can come back as its error after the delete committed,
// so a caller that reports the outcome passes a ctx that cannot end.
func (s *Store) deleteIfUncustodied(ctx context.Context, id uuid.UUID) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE contacts SET deleted_at = ?
		WHERE id = ? AND deleted_at IS NULL AND trust_zone = ? AND COALESCE(ha_person_entity, '') = ''
	`, time.Now().UTC().Format(time.RFC3339), id.String(), ZoneKnown)
	if err != nil {
		return false, fmt.Errorf("delete contact %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete contact %s: %w", id, err)
	}
	if affected == 0 {
		return false, nil
	}
	s.rebuildFTS()
	return true, nil
}

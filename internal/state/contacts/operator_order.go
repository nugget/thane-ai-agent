package contacts

import (
	"context"

	"github.com/google/uuid"
)

// Operator-first name resolution.
//
// Every notification, decision request, name lookup and conversation
// context resolves a name through [Store.ResolveContact]. When several
// active contacts answer to a name, as a formatted name or a nickname,
// the operator's own record wins, at any zone, then one above known,
// then a formatted-name match before a nickname match, then the lowest
// id. A known record whose formatted name is another record's nickname
// therefore never shadows the record with authority. When
// operator_contact_id or the legacy owner name is configured, the store
// learns who the operator is from the contact tools, which pin the same
// record identity custody protects and the channel resolver marks
// IsOwner. Under the sole-admin fallback nothing is pinned, so a shared
// name orders by zone, then match kind, then id, and the admin the
// resolver marks IsOwner does not outrank another contact above known.
// The legacy owner name itself is resolved before any pin exists, so it
// orders by zone and match kind alone.

// authorityOrderSQL is an ORDER BY term that puts the pinned operator's
// record first, then records above known (or with a malformed zone),
// then records at known. It takes the arguments [Store.authorityOrderArgs]
// returns, in that order.
const authorityOrderSQL = `CASE WHEN id = ? THEN 0 WHEN COALESCE(trust_zone, '') = ? THEN 2 ELSE 1 END`

// authorityOrderArgs are the arguments [authorityOrderSQL] takes.
func (s *Store) authorityOrderArgs() []any {
	return []any{s.operatorOrderID(), ZoneKnown}
}

// findByNameOrNickname returns the active contact that answers to name
// as its formatted name or its nickname, compared with LOWER as the
// resolver always has, in the order this file describes. Returns
// sql.ErrNoRows when no active contact answers to it.
func (s *Store) findByNameOrNickname(ctx context.Context, name string) (*Contact, error) {
	args := []any{name, name}
	args = append(args, s.authorityOrderArgs()...)
	args = append(args, name)
	return s.scanContact(s.db.QueryRowContext(ctx,
		`SELECT `+contactColumns+` FROM contacts
		WHERE `+activeFilter+` AND (LOWER(formatted_name) = LOWER(?) OR LOWER(nickname) = LOWER(?))
		ORDER BY `+authorityOrderSQL+`,
			CASE WHEN LOWER(formatted_name) = LOWER(?) THEN 0 ELSE 1 END,
			id
		LIMIT 1`,
		args...))
}

// pinOperatorContactID records the operator's own record for
// [Store.FindByNickname] ordering. uuid.Nil clears it.
func (s *Store) pinOperatorContactID(id uuid.UUID) {
	s.operatorID.Store(&id)
}

// operatorOrderID is the operator's id as nickname ordering compares
// it, or "" when no operator is pinned, which no contact id equals.
func (s *Store) operatorOrderID() string {
	id := s.operatorID.Load()
	if id == nil || *id == uuid.Nil {
		return ""
	}
	return id.String()
}

// pinnedOperatorID is the operator record the tools were configured
// with, chosen exactly as [Tools.custodyOperatorID] chooses it: the
// configured operator_contact_id, then the pinned legacy owner record.
// It is uuid.Nil when neither is configured, and when the legacy owner
// name is unpinned, since that is resolved again on every call.
func (t *Tools) pinnedOperatorID() uuid.UUID {
	if t.operatorContactID != uuid.Nil {
		return t.operatorContactID
	}
	if t.legacyOperatorPinned {
		return t.legacyOperatorID
	}
	return uuid.Nil
}

// pinStoreOperator passes the tools' operator to the store they share
// with every other name resolver, so a nickname the operator shares
// resolves to the operator on every path, not only in custody.
func (t *Tools) pinStoreOperator() {
	t.store.pinOperatorContactID(t.pinnedOperatorID())
}

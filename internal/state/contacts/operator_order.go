package contacts

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// Operator-first name resolution.
//
// Every notification, decision request, name lookup and conversation
// context resolves a name through [Store.ResolveContact]. When several
// active contacts hold a name exactly, as a formatted name or a
// nickname, standing decides, in three bands: the operator's own
// record, at any zone, then records above known (a malformed zone
// counts), then records at known. A holder in a higher band wins
// outright, so a known record whose formatted name is another record's
// nickname never shadows the record with authority. Two or more
// distinct holders in the highest band any holder reaches are an
// exact tie, an [AmbiguousNameError], never a pick: whether a holder
// has the name as its formatted name or its nickname, and which id is
// lower, say nothing about which person is meant. One record that has
// the name as both is one holder. Standing ranks only records that
// hold the name exactly. When none does, the resolver takes the one
// record that answers to it by a given name or first word and ranks
// nothing, so a first name two records share resolves to neither (see
// name_resolve.go). When operator_contact_id or the legacy owner name
// is configured, the store learns who the operator is from the contact
// tools, which pin the same record identity custody protects and the
// channel resolver marks IsOwner. Under the sole-admin fallback nothing
// is pinned, so the admin the resolver marks IsOwner stands with every
// other contact above known, and a name it shares with one of them is
// a tie. The legacy owner name itself is resolved before any pin
// exists, so it has two bands only, and a tie there pins no operator.

// authorityOrderSQL is an ORDER BY term that puts the pinned operator's
// record first, then records above known (or with a malformed zone),
// then records at known, as [ContactForkMember.authorityRank] ranks
// them. [Store.FindByNickname] orders by it. It takes the arguments
// [Store.authorityOrderArgs] returns, in that order.
const authorityOrderSQL = `CASE WHEN id = ? THEN 0 WHEN COALESCE(trust_zone, '') = ? THEN 2 ELSE 1 END`

// authorityOrderArgs are the arguments [authorityOrderSQL] takes.
func (s *Store) authorityOrderArgs() []any {
	return []any{s.operatorOrderID(), ZoneKnown}
}

// findByNameOrNickname returns the one active contact that holds name
// exactly, as its formatted name or its nickname, in the highest band
// of standing any holder reaches, as this file describes. Both sides
// fold as [nameKey] folds a name: the stored name is trimmed of every
// rune strings.TrimSpace trims and lowered with LOWER, and compared
// with the name's key. So a stored name with space at its edge is
// still an exact holder, as the fork audit counts it, and a name with
// space at its edge still reaches the exact holders and their
// standing. Each holder is ranked with [ContactForkMember.authorityRank],
// the rank the fork audit and the short-form ambiguity order by.
// Returns an [AmbiguousNameError] with ExactTie set when two or more
// holders share that band, and sql.ErrNoRows when no active contact
// holds the name, or when name is blank.
func (s *Store) findByNameOrNickname(ctx context.Context, name string) (*Contact, error) {
	key := nameKey(name)
	if key == "" {
		return nil, sql.ErrNoRows
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+contactColumns+` FROM contacts
		WHERE `+activeFilter+` AND (LOWER(TRIM(formatted_name, ?)) = ? OR LOWER(TRIM(nickname, ?)) = ?)
		ORDER BY id`,
		edgeSpace, key, edgeSpace, key)
	if err != nil {
		return nil, fmt.Errorf("query exact name holders: %w", err)
	}
	defer rows.Close()
	holders, err := s.scanContacts(rows)
	if err != nil {
		return nil, fmt.Errorf("scan exact name holders: %w", err)
	}
	if len(holders) == 0 {
		return nil, sql.ErrNoRows
	}

	top := s.forkMember(holders[0]).authorityRank()
	for _, c := range holders[1:] {
		top = min(top, s.forkMember(c).authorityRank())
	}
	var tied []*Contact
	for _, c := range holders {
		if s.forkMember(c).authorityRank() == top {
			tied = append(tied, c)
		}
	}
	if len(tied) == 1 {
		return tied[0], nil
	}
	return nil, s.exactTieError(name, key, tied)
}

// forkMember is c as the fork audit ranks it, marked as the operator's
// own record when it is the pinned operator.
func (s *Store) forkMember(c *Contact) ContactForkMember {
	return ContactForkMember{
		ContactID: c.ID,
		Name:      c.FormattedName,
		TrustZone: c.TrustZone,
		Operator:  s.operatorOrderID() == c.ID.String(),
	}
}

// pinOperatorContactID records the operator's own record, which name
// resolution, the fork audit and [Store.FindByNickname] rank first.
// uuid.Nil clears it.
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

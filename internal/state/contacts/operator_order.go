package contacts

import "github.com/google/uuid"

// Operator-first name resolution.
//
// Every notification, decision request, name lookup and conversation
// context resolves a name through [Store.ResolveContact]. When several
// active contacts share a nickname, the operator's own record wins, at
// any zone, then one above known, then the lowest id. When
// operator_contact_id or the legacy owner name is configured, the store
// learns who the operator is from the contact tools, which pin the same
// record identity custody protects and the channel resolver marks
// IsOwner. Under the sole-admin fallback nothing is pinned, so a shared
// nickname orders by zone, then id, and the admin the resolver marks
// IsOwner does not outrank another contact above known.

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

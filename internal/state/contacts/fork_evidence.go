package contacts

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Evidence that two records are one person.
//
// A shared name key says only that a lookup can confuse two records.
// Address books save many different people by one first name, and a
// shared nickname such as "Mom" can belong to two people. So the fork
// audit and the second-dossier refusal treat two name siblings as one
// person saved twice only when the records themselves suggest it. That
// means one of three things. The two records hold a common address or
// number. Or one of them, neither the operator's nor carrying more
// authority than the other, holds no real address or number of its
// own; a placeholder on a reserved domain does not count. Or one of
// them is a known record, not the operator's, bound to a Home Assistant
// person, which is how a duplicate takes over someone's presence. Both
// production forks behind #1545 were empty known records of this kind.
// A sparse record with authority beside a known record that holds its
// own number is not evidence: a household child saved with no number
// and a plumber saved by the same first name are two people.

// addressKindIMPP marks an IMPP value that is not a signal: number.
const addressKindIMPP = "impp"

// directoryAddress is one identity address a record holds, folded the
// way the channel resolvers match it.
type directoryAddress struct {
	// kind is ForkKindEmail, ForkKindPhone, or addressKindIMPP.
	kind string
	// key is the folded address: an email lowercased as LOWER folds it,
	// a phone number's digits without a leading '+', or another IMPP
	// value lowercased.
	key string
	// raw is the value as stored, trimmed.
	raw string
	// signal reports a phone number held as IMPP signal:.
	signal bool
	// mobile reports a phone number held as a TEL whose TYPE may be a
	// person's own phone (see [telMayBeMobile]).
	mobile bool
}

// placeholder reports an email address on a domain RFC 2606 or RFC 6761
// reserves, where no mailbox exists.
func (a directoryAddress) placeholder() bool {
	return a.kind == ForkKindEmail && reservedMailDomain(a.key)
}

// mobileTelTypes are the TEL TYPE values that name a mobile phone.
var mobileTelTypes = map[string]bool{"cell": true, "mobile": true, "iphone": true}

// telMayBeMobile reports whether a TEL with this TYPE parameter may be
// one person's own phone: it names no type other than pref, or it names
// a mobile phone. A TEL typed only home, work, main, fax, pager, voice
// or the like is a shared line that a household or an office lists on
// several cards, and nobody sends Signal from it.
func telMayBeMobile(typ string) bool {
	fields := strings.FieldsFunc(strings.ToLower(typ), func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t'
	})
	typed := false
	for _, f := range fields {
		if mobileTelTypes[f] {
			return true
		}
		if f != "pref" {
			typed = true
		}
	}
	return !typed
}

// directoryAddressOf folds one EMAIL, TEL or IMPP row, and reports
// false for a blank value.
func directoryAddressOf(property, value, typ string) (directoryAddress, bool) {
	raw := strings.TrimSpace(value)
	if property == identityEmail {
		key := sqliteLower(raw)
		return directoryAddress{kind: ForkKindEmail, key: key, raw: raw}, key != ""
	}
	if eq := identityEquivalents(property, raw)[0]; eq.Property == identityTel {
		return directoryAddress{
			kind:   ForkKindPhone,
			key:    eq.Value,
			raw:    raw,
			signal: property == identityIMPP,
			mobile: property == identityTel && telMayBeMobile(typ),
		}, eq.Value != ""
	}
	key := sqliteLower(raw)
	return directoryAddress{kind: addressKindIMPP, key: key, raw: raw}, key != ""
}

// loadDirectoryAddresses reads every EMAIL, TEL and IMPP row of the
// active contacts and attaches each to its record in records. A row of
// a contact written after records was read is left out; the next read
// sees it.
func (s *Store) loadDirectoryAddresses(ctx context.Context, records []directoryRecord) error {
	index := make(map[uuid.UUID]int, len(records))
	for i, r := range records {
		index[r.member.ContactID] = i
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.contact_id, UPPER(TRIM(p.property)), p.value, COALESCE(p.type, '')
		FROM contact_properties p
		JOIN contacts c ON c.id = p.contact_id
		WHERE c.deleted_at IS NULL
		  AND UPPER(TRIM(p.property)) IN (?, ?, ?)
		ORDER BY p.contact_id, p.value
	`, identityEmail, identityTel, identityIMPP)
	if err != nil {
		return fmt.Errorf("query directory addresses: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, property, value, typ string
		if err := rows.Scan(&id, &property, &value, &typ); err != nil {
			return fmt.Errorf("scan directory address: %w", err)
		}
		parsed, err := uuid.Parse(id)
		if err != nil {
			return fmt.Errorf("parse contact id %q: %w", id, err)
		}
		i, ok := index[parsed]
		if !ok {
			continue
		}
		if a, ok := directoryAddressOf(property, value, typ); ok {
			records[i].addresses = append(records[i].addresses, a)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate directory addresses: %w", err)
	}
	return nil
}

// holdsRealAddressBesides reports whether r holds an address or number,
// other than a placeholder, that is not skip. A zero skip skips nothing.
func (r directoryRecord) holdsRealAddressBesides(skip directoryAddress) bool {
	for _, a := range r.addresses {
		if a.placeholder() || (a.kind == skip.kind && a.key == skip.key) {
			continue
		}
		return true
	}
	return false
}

// sharedAddress returns an address or number both records hold.
func sharedAddress(a, b directoryRecord) (directoryAddress, bool) {
	for _, x := range a.addresses {
		for _, y := range b.addresses {
			if x.kind == y.kind && x.key == y.key {
				return x, true
			}
		}
	}
	return directoryAddress{}, false
}

// thinDuplicate reports why r, beside its name sibling, looks like a
// copy of it rather than a person of its own, or "". r must be neither
// the operator's nor carry more authority than sibling, and then either
// hold no real address or number, or be a known record bound to a Home
// Assistant person.
func (r directoryRecord) thinDuplicate(sibling directoryRecord) string {
	if r.member.Operator || r.member.authorityRank() < sibling.member.authorityRank() {
		return ""
	}
	if !r.holdsRealAddressBesides(directoryAddress{}) {
		return fmt.Sprintf("%s holds no address or number of its own", echoForRefusal(r.member.Name))
	}
	if r.member.TrustZone == ZoneKnown && r.member.HAPersonEntity != "" {
		return fmt.Sprintf("%s is a known contact bound to Home Assistant person %s", echoForRefusal(r.member.Name), echoForRefusal(r.member.HAPersonEntity))
	}
	return ""
}

// onePersonEvidence says why two records that share a name key are
// likely one person saved twice, as the comment at the top of this file
// describes, or returns "" when nothing in the records suggests they
// are.
func onePersonEvidence(a, b directoryRecord) string {
	if shared, ok := sharedAddress(a, b); ok {
		return fmt.Sprintf("both hold %s", echoForRefusal(shared.raw))
	}
	if why := a.thinDuplicate(b); why != "" {
		return why
	}
	return b.thinDuplicate(a)
}

package contacts

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Name resolution reads name fields only.
//
// [Store.ResolveContact] first finds the active contacts whose
// formatted name or nickname is the name, folding both sides as
// [nameKey] does, so it finds every record the fork audit counts as
// holding the name exactly. The one holder in the highest band of
// standing operator_order.go describes is the answer; two or more
// distinct holders in that band are an [AmbiguousNameError] with
// ExactTie set, and the resolver reads no short form after one, since
// a first name is weaker than the tie it would be asked to settle.
// When no record holds the name exactly, it asks which active contacts
// answer to the name by a short form name_keys.go defines, the keys
// the fork audit groups by: a given name or the first word of a
// formatted name. It reads the short forms alone, so every candidate it
// names matched by one, and the ambiguity never contradicts the exact
// step it follows. Exactly one such contact is the answer. Two or more
// are an [AmbiguousNameError] whatever their zones: standing breaks a
// tie between contacts that hold a name exactly in different bands,
// and a first name two people share is not a tie it may break. Notes,
// AI summaries and organizations are never read here, since a word in
// one contact's note does not make that contact the person it names;
// [Store.Search] reads them, for callers that want a list of matches.

// maxAmbiguousNamed bounds the candidates an [AmbiguousNameError] names.
const maxAmbiguousNamed = 5

// NameCandidate is one active contact that answers to an ambiguous name.
type NameCandidate struct {
	ContactID uuid.UUID
	Name      string
	TrustZone string
	// Field is the name field the contact answers by, one of the
	// NameField constants.
	Field string
}

// AmbiguousNameError reports a name that resolves to none of the active
// contacts that answer to it.
type AmbiguousNameError struct {
	Name string
	// ExactTie reports which step found the ambiguity. True means two or
	// more contacts hold the name exactly, as a formatted name or
	// nickname, in the same band of standing, and no contact in a higher
	// band holds it. False means no contact holds it exactly and two or
	// more answer to it by a given name or the first word of a formatted
	// name.
	ExactTie bool
	// Candidates lists up to maxAmbiguousNamed of those contacts, the
	// operator's own first, then those above known, then by name and id.
	Candidates []NameCandidate
	// Total counts every active contact the ambiguity is between.
	Total int
}

// Error names each listed candidate with its contact_id, zone and the
// field it matched, where to find the ones it leaves out, and the two
// ways to name one of them instead.
func (e *AmbiguousNameError) Error() string {
	var b strings.Builder
	b.WriteString(e.summary())
	b.WriteString(". Retry with the contact_id of the one you mean where the tool takes contact_id, or with that contact's full formatted name where it takes only a name")
	if e.ExactTie {
		b.WriteString("; when the one you mean has this name as its formatted name, only its contact_id tells it apart, so where the tool takes only a name, ask the operator which of them should keep the name")
	}
	return b.String()
}

// summary says why the name resolves to none of the contacts, names
// each listed candidate with its contact_id, zone and the field it
// matched, and says where to find the ones it leaves out. It omits the
// retry Error ends with, for a caller whose next move is not a retry by
// name or contact_id, such as the legacy owner name.
func (e *AmbiguousNameError) summary() string {
	var b strings.Builder
	if e.ExactTie {
		fmt.Fprintf(&b, "ambiguous contact %q: %d active contacts at the same standing (%s) hold it exactly as a formatted name or nickname, and no contact with more authority holds it, so it resolves to none of them: ",
			echoForRefusal(e.Name), e.Total, e.tiedStanding())
	} else {
		fmt.Fprintf(&b, "ambiguous contact %q: no active contact's formatted name or nickname is exactly that, and %d answer to it by a given name or the first word of a formatted name, so it resolves to none of them: ",
			echoForRefusal(e.Name), e.Total)
	}
	for i, c := range e.Candidates {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%q (%s, contact_id %s, matched on %s)", echoForRefusal(c.Name), c.TrustZone, c.ContactID, c.Field)
	}
	if more := e.Total - len(e.Candidates); more > 0 {
		b.WriteString(e.continuation(more))
	}
	return b.String()
}

// tiedStanding names the band of standing an exact tie's candidates
// share. No two records can be the operator's own, so a tie is above
// known or at known; a malformed zone counts as above known.
func (e *AmbiguousNameError) tiedStanding() string {
	if len(e.Candidates) > 0 && e.Candidates[0].TrustZone == ZoneKnown {
		return "known"
	}
	return "above known"
}

// exactTieError reports the holders of name tied in one band of
// standing, ordered and bounded as a short-form ambiguity is, each
// with the exact field it holds the name by.
func (s *Store) exactTieError(name, key string, tied []*Contact) *AmbiguousNameError {
	members := make([]ContactForkMember, len(tied))
	fields := make(map[uuid.UUID]string, len(tied))
	for i, c := range tied {
		members[i] = s.forkMember(c)
		fields[c.ID] = recordNameKeys(c.FormattedName, c.Nickname, c.GivenName).matchField(key)
	}
	sort.SliceStable(members, func(i, j int) bool { return forkMemberLess(members[i], members[j]) })
	amb := &AmbiguousNameError{Name: name, ExactTie: true, Total: len(members)}
	for _, m := range members[:min(len(members), maxAmbiguousNamed)] {
		amb.Candidates = append(amb.Candidates, NameCandidate{
			ContactID: m.ContactID,
			Name:      m.Name,
			TrustZone: m.TrustZone,
			Field:     fields[m.ContactID],
		})
	}
	return amb
}

// continuation says where to find the more candidates the error leaves
// out. [Store.Search] lists up to SearchLimit contacts that answer to a
// name ahead of any other match, in the order an ambiguity lists them:
// the exact holders first, by standing, then the short-form holders.
// contact_lookup prints each one's contact_id, so a query by the name
// lists them all first while there are no more than SearchLimit. Past
// that the query lists only SearchLimit of them, and the error says so
// and what to do when the one the model wants is not among them, rather
// than send it to a list that may stop short of that contact.
func (e *AmbiguousNameError) continuation(more int) string {
	if e.Total <= SearchLimit {
		return fmt.Sprintf("; and %d more not listed: contact_lookup with query set to %q lists all %d of them first, ahead of any other match, each with its contact_id, so find the one you mean there",
			more, echoForRefusal(e.Name), e.Total)
	}
	return fmt.Sprintf("; and %d more not listed: contact_lookup with query set to %q lists only %d of the %d, each with its contact_id, so the one you mean may not be among them; if it is not, ask the operator for its full formatted name",
		more, echoForRefusal(e.Name), SearchLimit, e.Total)
}

// resolveByNameKeys is the second step of [Store.ResolveContact]: the
// one active contact that answers to name by a short form, a given name
// or the first word of a formatted name. It reads no exact key, since
// the first step has found none by then. It returns sql.ErrNoRows when
// none does and an [AmbiguousNameError] when two or more do.
func (s *Store) resolveByNameKeys(ctx context.Context, name string) (*Contact, error) {
	key := nameKey(name)
	if key == "" {
		return nil, sql.ErrNoRows
	}
	records, err := s.activeDirectoryNames(ctx)
	if err != nil {
		return nil, err
	}
	type match struct {
		record directoryRecord
		field  string
	}
	var matched []match
	for _, r := range records {
		if field := r.keys.shortField(key); field != "" {
			matched = append(matched, match{r, field})
		}
	}
	switch len(matched) {
	case 0:
		return nil, sql.ErrNoRows
	case 1:
		// A record forgotten since the read is not found by now, and get
		// says so with sql.ErrNoRows.
		return s.get(ctx, matched[0].record.member.ContactID)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return forkMemberLess(matched[i].record.member, matched[j].record.member)
	})
	amb := &AmbiguousNameError{Name: name, Total: len(matched)}
	for _, m := range matched[:min(len(matched), maxAmbiguousNamed)] {
		amb.Candidates = append(amb.Candidates, NameCandidate{
			ContactID: m.record.member.ContactID,
			Name:      m.record.member.Name,
			TrustZone: m.record.member.TrustZone,
			Field:     m.field,
		})
	}
	return nil, amb
}

// NameMatchField names the field by which c answers to name, as the
// resolver's name keys read it: one of the NameField constants, or ""
// when c does not answer to name.
func NameMatchField(c *Contact, name string) string {
	if c == nil {
		return ""
	}
	return recordNameKeys(c.FormattedName, c.Nickname, c.GivenName).matchField(nameKey(name))
}

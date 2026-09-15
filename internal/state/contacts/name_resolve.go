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
// [Store.ResolveContact] first finds the active contact whose formatted
// name or nickname is the name, in the authority order operator_order.go
// describes. When none is, it asks which active contacts answer to the
// name by the keys name_keys.go defines, the keys the fork audit groups
// by, which by then means a given name or the first word of a formatted
// name. Exactly one such contact is the answer. Two or more are an
// [AmbiguousNameError] whatever their zones: authority breaks a tie
// between contacts that hold a name exactly, and a first name two
// people share is not a tie it may break. Notes, AI summaries and
// organizations are never read here, since a word in one contact's note
// does not make that contact the person it names; [Store.Search] reads
// them, for callers that want a list of matches.

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

// AmbiguousNameError reports a name that no active contact holds as its
// formatted name or nickname and that two or more answer to by another
// name field, so it resolves to none of them.
type AmbiguousNameError struct {
	Name string
	// Candidates lists up to maxAmbiguousNamed of those contacts, the
	// operator's own first, then those above known, then by name and id.
	Candidates []NameCandidate
	// Total counts every active contact that answers to the name.
	Total int
}

// Error names each listed candidate with its contact_id, zone and the
// field it matched, and the two ways to name one of them instead.
func (e *AmbiguousNameError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ambiguous contact %q: no active contact's formatted name or nickname is exactly that, and %d answer to it by another name field, so it resolves to none of them: ",
		echoForRefusal(e.Name), e.Total)
	for i, c := range e.Candidates {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%q (%s, contact_id %s, matched on %s)", echoForRefusal(c.Name), c.TrustZone, c.ContactID, c.Field)
	}
	if more := e.Total - len(e.Candidates); more > 0 {
		fmt.Fprintf(&b, "; and %d more not listed", more)
	}
	b.WriteString(". Retry with the contact_id of the one you mean where the tool takes contact_id, or with that contact's full formatted name where it takes only a name")
	return b.String()
}

// resolveByNameKeys is the second step of [Store.ResolveContact]: the
// one active contact that answers to name by its name keys. It returns
// sql.ErrNoRows when none does and an [AmbiguousNameError] when two or
// more do.
func (s *Store) resolveByNameKeys(ctx context.Context, name string) (*Contact, error) {
	key := nameKey(name)
	if key == "" {
		return nil, sql.ErrNoRows
	}
	records, err := s.activeDirectoryNames(ctx)
	if err != nil {
		return nil, err
	}
	var matched []directoryRecord
	for _, r := range records {
		if r.keys.answersTo(key) {
			matched = append(matched, r)
		}
	}
	switch len(matched) {
	case 0:
		return nil, sql.ErrNoRows
	case 1:
		// A record forgotten since the read is not found by now, and get
		// says so with sql.ErrNoRows.
		return s.get(ctx, matched[0].member.ContactID)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return forkMemberLess(matched[i].member, matched[j].member)
	})
	amb := &AmbiguousNameError{Name: name, Total: len(matched)}
	for _, r := range matched[:min(len(matched), maxAmbiguousNamed)] {
		amb.Candidates = append(amb.Candidates, NameCandidate{
			ContactID: r.member.ContactID,
			Name:      r.member.Name,
			TrustZone: r.member.TrustZone,
			Field:     r.keys.matchField(key),
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

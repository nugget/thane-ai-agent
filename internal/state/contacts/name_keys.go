package contacts

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Name keys.
//
// A contact answers to a name when the name is its formatted name or
// its nickname, compared the way the resolver compares them: LOWER,
// which folds ASCII letters and nothing else, here after trimming edge
// space. Those are the record's exact keys, and every name lookup finds
// only one of two records that share one. A record's short forms are
// its given name and the first word of a formatted name of more than
// one word. A record whose whole formatted name is another record's
// short form ("Bob" beside "Bob Smith") is how a person saved twice
// under a first name looks, and a lookup by that first name finds only
// the short record. The fork audit ([Store.ContactForks]) and the
// second-dossier refusal find siblings by these keys, and judge whether
// siblings are one person by the evidence fork_evidence.go describes.
// [Store.ResolveContact] reads the same keys when no record holds a
// name exactly (see name_resolve.go), so what the resolver finds by a
// name and what the audit says answers to it cannot drift apart.

// Name fields a record answers to a name key by, as
// [nameKeys.matchField] names them.
const (
	NameFieldFormatted          = "formatted_name"
	NameFieldNickname           = "nickname"
	NameFieldGiven              = "given_name"
	NameFieldFormattedFirstWord = "formatted_name_first_word"
)

// nameKeys is one record's name keys.
type nameKeys struct {
	// formatted is the formatted-name key, or "" when the name is blank.
	formatted string
	// given is the given-name key, or "" when the given name is blank.
	given string
	// exact holds the formatted-name and nickname keys.
	exact []string
	// short holds the given-name key and the first word of the
	// formatted name.
	short []string
}

// nameKey folds a name as the resolver's LOWER comparison does, after
// trimming edge space.
func nameKey(name string) string {
	return sqliteLower(strings.TrimSpace(name))
}

// recordNameKeys returns the name keys of a record with these names.
func recordNameKeys(formattedName, nickname, givenName string) nameKeys {
	var k nameKeys
	k.formatted = nameKey(formattedName)
	k.given = nameKey(givenName)
	k.exact = appendNameKey(k.exact, k.formatted)
	k.exact = appendNameKey(k.exact, nameKey(nickname))
	k.short = appendNameKey(k.short, k.given)
	if words := strings.Fields(k.formatted); len(words) > 1 {
		k.short = appendNameKey(k.short, words[0])
	}
	return k
}

// appendNameKey adds key to keys unless it is blank or already there.
func appendNameKey(keys []string, key string) []string {
	if key == "" || containsNameKey(keys, key) {
		return keys
	}
	return append(keys, key)
}

func containsNameKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// answersTo reports whether a record with these keys answers to key,
// exactly or by a short form.
func (k nameKeys) answersTo(key string) bool {
	return key != "" && (containsNameKey(k.exact, key) || containsNameKey(k.short, key))
}

// matchField names the field by which a record with these keys answers
// to key, the formatted name first, then the nickname, the given name
// and the first word of the formatted name, or "" when it does not
// answer to key.
func (k nameKeys) matchField(key string) string {
	switch {
	case !k.answersTo(key):
		return ""
	case key == k.formatted:
		return NameFieldFormatted
	case containsNameKey(k.exact, key):
		return NameFieldNickname
	case key == k.given:
		return NameFieldGiven
	}
	return NameFieldFormattedFirstWord
}

// sharedNameKey returns a name key two records share and true, or ""
// and false: an exact key both hold, or one record's whole formatted
// name that is a short form of the other.
func sharedNameKey(a, b nameKeys) (string, bool) {
	for _, k := range a.exact {
		if containsNameKey(b.exact, k) {
			return k, true
		}
	}
	if a.formatted != "" && containsNameKey(b.short, a.formatted) {
		return a.formatted, true
	}
	if b.formatted != "" && containsNameKey(a.short, b.formatted) {
		return b.formatted, true
	}
	return "", false
}

// directoryRecord is one active record as the fork audit and the
// sibling check read it.
type directoryRecord struct {
	member    ContactForkMember
	keys      nameKeys
	addresses []directoryAddress
}

// activeDirectoryRecords reads the names, zone, Home Assistant person
// binding and identity addresses of every active contact, in id order,
// marking the pinned operator's record.
func (s *Store) activeDirectoryRecords(ctx context.Context) ([]directoryRecord, error) {
	records, err := s.activeDirectoryNames(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.loadDirectoryAddresses(ctx, records); err != nil {
		return nil, err
	}
	return records, nil
}

// activeDirectoryNames reads what [Store.activeDirectoryRecords] reads
// except the identity addresses, which name resolution does not need.
func (s *Store) activeDirectoryNames(ctx context.Context) ([]directoryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(formatted_name, ''), COALESCE(nickname, ''), COALESCE(given_name, ''),
			COALESCE(trust_zone, ''), COALESCE(ha_person_entity, '')
		FROM contacts
		WHERE `+activeFilter+`
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("query directory names: %w", err)
	}
	defer rows.Close()

	operator := s.operatorOrderID()
	var records []directoryRecord
	for rows.Next() {
		var id, formatted, nickname, given, zone, entity string
		if err := rows.Scan(&id, &formatted, &nickname, &given, &zone, &entity); err != nil {
			return nil, fmt.Errorf("scan directory names: %w", err)
		}
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("parse contact id %q: %w", id, err)
		}
		records = append(records, directoryRecord{
			member: ContactForkMember{
				ContactID:      parsed,
				Name:           formatted,
				TrustZone:      zone,
				Operator:       operator != "" && id == operator,
				HAPersonEntity: entity,
			},
			keys: recordNameKeys(formatted, nickname, given),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate directory names: %w", err)
	}
	return records, nil
}

// nameSibling is an active record that shares a name key with another
// record, the key they share, and what suggests they are one person.
type nameSibling struct {
	ContactForkMember
	Key string
	// Evidence says why the two records are likely one person saved
	// twice (see fork_evidence.go), or is "" when nothing in the records
	// suggests they are, as with two people who share a first name.
	Evidence string
	keys     nameKeys
}

// nameSiblings returns c's own directory record and the active records
// other than c that share a name key with it, the operator's record
// first, then records above known, then by name and id.
func (s *Store) nameSiblings(ctx context.Context, c *Contact) (directoryRecord, []nameSibling, error) {
	records, err := s.activeDirectoryRecords(ctx)
	if err != nil {
		return directoryRecord{}, nil, err
	}
	self := directoryRecord{
		member: ContactForkMember{
			ContactID: c.ID,
			Name:      c.FormattedName,
			TrustZone: c.TrustZone,
			Operator:  s.operatorOrderID() == c.ID.String(),
		},
		keys: recordNameKeys(c.FormattedName, c.Nickname, c.GivenName),
	}
	for _, r := range records {
		if r.member.ContactID == c.ID {
			self = r
			break
		}
	}
	var siblings []nameSibling
	for _, r := range records {
		if r.member.ContactID == c.ID {
			continue
		}
		if key, ok := sharedNameKey(self.keys, r.keys); ok {
			siblings = append(siblings, nameSibling{
				ContactForkMember: r.member,
				Key:               key,
				Evidence:          onePersonEvidence(self, r),
				keys:              r.keys,
			})
		}
	}
	sort.SliceStable(siblings, func(i, j int) bool {
		return forkMemberLess(siblings[i].ContactForkMember, siblings[j].ContactForkMember)
	})
	return self, siblings, nil
}

// hasAuthority reports whether a record carries authority as identity
// custody counts it: a zone other than known (a malformed zone counts),
// or the operator's own record.
func (m ContactForkMember) hasAuthority() bool {
	return m.Operator || m.TrustZone != ZoneKnown
}

// authorityRank orders records by the authority they carry: 0 for the
// operator's own, 1 for a record above known, 2 for a known record.
func (m ContactForkMember) authorityRank() int {
	switch {
	case m.Operator:
		return 0
	case m.TrustZone != ZoneKnown:
		return 1
	}
	return 2
}

// forkMemberLess orders records the operator's first, then above known,
// then by name and id.
func forkMemberLess(a, b ContactForkMember) bool {
	if ra, rb := a.authorityRank(), b.authorityRank(); ra != rb {
		return ra < rb
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ContactID.String() < b.ContactID.String()
}

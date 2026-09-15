package contacts

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Contact fork kinds, as [ContactForkFinding.Kind] reports them.
const (
	// ForkKindName is one person saved as two or more records that
	// answer to a name key (see name_keys.go), as name_groups.go reads
	// people: the records with authority that share an address or
	// number or are each other's thin copy, and each copy that the
	// evidence fork_evidence.go describes ties to them. A copy tied to
	// two people is in each one's finding.
	ForkKindName = "name"
	// ForkKindSharedName is a formatted name or nickname that different
	// people each answer to, one of them with authority, with no sign
	// of being one person, such as two people both nicknamed "Mom". It
	// names one record per person; a copy that could be any of several
	// people stands for itself only while one of them is not named. A
	// name lookup reaches only the one with the most standing, as
	// operator_order.go ranks it, and none of them when two or more
	// share it, such as two household records.
	ForkKindSharedName = "shared_name"
	// ForkKindEmail is an email address two or more records hold, in a
	// shape that misroutes mail (see [emailGroupMisroutes]).
	ForkKindEmail = "email"
	// ForkKindPhone is a phone number two or more records hold as IMPP
	// signal: or as a TEL that may be a mobile phone, with or without a
	// leading '+'.
	ForkKindPhone = "phone"
	// ForkKindReservedDomain is an email address on a domain reserved
	// by RFC 2606 or RFC 6761, held by a record with authority. No
	// mailbox exists there, so it is a placeholder, not an address.
	ForkKindReservedDomain = "reserved_domain"
)

// forkKindOrder is the order findings are reported in, by kind.
var forkKindOrder = map[string]int{
	ForkKindName:           0,
	ForkKindSharedName:     1,
	ForkKindEmail:          2,
	ForkKindPhone:          3,
	ForkKindReservedDomain: 4,
}

// maxForkMembers bounds the records one finding names; its MemberCount
// still counts them all.
const maxForkMembers = 8

// reservedMailDomains are the names RFC 2606 and RFC 6761 reserve, so
// that no real mailbox exists at them or at any name under them.
var reservedMailDomains = []string{"example.com", "example.net", "example.org", "example", "test", "invalid", "localhost"}

// ContactForkMember is one record in a [ContactForkFinding].
type ContactForkMember struct {
	ContactID uuid.UUID `json:"contact_id"`
	Name      string    `json:"contact_name"`
	TrustZone string    `json:"trust_zone"`
	// Operator reports whether the record is the operator's own, as the
	// contact tools pinned it.
	Operator bool `json:"operator"`
	// HAPersonEntity is the Home Assistant person the record is bound
	// to, or "" when it is bound to none.
	HAPersonEntity string `json:"ha_person_entity,omitempty"`
}

// ContactForkFinding is one key that splits a person across records, one
// name several people answer to, or one placeholder address on a record
// with authority.
type ContactForkFinding struct {
	// Kind is one of the ForkKind constants.
	Kind string `json:"kind"`
	// Key is the shared name key, email address or phone number (digits
	// as stored, without a leading '+'), or the placeholder address.
	Key string `json:"key"`
	// Members are the finding's records, the operator's first, then
	// those above known, then by name and id; at most [maxForkMembers].
	// A shared-name finding names one record per person, and a
	// reserved-domain finding has exactly one.
	Members []ContactForkMember `json:"members"`
	// MemberCount counts all of the finding's records, including those
	// past the bound.
	MemberCount int `json:"member_count"`
}

// ContactForkAudit is what [Store.ContactForks] reports: a count of
// every finding and a bounded prefix of them.
type ContactForkAudit struct {
	// Total counts every finding, including those past the limit.
	Total int `json:"total"`
	// Findings holds the first findings in kind order (name,
	// shared_name, email, phone, reserved_domain), each kind by key;
	// never more than the limit the caller passed.
	Findings []ContactForkFinding `json:"findings"`
}

// ContactForks audits the active directory for records that split one
// person, and reports only what can misroute. Every group it reports
// includes a record above known (a malformed zone counts) or the
// operator's own, so ordinary known contacts never raise it.
//
//   - Name: records that share a name key (see name_keys.go) are read
//     as people (see name_groups.go). Records with authority that share
//     an address or number, or are each other's thin copy, are one
//     person. A record that the evidence
//     fork_evidence.go describes ties to a person any other way, one
//     without authority or a thin one with it, is a copy: it joins
//     every person it is tied to but never ties two into one. Each
//     person with two or more records
//     is a [ForkKindName] finding. Records that answer to the key as a
//     formatted name or nickname, one with authority, that no person
//     holds together are the key's one [ForkKindSharedName] finding,
//     naming a record per person, since a lookup reaches only the one
//     with the most standing, and none when two or more share it. A
//     short form alone between different people is not reported:
//     resolution takes a short form only when exactly one record
//     answers to it, so a first name two people share reaches neither
//     rather than the wrong one.
//   - Email: an address held by two or more records, when
//     [emailGroupMisroutes] says it can misroute mail.
//   - Phone: a number held by two or more records as IMPP signal: or as
//     a TEL that may be a mobile phone, with and without '+', as the
//     channel resolvers match it; once any holder has it as signal:,
//     every holder counts.
//   - Placeholder: an email address on a domain RFC 2606 or RFC 6761
//     reserves, held by a record with authority.
//
// It keeps at most limit findings (none when limit is zero or negative)
// and counts the rest. It reads every active record's names and
// identity addresses once, and writes nothing.
func (s *Store) ContactForks(ctx context.Context, limit int) (ContactForkAudit, error) {
	records, err := s.activeDirectoryRecords(ctx)
	if err != nil {
		return ContactForkAudit{}, err
	}
	findings := nameFindings(records)
	findings = append(findings, addressFindings(records)...)
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Kind != b.Kind {
			return forkKindOrder[a.Kind] < forkKindOrder[b.Kind]
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Members[0].ContactID.String() < b.Members[0].ContactID.String()
	})
	audit := ContactForkAudit{Total: len(findings)}
	if n := min(len(findings), max(limit, 0)); n > 0 {
		audit.Findings = append([]ContactForkFinding(nil), findings[:n]...)
	}
	return audit, nil
}

// addressHolders is the records that hold one address or number.
type addressHolders struct {
	// records are the holders' indexes, in first-seen order, and
	// counted marks those whose holding counts toward a phone group.
	records []int
	counted map[int]bool
	// address is the first holding seen, and signal reports whether
	// any holder has the number as IMPP signal:.
	address directoryAddress
	signal  bool
}

func (h *addressHolders) add(i int, a directoryAddress) {
	if h.counted == nil {
		h.counted = make(map[int]bool)
		h.address = a
	}
	if _, seen := h.counted[i]; !seen {
		h.records = append(h.records, i)
		h.counted[i] = false
	}
	if a.kind != ForkKindPhone || a.signal || a.mobile {
		h.counted[i] = true
	}
	h.signal = h.signal || a.signal
}

// addressFindings returns the email, phone and reserved-domain findings.
func addressFindings(records []directoryRecord) []ContactForkFinding {
	emails := make(map[string]*addressHolders)
	phones := make(map[string]*addressHolders)
	var out []ContactForkFinding
	for i, r := range records {
		placeholders := make(map[string]bool)
		for _, a := range r.addresses {
			switch a.kind {
			case ForkKindEmail:
				holdersFor(emails, a.key).add(i, a)
				if r.member.hasAuthority() && a.placeholder() && !placeholders[a.key] {
					placeholders[a.key] = true
					out = append(out, newForkFinding(ForkKindReservedDomain, a.raw, []ContactForkMember{r.member}))
				}
			case ForkKindPhone:
				holdersFor(phones, a.key).add(i, a)
			}
		}
	}
	for key, h := range emails {
		if len(h.records) >= 2 && groupHasAuthority(records, h.records) && emailGroupMisroutes(records, h) {
			out = append(out, newForkFinding(ForkKindEmail, key, membersOf(records, h.records)))
		}
	}
	for key, h := range phones {
		holders := h.records
		if !h.signal {
			holders = holders[:0:0]
			for _, i := range h.records {
				if h.counted[i] {
					holders = append(holders, i)
				}
			}
		}
		if len(holders) >= 2 && groupHasAuthority(records, holders) {
			out = append(out, newForkFinding(ForkKindPhone, key, membersOf(records, holders)))
		}
	}
	return out
}

func holdersFor(m map[string]*addressHolders, key string) *addressHolders {
	h := m[key]
	if h == nil {
		h = &addressHolders{}
		m[key] = h
	}
	return h
}

// emailGroupMisroutes reports whether an email address several records
// hold can misroute mail. It can when a holder is an ordinary known
// record, because the send gate then reads the address at known for
// every holder, or when a holder other than the operator's holds no
// other real address or number, the shape of a record saved twice. Two
// records with authority that each hold their own addresses share a
// mailbox, which email already reads as ambiguous at the least
// privileged zone, so that is not reported.
func emailGroupMisroutes(records []directoryRecord, h *addressHolders) bool {
	for _, i := range h.records {
		r := records[i]
		if r.member.Operator {
			continue
		}
		if r.member.TrustZone == ZoneKnown || !r.holdsRealAddressBesides(h.address) {
			return true
		}
	}
	return false
}

func groupHasAuthority(records []directoryRecord, holders []int) bool {
	for _, i := range holders {
		if records[i].member.hasAuthority() {
			return true
		}
	}
	return false
}

func membersOf(records []directoryRecord, holders []int) []ContactForkMember {
	members := make([]ContactForkMember, 0, len(holders))
	for _, i := range holders {
		members = append(members, records[i].member)
	}
	return members
}

// newForkFinding orders members, the operator's first, then those above
// known, then by name and id, and keeps at most [maxForkMembers].
func newForkFinding(kind, key string, members []ContactForkMember) ContactForkFinding {
	sort.Slice(members, func(i, j int) bool { return forkMemberLess(members[i], members[j]) })
	return ContactForkFinding{
		Kind:        kind,
		Key:         key,
		Members:     members[:min(len(members), maxForkMembers)],
		MemberCount: len(members),
	}
}

// reservedMailDomain reports whether a bare addr-spec's domain is one
// RFC 2606 or RFC 6761 reserves (example.com, example.net, example.org,
// and the example, test, invalid and localhost top-level domains), or a
// name under one. Callers pass a stored EMAIL value, never a raw header.
func reservedMailDomain(address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return false
	}
	domain := strings.TrimSuffix(address[at+1:], ".")
	for _, reserved := range reservedMailDomains {
		if domain == reserved || strings.HasSuffix(domain, "."+reserved) {
			return true
		}
	}
	return false
}

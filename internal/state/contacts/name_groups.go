package contacts

import "slices"

// Name groups.
//
// The fork audit reads the records that answer to one name key (see
// name_keys.go) as people. Two of them are tied when one has authority,
// they match on the key, and the evidence fork_evidence.go describes
// says they are one person. Two records with authority that share an
// address or number, or that are each other's thin copy (see
// [directoryRecord.thinDuplicate]), are one person. Any other tie makes
// a copy: the record without authority, or the thin one of two with
// authority, copies the other's person. A copy joins every person it
// is tied to, since a thin "Dave" beside a household "Dave Rivera" and
// a trusted "Dave Smith" is a copy of one of them and nothing in the
// records says which. Whatever its zone, it never ties two people into
// one, because that would conflate them through a thin copy. Each
// person with two or more records is a [ForkKindName] finding that
// lists exactly those records. Records that both hold the key as a
// formatted name or nickname, one with authority, but that no person
// holds together are different people who answer to one name, and the
// key's one [ForkKindSharedName] finding names a record for each of
// them. There a copy stands for the person it copies, and a copy of
// several people stands for itself unless each of them is already
// named.

// nameEntry is one record that answers to a name key: exactly, as its
// formatted name or nickname, or by a short form.
type nameEntry struct {
	record int
	exact  bool
}

// nameFindings groups records by exact name key, adds a record by its
// short form only to a key that is some record's whole formatted name,
// and reads each group of two or more as people with [newNameGroup].
func nameFindings(records []directoryRecord) []ContactForkFinding {
	formatted := make(map[string]bool, len(records))
	groups := make(map[string][]nameEntry)
	for i, r := range records {
		if r.keys.formatted != "" {
			formatted[r.keys.formatted] = true
		}
		for _, k := range r.keys.exact {
			groups[k] = append(groups[k], nameEntry{record: i, exact: true})
		}
	}
	for i, r := range records {
		for _, k := range r.keys.short {
			if formatted[k] && !containsNameKey(r.keys.exact, k) {
				groups[k] = append(groups[k], nameEntry{record: i})
			}
		}
	}
	var out []ContactForkFinding
	for key, entries := range groups {
		if len(entries) < 2 {
			continue
		}
		g := newNameGroup(records, key, entries)
		out = append(out, g.personFindings()...)
		if f, ok := g.sharedNameFinding(); ok {
			out = append(out, f)
		}
	}
	return out
}

// namePairMatches reports whether two records in one name group share
// key as [sharedNameKey] counts it: both hold it exactly, or one holds it
// as its whole formatted name and the other as a short form.
func namePairMatches(a directoryRecord, aExact bool, b directoryRecord, bExact bool, key string) bool {
	switch {
	case aExact && bExact:
		return true
	case aExact:
		return a.keys.formatted == key
	case bExact:
		return b.keys.formatted == key
	}
	return false
}

// nameGroup is one name key's entries read as people, as the comment at
// the top of this file describes. Entries are addressed by their index
// in entries.
type nameGroup struct {
	records []directoryRecord
	key     string
	entries []nameEntry
	// parent is a union-find forest that unites only entries with
	// authority; each person's root is its lowest entry.
	parent []int
	// ties holds, for each entry that is a copy, the entries with
	// authority whose people it copies.
	ties [][]int
	// exactPairs are the pairs of entries, one with authority, that
	// both hold key exactly.
	exactPairs [][2]int
}

// newNameGroup ties the entries of one name group pair by pair, in
// entry order.
func newNameGroup(records []directoryRecord, key string, entries []nameEntry) *nameGroup {
	g := &nameGroup{
		records: records,
		key:     key,
		entries: entries,
		parent:  make([]int, len(entries)),
		ties:    make([][]int, len(entries)),
	}
	for i := range g.parent {
		g.parent[i] = i
	}
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			g.judgePair(i, j)
		}
	}
	return g
}

// judgePair records what entries i and j say about each other when one
// has authority and they match on the key: an exact pair when both hold
// it exactly, and a tie when they are likely one person.
func (g *nameGroup) judgePair(i, j int) {
	a, b := g.entries[i], g.entries[j]
	ra, rb := g.records[a.record], g.records[b.record]
	authA, authB := ra.member.hasAuthority(), rb.member.hasAuthority()
	if (!authA && !authB) || !namePairMatches(ra, a.exact, rb, b.exact, g.key) {
		return
	}
	if a.exact && b.exact {
		g.exactPairs = append(g.exactPairs, [2]int{i, j})
	}
	if onePersonEvidence(ra, rb) == "" {
		return
	}
	switch {
	case authA && authB:
		g.tieAuthorities(i, j)
	case authA:
		g.ties[j] = append(g.ties[j], i)
	default:
		g.ties[i] = append(g.ties[i], j)
	}
}

// tieAuthorities ties entries i and j, both with authority, that the
// evidence says are likely one person. They are one person when they
// share an address or number or each is the other's thin copy; when
// only one is the other's thin copy, it is a copy of the other's person,
// so that a thin record above known beside two people never makes them
// one.
func (g *nameGroup) tieAuthorities(i, j int) {
	ra, rb := g.records[g.entries[i].record], g.records[g.entries[j].record]
	if _, shared := sharedAddress(ra, rb); !shared {
		copyA, copyB := ra.thinDuplicate(rb) != "", rb.thinDuplicate(ra) != ""
		switch {
		case copyA && !copyB:
			g.ties[i] = append(g.ties[i], j)
			return
		case copyB && !copyA:
			g.ties[j] = append(g.ties[j], i)
			return
		}
	}
	g.union(i, j)
}

func (g *nameGroup) root(i int) int {
	for g.parent[i] != i {
		i = g.parent[i]
	}
	return i
}

// union makes one person of entries i and j, rooted at the lower root.
func (g *nameGroup) union(i, j int) {
	ri, rj := g.root(i), g.root(j)
	if ri > rj {
		ri, rj = rj, ri
	}
	g.parent[rj] = ri
}

func (g *nameGroup) member(i int) ContactForkMember {
	return g.records[g.entries[i].record].member
}

// ownPerson returns the root of entry i's own person, and false for an
// entry without authority, which is only ever a copy.
func (g *nameGroup) ownPerson(i int) (int, bool) {
	if !g.member(i).hasAuthority() {
		return 0, false
	}
	return g.root(i), true
}

// copied returns the roots of the people entry i copies, other than its
// own, in the order first tied.
func (g *nameGroup) copied(i int) []int {
	own, hasOwn := g.ownPerson(i)
	var roots []int
	for _, t := range g.ties[i] {
		r := g.root(t)
		if (hasOwn && r == own) || slices.Contains(roots, r) {
			continue
		}
		roots = append(roots, r)
	}
	return roots
}

// people returns the roots of every person entry i belongs to: its own
// first, then those it copies.
func (g *nameGroup) people(i int) []int {
	if own, ok := g.ownPerson(i); ok {
		return append([]int{own}, g.copied(i)...)
	}
	return g.copied(i)
}

// joined reports whether entry i has authority and another entry is one
// person with it.
func (g *nameGroup) joined(i int) bool {
	own, ok := g.ownPerson(i)
	if !ok {
		return false
	}
	for j := range g.entries {
		if j != i && g.root(j) == own {
			return true
		}
	}
	return false
}

// ambiguous reports whether entry i copies two or more people and is
// one person with no other entry, so nothing says which of them it is.
func (g *nameGroup) ambiguous(i int) bool {
	return len(g.copied(i)) >= 2 && !g.joined(i)
}

// personFindings returns a [ForkKindName] finding for each person with
// two or more records, listing exactly those records.
func (g *nameGroup) personFindings() []ContactForkFinding {
	members := make(map[int][]ContactForkMember)
	var roots []int
	for i := range g.entries {
		for _, r := range g.people(i) {
			if _, seen := members[r]; !seen {
				roots = append(roots, r)
			}
			members[r] = append(members[r], g.member(i))
		}
	}
	var out []ContactForkFinding
	for _, r := range roots {
		if len(members[r]) >= 2 {
			out = append(out, newForkFinding(ForkKindName, g.key, members[r]))
		}
	}
	return out
}

// samePerson reports whether some person holds both entries.
func (g *nameGroup) samePerson(i, j int) bool {
	pj := g.people(j)
	for _, r := range g.people(i) {
		if slices.Contains(pj, r) {
			return true
		}
	}
	return false
}

// personKey names the person a [ForkKindSharedName] finding counts
// entry i as: its own person's root when it copies no one or is one
// person with another entry, the root of the one person it copies, and
// itself when it is [nameGroup.ambiguous] or an entry without authority
// tied to no one. Only an entry with authority is a root, and an
// ambiguous one is its own, so no two people share a key.
func (g *nameGroup) personKey(i int) int {
	own, hasOwn := g.ownPerson(i)
	copied := g.copied(i)
	switch {
	case hasOwn && (len(copied) == 0 || g.joined(i)):
		return own
	case len(copied) == 1:
		return copied[0]
	}
	return i
}

// sharedNameFinding returns the key's [ForkKindSharedName] finding:
// among the exact pairs no person holds together, one record for each
// person, the first as [forkMemberLess] orders them. An ambiguous copy
// of people who are all named that way is one of them, so it is left
// out. It reports false when fewer than two people remain.
func (g *nameGroup) sharedNameFinding() (ContactForkFinding, bool) {
	chosen := make(map[int]ContactForkMember)
	var people []int
	for _, pair := range g.exactPairs {
		if g.samePerson(pair[0], pair[1]) {
			continue
		}
		for _, i := range pair {
			person, m := g.personKey(i), g.member(i)
			current, seen := chosen[person]
			if !seen {
				people = append(people, person)
			}
			if !seen || forkMemberLess(m, current) {
				chosen[person] = m
			}
		}
	}
	people = slices.DeleteFunc(people, func(person int) bool {
		if !g.ambiguous(person) {
			return false
		}
		for _, r := range g.copied(person) {
			if _, named := chosen[r]; !named {
				return false
			}
		}
		return true
	})
	if len(people) < 2 {
		return ContactForkFinding{}, false
	}
	members := make([]ContactForkMember, 0, len(people))
	for _, person := range people {
		members = append(members, chosen[person])
	}
	return newForkFinding(ForkKindSharedName, g.key, members), true
}

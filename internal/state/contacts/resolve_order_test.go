package contacts

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// wantCandidate is one contact an ambiguous name must list, and the
// field it must say the contact matched on.
type wantCandidate struct{ name, field string }

// TestResolveContact_AuthorityOrder pins the order a name resolves in
// when several active records answer to it as a formatted name or a
// nickname: the pinned operator, then records above known, then a
// formatted-name match before a nickname match, then id. Each case
// resolves three times so the answer is shown to be stable.
func TestResolveContact_AuthorityOrder(t *testing.T) {
	type seed struct{ name, nickname, zone string }
	tests := []struct {
		name   string
		seeds  []seed
		pin    string // the record pinned as the operator, or ""
		lookup string
		want   string // the formatted name ResolveContact returns
	}{
		{
			name:  "the operator's nickname beats a known formatted name",
			seeds: []seed{{"Ally", "", ZoneKnown}, {"Alice Operator", "Ally", ZoneKnown}},
			pin:   "Alice Operator", lookup: "ally", want: "Alice Operator",
		},
		{
			name:  "the operator at known beats a household formatted name",
			seeds: []seed{{"Ally", "", ZoneHousehold}, {"Alice Operator", "ally", ZoneKnown}},
			pin:   "Alice Operator", lookup: "ALLY", want: "Alice Operator",
		},
		{
			name:   "a household nickname beats a known formatted name",
			seeds:  []seed{{"Carol", "", ZoneKnown}, {"Carol Household", "Carol", ZoneHousehold}},
			lookup: "Carol", want: "Carol Household",
		},
		{
			name:   "an admin nickname beats a known formatted name with no operator pinned",
			seeds:  []seed{{"Boss", "", ZoneKnown}, {"Alice Admin", "boss", ZoneAdmin}},
			lookup: "BOSS", want: "Alice Admin",
		},
		{
			name:   "within the rank above known a formatted name beats a nickname",
			seeds:  []seed{{"Robert Trusted", "Bob", ZoneTrusted}, {"Bob", "", ZoneHousehold}},
			lookup: "bob", want: "Bob",
		},
		{
			name:   "within known a formatted name beats a nickname",
			seeds:  []seed{{"Robert Known", "Bob", ZoneKnown}, {"Bob", "", ZoneKnown}},
			lookup: "Bob", want: "Bob",
		},
		{
			// A first name alone is not a name the resolver knows, so this
			// shape still resolves to the short record; the fork audit is
			// what reports it.
			name:   "a household record's first name does not outrank a known formatted name",
			seeds:  []seed{{"Dave", "", ZoneKnown}, {"Dave Household", "", ZoneHousehold}},
			lookup: "Dave", want: "Dave",
		},
		{
			name:   "a first word no record holds exactly resolves to the one record it begins",
			seeds:  []seed{{"Frank Known", "", ZoneKnown}, {"Grace Household", "", ZoneHousehold}},
			lookup: "Frank", want: "Frank Known",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			byName := make(map[string]*Contact, len(tt.seeds))
			for _, s := range tt.seeds {
				byName[s.name] = seedNicknameAt(t, store, s.name, s.nickname, s.zone)
			}
			if tt.pin != "" {
				store.pinOperatorContactID(byName[tt.pin].ID)
			}
			for range 3 {
				got, err := store.ResolveContact(tt.lookup)
				if err != nil || got.ID != byName[tt.want].ID {
					t.Fatalf("ResolveContact(%q) = %+v, %v, want %s", tt.lookup, got, err, tt.want)
				}
			}
		})
	}
}

// TestResolveContact_KnownOnlyCollisionsAreDeterministic pins that a
// nickname two known records share resolves to the lower id every time,
// whichever was written first.
func TestResolveContact_KnownOnlyCollisionsAreDeterministic(t *testing.T) {
	store := newTestStore(t)
	a := seedNicknameAt(t, store, "Kim One", "twin", ZoneKnown)
	b := seedNicknameAt(t, store, "Kim Two", "Twin", ZoneKnown)
	want := a.ID
	if b.ID.String() < a.ID.String() {
		want = b.ID
	}
	for range 3 {
		if got, err := store.ResolveContact("TWIN"); err != nil || got.ID != want {
			t.Fatalf("ResolveContact(TWIN) = %+v, %v, want %s", got, err, want)
		}
	}
}

// TestResolveContact_NameFieldsOnly pins what a name resolves to when
// no record holds it as a formatted name or nickname. It replaces the
// search-fallback tests, which pinned the old behaviour on purpose: such
// a name fell to a search of names, notes, AI summaries and
// organizations, and a single hit was taken as the person. Resolution
// now reads name fields only, through the fork audit's name keys, takes
// the one record that answers to the name by a given name or first
// word, and resolves a name two records answer to that way to neither,
// whatever their zones. Every case runs with and without FTS5, since
// resolution no longer touches either search path.
func TestResolveContact_NameFieldsOnly(t *testing.T) {
	type seed struct{ name, nickname, given, zone, note, summary, org string }
	type candidate = wantCandidate
	tests := []struct {
		name   string
		seeds  []seed
		lookup string
		// want is the formatted name the lookup resolves to; with neither
		// it nor wantAmbiguous set the lookup must be sql.ErrNoRows.
		want          string
		wantAmbiguous []candidate
		// searchFinds is a control: records Search finds for the lookup,
		// proving the free text is there for resolution to ignore.
		searchFinds []string
	}{
		{
			name: "a name only other records' notes, summaries and organizations mention resolves to none of them",
			seeds: []seed{
				{name: "Carol Rivera", zone: ZoneHousehold, note: "Dave's partner"},
				{name: "Eve Rivera", zone: ZoneHousehold, summary: "daughter of Dave and Carol"},
				{name: "Mallory Works", zone: ZoneKnown, org: "Dave Industries"},
			},
			lookup:      "Dave",
			searchFinds: []string{"Carol Rivera", "Eve Rivera", "Mallory Works"},
		},
		{
			name:        "a single note that mentions a name is not that person",
			seeds:       []seed{{name: "Carol Rivera", zone: ZoneHousehold, note: "Dave's partner"}},
			lookup:      "Dave",
			searchFinds: []string{"Carol Rivera"},
		},
		{
			name: "a unique given name resolves by short form",
			seeds: []seed{
				{name: "Dr. Alice Jones", given: "Alice", zone: ZoneHousehold},
				{name: "Bob Stone", zone: ZoneKnown, note: "Alice's neighbour"},
			},
			lookup: "alice", want: "Dr. Alice Jones",
			searchFinds: []string{"Dr. Alice Jones", "Bob Stone"},
		},
		{
			name:   "a unique first word resolves by short form",
			seeds:  []seed{{name: "Bob Stone", zone: ZoneKnown}, {name: "Carol Rivera", zone: ZoneKnown, note: "Bob's sister"}},
			lookup: "BOB", want: "Bob Stone",
		},
		{
			name:   "case and edge space fold as the fork audit folds them",
			seeds:  []seed{{name: "Bob Stone", zone: ZoneKnown}},
			lookup: "  bOb ", want: "Bob Stone",
		},
		{
			name: "a given name two records share is ambiguous, and authority does not break the tie",
			seeds: []seed{
				{name: "Dave Smith", given: "Dave", zone: ZoneKnown},
				{name: "Dave Rivera", given: "Dave", zone: ZoneHousehold},
			},
			lookup:        "Dave",
			wantAmbiguous: []candidate{{"Dave Rivera", NameFieldGiven}, {"Dave Smith", NameFieldGiven}},
		},
		{
			name: "a first word and a given name two records answer by are ambiguous, each naming its field",
			seeds: []seed{
				{name: "Eve Alpha", zone: ZoneHousehold},
				{name: "Eve Beta", given: "Eve", zone: ZoneKnown},
			},
			lookup:        "Eve",
			wantAmbiguous: []candidate{{"Eve Alpha", NameFieldFormattedFirstWord}, {"Eve Beta", NameFieldGiven}},
		},
		{
			name: "an exact holder still wins over short-form holders",
			seeds: []seed{
				{name: "Dave", zone: ZoneKnown},
				{name: "Dave Rivera", given: "Dave", zone: ZoneHousehold},
				{name: "Dave Smith", given: "Dave", zone: ZoneTrusted},
			},
			lookup: "Dave", want: "Dave",
		},
		{
			name: "exact holders still break a tie by authority",
			seeds: []seed{
				{name: "Carol", zone: ZoneKnown},
				{name: "Carol Household", nickname: "Carol", zone: ZoneHousehold},
				{name: "Carol Smith", given: "Carol", zone: ZoneTrusted},
			},
			lookup: "Carol", want: "Carol Household",
		},
		{
			name:        "a surname is not a name the resolver reads",
			seeds:       []seed{{name: "Dave Rivera", given: "Dave", zone: ZoneHousehold}},
			lookup:      "Rivera",
			searchFinds: []string{"Dave Rivera"},
		},
		{
			name:   "a multi-word miss matches none of its words",
			seeds:  []seed{{name: "Dave Rivera", given: "Dave", zone: ZoneHousehold}, {name: "Bob Stone", zone: ZoneKnown}},
			lookup: "Dave Stone",
		},
		{
			// Unfolded, the lookup would miss both exact holders and fall to
			// the short-form step, which would pick Carol Smith by given
			// name without a word of warning.
			name: "a name with edge space still reaches the exact holders and their authority order",
			seeds: []seed{
				{name: "Carol", zone: ZoneKnown},
				{name: "Alice Household", nickname: "Carol", zone: ZoneHousehold},
				{name: "Carol Smith", given: "Carol", zone: ZoneTrusted},
			},
			lookup: " Carol\t", want: "Alice Household",
		},
		{
			name: "a nickname stored with edge space is still an exact holder, as the fork audit counts it",
			seeds: []seed{
				{name: "Carol", zone: ZoneKnown},
				{name: "Carol Household", nickname: " Carol\n", zone: ZoneHousehold},
			},
			lookup: "Carol", want: "Carol Household",
		},
		{
			name: "a formatted name stored with any edge space strings.TrimSpace trims still beats a short form",
			seeds: []seed{
				{name: "Dave ", zone: ZoneKnown},
				{name: "Dave Rivera", given: "Dave", zone: ZoneHousehold},
			},
			lookup: "dave", want: "Dave ",
		},
		{
			name: "an edge-spaced first name two records share lists them by short form only",
			seeds: []seed{
				{name: "Dave Smith", given: "Dave", zone: ZoneKnown},
				{name: "Dave Rivera", given: "Dave", zone: ZoneHousehold},
			},
			lookup:        "Dave ",
			wantAmbiguous: []candidate{{"Dave Rivera", NameFieldGiven}, {"Dave Smith", NameFieldGiven}},
		},
		{
			name:   "a blank name answers to no one, not even a record whose nickname is blank",
			seeds:  []seed{{name: "Bob Stone", nickname: "  ", zone: ZoneKnown}},
			lookup: " \t ",
		},
		{
			name:   "a name nothing answers to is sql.ErrNoRows",
			seeds:  []seed{{name: "Bob Stone", zone: ZoneKnown}},
			lookup: "Nobody Here",
		},
	}
	for _, fts := range []bool{true, false} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s/fts=%v", tt.name, fts), func(t *testing.T) {
				store := newTestStore(t)
				store.ftsEnabled = fts
				byName := make(map[string]*Contact, len(tt.seeds))
				for _, s := range tt.seeds {
					c, err := store.UpsertWithProperties(&Contact{
						FormattedName: s.name, Nickname: s.nickname, GivenName: s.given, Kind: "individual",
						TrustZone: s.zone, Note: s.note, AISummary: s.summary, Org: s.org,
					}, nil)
					if err != nil {
						t.Fatalf("seed %s: %v", s.name, err)
					}
					byName[s.name] = c
				}
				requireSearchFinds(t, store, tt.lookup, tt.searchFinds, byName)

				got, err := store.ResolveContact(tt.lookup)
				switch {
				case tt.want != "":
					if err != nil || got.ID != byName[tt.want].ID {
						t.Fatalf("ResolveContact(%q) = %+v, %v, want %s", tt.lookup, got, err, tt.want)
					}
				case len(tt.wantAmbiguous) > 0:
					requireAmbiguous(t, err, tt.lookup, tt.wantAmbiguous, byName)
				default:
					if !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("ResolveContact(%q) = %+v, %v, want sql.ErrNoRows", tt.lookup, got, err)
					}
				}
			})
		}
	}
}

// requireSearchFinds fails unless Search finds every record in want.
func requireSearchFinds(t *testing.T, store *Store, query string, want []string, byName map[string]*Contact) {
	t.Helper()
	if len(want) == 0 {
		return
	}
	found, err := store.Search(query)
	if err != nil {
		t.Fatalf("control Search(%q): %v", query, err)
	}
	for _, name := range want {
		if !slices.ContainsFunc(found, func(c *Contact) bool { return c.ID == byName[name].ID }) {
			t.Errorf("control Search(%q) did not find %s, so the case does not prove resolution ignores it", query, name)
		}
	}
}

// requireAmbiguous fails unless err is an [AmbiguousNameError] listing
// exactly want, in order, each with its contact_id, zone and field in
// the text, and echoing the lookup as trimmed.
func requireAmbiguous(t *testing.T, err error, lookup string, want []wantCandidate, byName map[string]*Contact) {
	lookup = strings.TrimSpace(lookup)
	t.Helper()
	var ambiguous *AmbiguousNameError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("ResolveContact(%q) error = %v, want an AmbiguousNameError", lookup, err)
	}
	if ambiguous.Total != len(want) || len(ambiguous.Candidates) != len(want) {
		t.Fatalf("ambiguity lists %d of %d, want %d:\n%v", len(ambiguous.Candidates), ambiguous.Total, len(want), err)
	}
	for i, w := range want {
		c, seeded := ambiguous.Candidates[i], byName[w.name]
		if c.ContactID != seeded.ID || c.Name != seeded.FormattedName || c.TrustZone != seeded.TrustZone || c.Field != w.field {
			t.Errorf("candidate %d = %+v, want %s (%s, %s) matched on %s", i, c, w.name, seeded.TrustZone, seeded.ID, w.field)
		}
		entry := fmt.Sprintf("%q (%s, contact_id %s, matched on %s)", seeded.FormattedName, seeded.TrustZone, seeded.ID, w.field)
		if !strings.Contains(err.Error(), entry) {
			t.Errorf("error does not list %s:\n%v", entry, err)
		}
	}
	for _, s := range []string{fmt.Sprintf("ambiguous contact %q", lookup), "answer to it by a given name or the first word of a formatted name", "resolves to none of them", "Retry with the contact_id", "full formatted name"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error missing %q:\n%v", s, err)
		}
	}
}

// TestResolveContact_AmbiguityIsBounded pins that an ambiguous name
// lists at most maxAmbiguousNamed candidates, says how many it left
// out, and still counts them all.
func TestResolveContact_AmbiguityIsBounded(t *testing.T) {
	store := newTestStore(t)
	const total = maxAmbiguousNamed + 2
	for i := range total {
		seedContactAt(t, store, fmt.Sprintf("Eve %c", 'A'+i), ZoneKnown)
	}
	_, err := store.ResolveContact("Eve")
	var ambiguous *AmbiguousNameError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("ResolveContact(Eve) error = %v, want an AmbiguousNameError", err)
	}
	if ambiguous.Total != total || len(ambiguous.Candidates) != maxAmbiguousNamed {
		t.Errorf("listed %d of %d, want %d of %d", len(ambiguous.Candidates), ambiguous.Total, maxAmbiguousNamed, total)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d answer to it", total)) || !strings.Contains(err.Error(), "and 2 more not listed") {
		t.Errorf("error does not count what it left out:\n%v", err)
	}
	if next := fmt.Sprintf("contact_lookup with query set to %q lists all %d of them first", "Eve", total); !strings.Contains(err.Error(), next) {
		t.Errorf("error does not say how to find what it left out, want %q:\n%v", next, err)
	}
	if strings.Contains(err.Error(), "Eve G") {
		t.Errorf("error names a candidate past the bound:\n%v", err)
	}
}

// TestAmbiguousNameError_Continuation pins what an ambiguous name that
// leaves candidates out says about the rest: while they fit in one
// query, that query lists them all first; past SearchLimit, no lookup
// lists them all, and the error says so rather than send the model to a
// list that stops short of the one it wants.
func TestAmbiguousNameError_Continuation(t *testing.T) {
	tests := []struct {
		total     int
		want, not string
	}{
		{maxAmbiguousNamed + 2, `and 2 more not listed: contact_lookup with query set to "Eve" lists all 7 of them first, ahead of any other match`, "no lookup lists the rest"},
		{SearchLimit, fmt.Sprintf(`and %d more not listed: contact_lookup with query set to "Eve" lists all %d of them first`, SearchLimit-maxAmbiguousNamed, SearchLimit), "no lookup lists the rest"},
		{SearchLimit + 1, fmt.Sprintf(`lists only %d of the %d, and no lookup lists the rest, so ask the operator for the full formatted name`, SearchLimit, SearchLimit+1), "lists all"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("total=%d", tt.total), func(t *testing.T) {
			e := &AmbiguousNameError{Name: "Eve", Total: tt.total}
			for i := range maxAmbiguousNamed {
				e.Candidates = append(e.Candidates, NameCandidate{Name: fmt.Sprintf("Eve %c", 'A'+i), TrustZone: ZoneKnown, Field: NameFieldFormattedFirstWord})
			}
			got := e.Error()
			if !strings.Contains(got, tt.want) || strings.Contains(got, tt.not) {
				t.Errorf("error = %s\nwant it to contain %q and not %q", got, tt.want, tt.not)
			}
		})
	}
}

// TestResolveContact_ContinuationReachesEveryCandidate pins that an
// ambiguous name's continuation reaches every candidate the error leaves
// out, on both search paths. contact_lookup with the name as query lists
// all seven contacts that answer to "Alice" by a short form first, among
// them a record that answers only by its given name and whose formatted
// name does not contain it, ahead of more contacts than SearchLimit
// whose notes merely mention Alice.
func TestResolveContact_ContinuationReachesEveryCandidate(t *testing.T) {
	for _, fts := range []bool{true, false} {
		t.Run(fmt.Sprintf("fts=%v", fts), func(t *testing.T) {
			tools := newTestTools(t)
			tools.store.ftsEnabled = fts
			seed := func(c Contact) *Contact {
				t.Helper()
				c.Kind = "individual"
				saved, err := tools.store.UpsertWithProperties(&c, nil)
				if err != nil {
					t.Fatalf("seed %s: %v", c.FormattedName, err)
				}
				return saved
			}
			var holders []*Contact
			for _, name := range []string{"Alice Adams", "Alice Baker", "Alice Clark", "Alice Davis", "Alice Evans"} {
				holders = append(holders, seed(Contact{FormattedName: name, TrustZone: ZoneHousehold}))
			}
			givenOnly := seed(Contact{FormattedName: "Dr. A. Jones", GivenName: "Alice", TrustZone: ZoneKnown})
			holders = append(holders, seed(Contact{FormattedName: "Alice Zed", TrustZone: ZoneKnown}), givenOnly)
			for i := range SearchLimit {
				seed(Contact{FormattedName: fmt.Sprintf("Neighbour %03d", i), TrustZone: ZoneKnown, Note: "lives next to Alice"})
			}

			_, err := tools.store.ResolveContact("Alice")
			var ambiguous *AmbiguousNameError
			if !errors.As(err, &ambiguous) {
				t.Fatalf("ResolveContact(Alice) error = %v, want an AmbiguousNameError", err)
			}
			leftOut := !slices.ContainsFunc(ambiguous.Candidates, func(c NameCandidate) bool { return c.ContactID == givenOnly.ID })
			if ambiguous.Total != len(holders) || len(ambiguous.Candidates) != maxAmbiguousNamed || !leftOut {
				t.Fatalf("listed %d of %d, given-name-only record left out = %v; the case needs %d of %d with it left out:\n%v",
					len(ambiguous.Candidates), ambiguous.Total, leftOut, maxAmbiguousNamed, len(holders), err)
			}
			requireContains(t, err, fmt.Sprintf(`and %d more not listed: contact_lookup with query set to "Alice" lists all %d of them first`, len(holders)-maxAmbiguousNamed, len(holders)))

			found, truncated, err := tools.store.search(t.Context(), "Alice")
			if err != nil {
				t.Fatalf("search(Alice): %v", err)
			}
			if !truncated || len(found) != SearchLimit {
				t.Errorf("search(Alice) = %d contacts, truncated %v, want %d and truncated", len(found), truncated, SearchLimit)
			}
			for _, h := range holders {
				if !slices.ContainsFunc(found[:min(len(found), len(holders))], func(c *Contact) bool { return c.ID == h.ID }) {
					t.Errorf("search(Alice) does not list %s among its first %d contacts", h.FormattedName, len(holders))
				}
			}
			got, err := tools.LookupContact(`{"query":"Alice"}`)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range holders {
				if !strings.Contains(got, "**"+h.FormattedName+"**") {
					t.Errorf("contact_lookup query Alice does not list %s:\n%s", h.FormattedName, got)
				}
			}
		})
	}
}

// TestEdgeSpaceIsWhatTrimSpaceTrims pins that the set the exact step's
// SQL TRIM strips from a stored name holds exactly the runes
// strings.TrimSpace trims, so the exact step and nameKey cannot disagree
// about edge space.
func TestEdgeSpaceIsWhatTrimSpaceTrims(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if got, want := strings.ContainsRune(edgeSpace, r), unicode.IsSpace(r); got != want {
			t.Errorf("edgeSpace contains %U = %v, want %v", r, got, want)
		}
	}
}

// TestForgetContact_AmbiguousNameRemovesNothing pins that contact_forget
// by a first name two known records share removes neither and hands
// back both contact_id values.
func TestForgetContact_AmbiguousNameRemovesNothing(t *testing.T) {
	tools := newTestTools(t)
	a, err := tools.store.UpsertWithProperties(&Contact{FormattedName: "Dave Smith", GivenName: "Dave", Kind: "individual", TrustZone: ZoneKnown}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := tools.store.UpsertWithProperties(&Contact{FormattedName: "Dave Jones", GivenName: "Dave", Kind: "individual", TrustZone: ZoneKnown}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools.ForgetContact(`{"name":"Dave"}`)
	requireContains(t, err, `ambiguous contact "Dave"`, a.ID.String(), b.ID.String(), "nothing was removed")
	for _, c := range []*Contact{a, b} {
		if _, err := tools.store.Get(c.ID); err != nil {
			t.Errorf("%s is no longer active: %v", c.FormattedName, err)
		}
	}
}

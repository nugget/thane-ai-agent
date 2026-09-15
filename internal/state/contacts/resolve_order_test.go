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
// nickname in different bands of standing: the pinned operator, then
// records above known, then records at known. Two holders in one band
// are a tie, which TestResolveContact_ExactTies pins. Each case
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

// TestResolveContact_ExactTies pins what a name resolves to when
// several active records hold it exactly, as a formatted name or a
// nickname. A holder in a strictly higher band of standing wins
// outright: the pinned operator, then records above known, then
// records at known. Two or more distinct holders in the highest band
// any holder reaches are an exact tie, an AmbiguousNameError listing
// them with the field each holds the name by, and never a pick: a
// formatted name does not beat a nickname, and a lower id does not beat
// a higher one. It replaces the within-band cases of
// TestResolveContact_AuthorityOrder and
// TestResolveContact_KnownOnlyCollisionsAreDeterministic, which pinned
// those picks on purpose. Every case runs with and without FTS5.
func TestResolveContact_ExactTies(t *testing.T) {
	type seed struct{ name, nickname, given, zone string }
	tests := []struct {
		name   string
		seeds  []seed
		pin    string // the record pinned as the operator, or ""
		lookup string
		// want is the formatted name the lookup resolves to; otherwise the
		// lookup must be an exact tie between wantTie, in listing order, at
		// standing.
		want     string
		wantTie  []wantCandidate
		standing string
	}{
		{
			name:     "two known records sharing a nickname are a tie",
			seeds:    []seed{{name: "Alice Baker", nickname: "Ally", zone: ZoneKnown}, {name: "Alice Adams", nickname: "ally", zone: ZoneKnown}},
			lookup:   "ALLY",
			wantTie:  []wantCandidate{{"Alice Adams", NameFieldNickname}, {"Alice Baker", NameFieldNickname}},
			standing: "known",
		},
		{
			name:     "two household records sharing a nickname are a tie",
			seeds:    []seed{{name: "Carol Adams", nickname: "Cee", zone: ZoneHousehold}, {name: "Carol Baker", nickname: "cee", zone: ZoneHousehold}},
			lookup:   "Cee",
			wantTie:  []wantCandidate{{"Carol Adams", NameFieldNickname}, {"Carol Baker", NameFieldNickname}},
			standing: "above known",
		},
		{
			name:     "a known formatted name does not beat a known nickname",
			seeds:    []seed{{name: "Alice Adams", nickname: "Alice", zone: ZoneKnown}, {name: "Alice", zone: ZoneKnown}},
			lookup:   "alice",
			wantTie:  []wantCandidate{{"Alice", NameFieldFormatted}, {"Alice Adams", NameFieldNickname}},
			standing: "known",
		},
		{
			name:     "a household formatted name does not beat a trusted nickname, both being above known",
			seeds:    []seed{{name: "Robert Trusted", nickname: "Bob", zone: ZoneTrusted}, {name: "Bob", zone: ZoneHousehold}},
			lookup:   "bob",
			wantTie:  []wantCandidate{{"Bob", NameFieldFormatted}, {"Robert Trusted", NameFieldNickname}},
			standing: "above known",
		},
		{
			name:     "with no operator pinned an admin stands with a household record",
			seeds:    []seed{{name: "Alice Admin", nickname: "Boss", zone: ZoneAdmin}, {name: "Hana Household", nickname: "boss", zone: ZoneHousehold}},
			lookup:   "BOSS",
			wantTie:  []wantCandidate{{"Alice Admin", NameFieldNickname}, {"Hana Household", NameFieldNickname}},
			standing: "above known",
		},
		{
			name: "a household holder beats known holders outright",
			seeds: []seed{
				{name: "Alice", zone: ZoneKnown},
				{name: "Alice Adams", nickname: "Alice", zone: ZoneHousehold},
				{name: "Alice Baker", nickname: "Alice", zone: ZoneKnown},
			},
			lookup: "Alice", want: "Alice Adams",
		},
		{
			name: "the pinned operator beats household holders outright",
			seeds: []seed{
				{name: "Alice Operator", nickname: "Alice", zone: ZoneKnown},
				{name: "Alice Adams", nickname: "Alice", zone: ZoneHousehold},
				{name: "Alice Baker", nickname: "Alice", zone: ZoneHousehold},
			},
			pin:    "Alice Operator",
			lookup: "alice", want: "Alice Operator",
		},
		{
			name: "a lower-standing exact holder is not part of the tie",
			seeds: []seed{
				{name: "Alice", zone: ZoneKnown},
				{name: "Alice Baker", nickname: "Alice", zone: ZoneTrusted},
				{name: "Alice Adams", nickname: "Alice", zone: ZoneHousehold},
			},
			lookup:   "Alice",
			wantTie:  []wantCandidate{{"Alice Adams", NameFieldNickname}, {"Alice Baker", NameFieldNickname}},
			standing: "above known",
		},
		{
			name: "one record holding the name as its formatted name and nickname is one holder",
			seeds: []seed{
				{name: "Alice", nickname: "alice", zone: ZoneKnown},
				{name: "Alice Smith", given: "Alice", zone: ZoneHousehold},
			},
			lookup: "ALICE", want: "Alice",
		},
		{
			name: "an exact tie takes precedence over short-form holders and is reported alone",
			seeds: []seed{
				{name: "Alice Adams", nickname: "Alice", zone: ZoneKnown},
				{name: "Alice Baker", nickname: "Alice", zone: ZoneKnown},
				{name: "Alice Smith", given: "Alice", zone: ZoneHousehold},
				{name: "Dr. A. Jones", given: "Alice", zone: ZoneTrusted},
			},
			lookup:   "Alice",
			wantTie:  []wantCandidate{{"Alice Adams", NameFieldNickname}, {"Alice Baker", NameFieldNickname}},
			standing: "known",
		},
		{
			name: "padded names fold as before",
			seeds: []seed{
				{name: "Alice Adams", nickname: " Alice\t", zone: ZoneKnown},
				{name: "Alice\u3000", zone: ZoneKnown},
				{name: "Alice Baker", given: "Alice", zone: ZoneHousehold},
			},
			lookup:   "  alice\n",
			wantTie:  []wantCandidate{{"Alice Adams", NameFieldNickname}, {"Alice\u3000", NameFieldFormatted}},
			standing: "known",
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
						FormattedName: s.name, Nickname: s.nickname, GivenName: s.given, Kind: "individual", TrustZone: s.zone,
					}, nil)
					if err != nil {
						t.Fatalf("seed %s: %v", s.name, err)
					}
					byName[s.name] = c
				}
				if tt.pin != "" {
					store.pinOperatorContactID(byName[tt.pin].ID)
				}
				for range 3 {
					got, err := store.ResolveContact(tt.lookup)
					if tt.want != "" {
						if err != nil || got.ID != byName[tt.want].ID {
							t.Fatalf("ResolveContact(%q) = %+v, %v, want %s", tt.lookup, got, err, tt.want)
						}
						continue
					}
					requireExactTie(t, err, tt.lookup, tt.standing, tt.wantTie, byName)
				}
			})
		}
	}
}

// TestResolveContact_ExactTieContinuation pins that an exact tie too
// large to list whole points at a query that lists the tied holders
// first, ahead of every other contact that answers to the name. Seven
// household records hold the nickname "Eve". Three known records also
// hold it, below the tie, and three household records answer to it by
// the first word of their formatted names; each was saved more
// recently than any tied holder, and each mentions Eve in its note
// often enough to outrank one, so a search that put every contact
// answering to the name in one group, ordered by rank or recency within
// it, would list them among the tied holders. The fixture checks that
// it does before it checks the search that lists the tie first.
func TestResolveContact_ExactTieContinuation(t *testing.T) {
	for _, fts := range []bool{true, false} {
		t.Run(fmt.Sprintf("fts=%v", fts), func(t *testing.T) {
			tools := newTestTools(t)
			tools.store.ftsEnabled = fts
			seed := func(c Contact) *Contact {
				t.Helper()
				c.Kind = "individual"
				saved, err := tools.store.UpsertWithProperties(&c, nil)
				if err != nil {
					t.Fatalf("seed %q: %v", c.FormattedName, err)
				}
				return saved
			}
			var tied, others []*Contact
			for i := range maxAmbiguousNamed + 2 {
				tied = append(tied, seed(Contact{FormattedName: fmt.Sprintf("Tied %d", i+1), Nickname: "Eve", TrustZone: ZoneHousehold}))
			}
			for i := range 3 {
				others = append(others,
					seed(Contact{FormattedName: fmt.Sprintf("Known %d", i+1), Nickname: "Eve", TrustZone: ZoneKnown, Note: "Eve Eve Eve"}),
					seed(Contact{FormattedName: fmt.Sprintf("Eve Short %d", i+1), TrustZone: ZoneHousehold, Note: "Eve Eve Eve"}))
			}
			for _, set := range []struct {
				at string
				cs []*Contact
			}{{"2026-01-01T00:00:00Z", tied}, {"2026-06-01T00:00:00Z", others}} {
				for _, c := range set.cs {
					if _, err := tools.store.db.Exec(`UPDATE contacts SET updated_at = ? WHERE id = ?`, set.at, c.ID.String()); err != nil {
						t.Fatal(err)
					}
				}
			}

			_, err := tools.store.ResolveContact("Eve")
			var tie *AmbiguousNameError
			if !errors.As(err, &tie) || !tie.ExactTie || tie.Total != len(tied) || len(tie.Candidates) != maxAmbiguousNamed {
				t.Fatalf("ResolveContact(Eve) = %v, want an exact tie listing %d of %d", err, maxAmbiguousNamed, len(tied))
			}
			for i, c := range tie.Candidates {
				if c.ContactID != tied[i].ID || c.Field != NameFieldNickname {
					t.Errorf("candidate %d = %+v, want %s by nickname", i, c, tied[i].FormattedName)
				}
			}
			requireContains(t, err, fmt.Sprintf(`and 2 more not listed: contact_lookup with query set to "Eve" lists all %d of them first`, len(tied)))

			isTied := func(c *Contact) bool { return slices.ContainsFunc(tied, func(h *Contact) bool { return h.ID == c.ID }) }
			search := tools.store.searchLIKE
			if fts {
				search = tools.store.searchFTS
			}
			// Control: every contact answering to Eve in one group, as the
			// search once ordered them, lists another among the tied.
			var ids []any
			for _, c := range append(append([]*Contact{}, tied...), others...) {
				ids = append(ids, c.ID.String())
			}
			grouped, err := search(t.Context(), "Eve", searchOrder{
				sql:  `CASE WHEN contacts.id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `) THEN 0 ELSE 1 END`,
				args: ids,
			})
			if err != nil {
				t.Fatalf("grouped search: %v", err)
			}
			if !slices.ContainsFunc(grouped[:len(tied)], func(c *Contact) bool { return !isTied(c) }) {
				t.Fatalf("a search grouping every holder lists the tie first anyway; the fixture no longer shows the ordering is needed")
			}

			found, _, err := tools.store.search(t.Context(), "Eve")
			if err != nil {
				t.Fatalf("search(Eve): %v", err)
			}
			for i, c := range found[:len(tied)] {
				if c.ID != tied[i].ID {
					t.Errorf("search(Eve) row %d = %q, want the tied holder %q", i, c.FormattedName, tied[i].FormattedName)
				}
			}

			got, err := tools.LookupContact(`{"query":"Eve"}`)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range tied {
				if row := fmt.Sprintf("contact_id %s | trust zone %s | answers to %q by %s", h.ID, h.TrustZone, "Eve", NameFieldNickname); !strings.Contains(got, row) {
					t.Errorf("contact_lookup query Eve does not list %q as %q:\n%s", h.FormattedName, row, got)
				}
			}
		})
	}
}

// TestAmbiguousNameError_ExactTieWording pins how an exact tie names
// the standing its candidates share, and that its text never borrows the
// short-form ambiguity's claim that no contact holds the name exactly.
func TestAmbiguousNameError_ExactTieWording(t *testing.T) {
	tests := []struct {
		zone, standing string
	}{
		{ZoneKnown, "known"},
		{ZoneHousehold, "above known"},
		{ZoneAdmin, "above known"},
		{"bogus", "above known"},
	}
	for _, tt := range tests {
		t.Run(tt.zone, func(t *testing.T) {
			e := &AmbiguousNameError{Name: "Eve", ExactTie: true, Total: 2, Candidates: []NameCandidate{
				{Name: "Eve", TrustZone: tt.zone, Field: NameFieldFormatted},
				{Name: "Eve Adams", TrustZone: tt.zone, Field: NameFieldNickname},
			}}
			got := e.Error()
			want := fmt.Sprintf("2 active contacts at the same standing (%s) hold it exactly as a formatted name or nickname", tt.standing)
			if !strings.Contains(got, want) || strings.Contains(got, "is exactly that") || !strings.Contains(got, "only its contact_id tells it apart") {
				t.Errorf("error = %s\nwant it to contain %q and the contact_id retry, and not the short-form wording", got, want)
			}
		})
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

// requireAmbiguous fails unless err is a short-form [AmbiguousNameError]
// listing exactly want, in order, each with its contact_id, zone and
// field in the text, and echoing the lookup as trimmed.
func requireAmbiguous(t *testing.T, err error, lookup string, want []wantCandidate, byName map[string]*Contact) {
	t.Helper()
	if ambiguous := requireCandidates(t, err, lookup, want, byName); ambiguous.ExactTie {
		t.Errorf("ambiguity is an exact tie, want a short-form one:\n%v", err)
	}
	for _, s := range []string{"answer to it by a given name or the first word of a formatted name", "resolves to none of them", "Retry with the contact_id", "full formatted name"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error missing %q:\n%v", s, err)
		}
	}
}

// requireExactTie fails unless err is an exact-tie [AmbiguousNameError]
// at standing, listing exactly want as requireAmbiguous checks it, with
// no word of the short-form ambiguity.
func requireExactTie(t *testing.T, err error, lookup, standing string, want []wantCandidate, byName map[string]*Contact) {
	t.Helper()
	if ambiguous := requireCandidates(t, err, lookup, want, byName); !ambiguous.ExactTie {
		t.Errorf("ambiguity is a short-form one, want an exact tie:\n%v", err)
	}
	for _, s := range []string{
		fmt.Sprintf("%d active contacts at the same standing (%s) hold it exactly as a formatted name or nickname", len(want), standing),
		"no contact with more authority holds it", "resolves to none of them", "Retry with the contact_id", "only its contact_id tells it apart",
	} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error missing %q:\n%v", s, err)
		}
	}
	if strings.Contains(err.Error(), "given name or the first word") {
		t.Errorf("an exact tie speaks of short forms:\n%v", err)
	}
}

// requireCandidates fails unless err is an [AmbiguousNameError] listing
// exactly want, in order, each with its contact_id, zone and field in
// the text, and echoing the lookup as trimmed, and returns it.
func requireCandidates(t *testing.T, err error, lookup string, want []wantCandidate, byName map[string]*Contact) *AmbiguousNameError {
	t.Helper()
	lookup = strings.TrimSpace(lookup)
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
	if echo := fmt.Sprintf("ambiguous contact %q", lookup); !strings.Contains(err.Error(), echo) {
		t.Errorf("error missing %q:\n%v", echo, err)
	}
	return ambiguous
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
// query, that query lists them all first, each with its contact_id;
// past SearchLimit, that query lists only SearchLimit of them, and the
// error says so and to ask the operator when the one the model wants is
// not among them. It claims nothing about lookups other than that query.
func TestAmbiguousNameError_Continuation(t *testing.T) {
	tests := []struct {
		total     int
		want, not string
	}{
		{maxAmbiguousNamed + 2, `and 2 more not listed: contact_lookup with query set to "Eve" lists all 7 of them first, ahead of any other match, each with its contact_id`, "may not be among them"},
		{SearchLimit, fmt.Sprintf(`and %d more not listed: contact_lookup with query set to "Eve" lists all %d of them first`, SearchLimit-maxAmbiguousNamed, SearchLimit), "may not be among them"},
		{SearchLimit + 1, fmt.Sprintf(`lists only %d of the %d, each with its contact_id, so the one you mean may not be among them; if it is not, ask the operator for its full formatted name`, SearchLimit, SearchLimit+1), "lists all"},
		{SearchLimit + 1, "not listed", "no lookup"},
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
// out, on both search paths. Eight contacts answer to "Alice" by a short
// form. The three the error leaves out are a record that answers only by
// its given name and whose formatted name does not contain it, and two
// records whose formatted names differ only by edge space, so they share
// one name key and no name tells them apart. More contacts than
// SearchLimit mention Alice in their notes, often enough to outrank a
// single name hit, and were saved more recently than any holder, so a
// search ordered by rank or recency alone leaves candidates out; the
// fixture checks that it does before it checks the search that lists
// the holders first. contact_lookup with the name as query then lists
// all eight first, each with the contact_id and zone the error's retry
// needs and the name field it answers by.
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
					t.Fatalf("seed %q: %v", c.FormattedName, err)
				}
				return saved
			}
			var holders []*Contact
			for _, name := range []string{"Alice Adams", "Alice Baker", "Alice Clark", "Alice Davis", "Alice Evans"} {
				holders = append(holders, seed(Contact{FormattedName: name, TrustZone: ZoneHousehold}))
			}
			leftOut := []*Contact{
				seed(Contact{FormattedName: "Alice Smith", TrustZone: ZoneKnown, Note: "dentist"}),
				seed(Contact{FormattedName: "Alice Smith ", TrustZone: ZoneKnown, Note: "coworker"}),
				seed(Contact{FormattedName: "Dr. A. Jones", GivenName: "Alice", TrustZone: ZoneKnown}),
			}
			holders = append(holders, leftOut...)
			var neighbours []*Contact
			for i := range SearchLimit {
				neighbours = append(neighbours, seed(Contact{FormattedName: fmt.Sprintf("Neighbour %03d", i), TrustZone: ZoneKnown, Note: "Alice Alice Alice, Alice's neighbour"}))
			}
			setUpdatedAt := func(at string, cs []*Contact) {
				t.Helper()
				for _, c := range cs {
					if _, err := tools.store.db.Exec(`UPDATE contacts SET updated_at = ? WHERE id = ?`, at, c.ID.String()); err != nil {
						t.Fatal(err)
					}
				}
			}
			setUpdatedAt("2026-01-01T00:00:00Z", holders)
			setUpdatedAt("2026-06-01T00:00:00Z", neighbours)

			_, err := tools.store.ResolveContact("Alice")
			var ambiguous *AmbiguousNameError
			if !errors.As(err, &ambiguous) {
				t.Fatalf("ResolveContact(Alice) error = %v, want an AmbiguousNameError", err)
			}
			listed := func(c *Contact) bool {
				return slices.ContainsFunc(ambiguous.Candidates, func(n NameCandidate) bool { return n.ContactID == c.ID })
			}
			if ambiguous.Total != len(holders) || len(ambiguous.Candidates) != maxAmbiguousNamed || slices.ContainsFunc(leftOut, listed) {
				t.Fatalf("listed %d of %d; the case needs %d of %d with none of %q, %q, %q among them:\n%v",
					len(ambiguous.Candidates), ambiguous.Total, maxAmbiguousNamed, len(holders),
					leftOut[0].FormattedName, leftOut[1].FormattedName, leftOut[2].FormattedName, err)
			}
			requireContains(t, err, fmt.Sprintf(`and %d more not listed: contact_lookup with query set to "Alice" lists all %d of them first`, len(holders)-maxAmbiguousNamed, len(holders)))

			// Without the holders-first term, rank or recency must push a
			// holder past SearchLimit, or this fixture cannot tell the
			// ordering from the order it seeded in.
			unordered := tools.store.searchLIKE
			if fts {
				unordered = tools.store.searchFTS
			}
			plain, err := unordered(t.Context(), "Alice", searchOrder{})
			if err != nil {
				t.Fatalf("search(Alice) without holders first: %v", err)
			}
			if !slices.ContainsFunc(holders, func(h *Contact) bool {
				return !slices.ContainsFunc(plain[:min(len(plain), SearchLimit)], func(c *Contact) bool { return c.ID == h.ID })
			}) {
				t.Fatalf("search(Alice) without holders first lists every holder within %d rows; the fixture no longer shows the ordering is needed", SearchLimit)
			}

			found, truncated, err := tools.store.search(t.Context(), "Alice")
			if err != nil {
				t.Fatalf("search(Alice): %v", err)
			}
			if !truncated || len(found) != SearchLimit {
				t.Errorf("search(Alice) = %d contacts, truncated %v, want %d and truncated", len(found), truncated, SearchLimit)
			}
			for _, h := range holders {
				if !slices.ContainsFunc(found[:min(len(found), len(holders))], func(c *Contact) bool { return c.ID == h.ID }) {
					t.Errorf("search(Alice) does not list %q among its first %d contacts", h.FormattedName, len(holders))
				}
			}

			got, err := tools.LookupContact(`{"query":"Alice"}`)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range holders {
				field := NameFieldFormattedFirstWord
				if h.GivenName != "" {
					field = NameFieldGiven
				}
				row := fmt.Sprintf("contact_id %s | trust zone %s | answers to %q by %s", h.ID, h.TrustZone, "Alice", field)
				if !strings.Contains(got, row) {
					t.Errorf("contact_lookup query Alice does not list %q as %q:\n%s", h.FormattedName, row, got)
				}
			}
			noteOnlyRows := 0
			for _, line := range strings.Split(got, "\n") {
				if !slices.ContainsFunc(neighbours, func(n *Contact) bool { return strings.Contains(line, "contact_id "+n.ID.String()) }) {
					continue
				}
				noteOnlyRows++
				if strings.Contains(line, "answers to") {
					t.Errorf("contact_lookup query Alice marks a note-only match as answering to the name: %q", line)
				}
			}
			if noteOnlyRows == 0 {
				t.Errorf("contact_lookup query Alice lists no note-only match with its contact_id:\n%s", got)
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

package contacts

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

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
			name:   "a name no record answers to still falls back to search",
			seeds:  []seed{{"Frank Search", "", ZoneKnown}, {"Grace Household", "", ZoneHousehold}},
			lookup: "Frank", want: "Frank Search",
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

// TestResolveContact_SearchFallbackUnchanged pins that authority plays
// no part in the search fallback: several partial matches stay
// ambiguous even when one is above known, and no match stays
// sql.ErrNoRows.
func TestResolveContact_SearchFallbackUnchanged(t *testing.T) {
	store := newTestStore(t)
	seedContactAt(t, store, "Eve Alpha", ZoneHousehold)
	seedContactAt(t, store, "Eve Beta", ZoneKnown)
	if _, err := store.ResolveContact("Eve"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ResolveContact(Eve) error = %v, want an ambiguous-match error", err)
	}
	if _, err := store.ResolveContact("Nobody Here"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ResolveContact(Nobody Here) error = %v, want sql.ErrNoRows", err)
	}
}

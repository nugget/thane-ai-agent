package contacts

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
)

// stopNoteFormat is the note a query result ends with when the search
// stopped at SearchLimit, as it read before the result had a budget.
const stopNoteFormat = "\nStopped at %d matches; more contacts match %q. Contacts whose formatted name, nickname, given name or first word it is are listed first. contact_lookup with a contact's full formatted name as name reaches one this list left out.\n"

// listNoteFormat is the note a kind, key/value or contact_list result
// ends with when it left rows off to stay within searchResultMaxBytes.
const listNoteFormat = "\nListed %d of the %d contacts found; the last %d are left off to keep this result within 16 KB. Rows run in formatted-name order; contact_lookup by name, or with a query, reaches the ones left off.\n"

// longSearchContacts returns n contacts, none answering to "Dave" as a
// name, whose formatted names run past nameBytes and whose AI summaries
// are summaryBytes long.
func longSearchContacts(n, nameBytes, summaryBytes int) []*Contact {
	zones := []string{ZoneKnown, ZoneTrusted, ZoneHousehold}
	contacts := make([]*Contact, n)
	for i := range contacts {
		contacts[i] = &Contact{
			ID:            uuid.New(),
			FormattedName: fmt.Sprintf("%03d %s", i, strings.Repeat("n", nameBytes)),
			AISummary:     strings.Repeat("s", summaryBytes),
			TrustZone:     zones[i%len(zones)],
		}
	}
	return contacts
}

// TestFormatSearchResults_Budget pins the query result within
// searchResultMaxBytes: rows that would pass it are left off the end,
// the result says how many it lists and how many it left off, every
// listed row keeps its whole contact_id and zone, and a stopped search
// still ends with its note, intact.
func TestFormatSearchResults_Budget(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		n, nameBytes, summaries int
		truncated               bool
	}{
		{name: "50 rows with long names and summaries", n: SearchLimit, nameBytes: 300, summaries: 600},
		{name: "50 ordinary rows", n: SearchLimit, nameBytes: 20, summaries: 300},
		{name: "a stopped search", n: SearchLimit, nameBytes: 300, summaries: 600, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contacts := longSearchContacts(tc.n, tc.nameBytes, tc.summaries)
			got := formatSearchResults(contacts, "Dave", tc.truncated)
			if len(got) > searchResultMaxBytes {
				t.Errorf("result is %d bytes, over the %d budget", len(got), searchResultMaxBytes)
			}
			if !utf8.ValidString(got) {
				t.Error("result is not valid UTF-8")
			}
			if !strings.HasPrefix(got, fmt.Sprintf("Found %d contact(s):\n\n", tc.n)) {
				t.Errorf("result does not open with the count found:\n%.200s", got)
			}
			listed := strings.Count(got, "\n  contact_id ")
			if listed == 0 || listed >= tc.n {
				t.Fatalf("listed %d of %d rows, want some left off", listed, tc.n)
			}
			for i, c := range contacts {
				row := fmt.Sprintf("\n  contact_id %s | trust zone %s\n", c.ID, c.TrustZone)
				if want := i < listed; strings.Contains(got, row) != want || strings.Contains(got, c.ID.String()) != want {
					t.Errorf("row %d whole in the result = %v, want %v", i, !want, want)
				}
			}
			tail := fmt.Sprintf("\nListed %d of the %d contacts found; the last %d are left off to keep this result within 16 KB. Narrow the query, or look one up by its full formatted name as name, to reach them.\n", listed, tc.n, tc.n-listed)
			if tc.truncated {
				tail += fmt.Sprintf(stopNoteFormat, SearchLimit, "Dave")
			}
			if !strings.HasSuffix(got, tail) {
				t.Errorf("result does not end with %q:\n...%s", tail, got[max(len(got)-600, 0):])
			}
		})
	}
}

// TestFormatSearchResults_ClipsLongFields pins a single pathological
// row: its name, organization and summary, and the query the row and
// the stop note quote, are each cut on a rune boundary and marked, with
// a multi-byte rune straddling each cut, and the row keeps its whole
// contact_id, zone and matched field.
func TestFormatSearchResults_ClipsLongFields(t *testing.T) {
	name := strings.Repeat("€", 1000) // 3 bytes a rune: the cut at 248 falls inside one
	c := &Contact{
		ID:            uuid.New(),
		FormattedName: name,
		Org:           strings.Repeat("é", 500),
		AISummary:     "x" + strings.Repeat("日", 2000), // runes start at 1+3k: the cut at 504 falls inside one
		TrustZone:     ZoneTrusted,
	}
	got := formatSearchResults([]*Contact{c}, name, true)
	if !utf8.ValidString(got) {
		t.Fatal("result is not valid UTF-8")
	}
	if len(got) > 2<<10 {
		t.Errorf("one clipped row is %d bytes", len(got))
	}
	echo := strings.Repeat("€", 82) + searchCutMarker
	want := fmt.Sprintf("Found 1 contact(s):\n\n**%s** (%s) — %s\n  contact_id %s | trust zone %s | answers to %q by %s\n",
		echo, strings.Repeat("é", 124)+searchCutMarker, "x"+strings.Repeat("日", 167)+searchCutMarker,
		c.ID, ZoneTrusted, echo, NameFieldFormatted) + fmt.Sprintf(stopNoteFormat, SearchLimit, echo)
	if got != want {
		t.Errorf("result =\n%s\nwant\n%s", got, want)
	}
}

// TestClipSearchField pins the cut: at most limit bytes, on a rune
// boundary, ending with the marker, and a value within the limit
// unchanged.
func TestClipSearchField(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		limit    int
		want     string
	}{
		{name: "short", in: "Alice", limit: 20, want: "Alice"},
		{name: "exactly the limit", in: strings.Repeat("a", 20), limit: 20, want: strings.Repeat("a", 20)},
		{name: "cut on a boundary", in: strings.Repeat("€", 10), limit: 20, want: "€€€€" + searchCutMarker},
		{name: "3-byte rune straddles the cut", in: "a" + strings.Repeat("€", 10), limit: 20, want: "a€€€" + searchCutMarker},
		{name: "4-byte rune straddles the cut", in: strings.Repeat("𝄞", 10), limit: 21, want: "𝄞𝄞𝄞" + searchCutMarker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := clipSearchField(tc.in, tc.limit)
			if got != tc.want || len(got) > tc.limit || !utf8.ValidString(got) {
				t.Errorf("clipSearchField(%q, %d) = %q (%d bytes), want %q", tc.in, tc.limit, got, len(got), tc.want)
			}
		})
	}
}

// TestFormatSearchResults_SmallResultUnchanged pins that a result within
// every bound renders byte for byte as it did before the budget.
func TestFormatSearchResults_SmallResultUnchanged(t *testing.T) {
	alice := &Contact{ID: uuid.New(), FormattedName: "Alice Rivera", Nickname: "Dave", Org: "Acme", AISummary: "Backend developer", TrustZone: ZoneHousehold}
	bob := &Contact{ID: uuid.New(), FormattedName: "Bob Stone", Note: "Dave's neighbour", TrustZone: ZoneKnown}
	rows := "Found 2 contact(s):\n\n" +
		fmt.Sprintf("**Alice Rivera** (Acme) — Backend developer\n  contact_id %s | trust zone household | answers to \"Dave\" by %s\n", alice.ID, NameFieldNickname) +
		fmt.Sprintf("**Bob Stone**\n  contact_id %s | trust zone known\n", bob.ID)
	for _, truncated := range []bool{false, true} {
		want := rows
		if truncated {
			want += fmt.Sprintf(stopNoteFormat, SearchLimit, "Dave")
		}
		if got := formatSearchResults([]*Contact{alice, bob}, "Dave", truncated); got != want {
			t.Errorf("truncated=%v: result =\n%s\nwant\n%s", truncated, got, want)
		}
	}
}

// TestLookupContact_QueryStaysWithinBudget pins the budget through
// contact_lookup on both search paths: 50 ordinary rows with
// sentence-long summaries pass 16 KB unbounded, so the result lists what
// fits and counts the rest, and a search that stopped at SearchLimit
// still ends with its note. A long query that matches nothing is echoed
// cut.
func TestLookupContact_QueryStaysWithinBudget(t *testing.T) {
	summary := strings.Repeat("a quiet neighbour who waters the plants; ", 12)
	for _, fts := range []bool{true, false} {
		for _, seeded := range []int{SearchLimit, SearchLimit + 1} {
			t.Run(fmt.Sprintf("seeded=%d/fts=%v", seeded, fts), func(t *testing.T) {
				tools := newTestTools(t)
				tools.store.ftsEnabled = fts
				for i := range seeded {
					if _, err := tools.store.UpsertWithProperties(&Contact{
						FormattedName: fmt.Sprintf("Neighbour %03d", i), Kind: "individual", TrustZone: ZoneKnown, Note: "lives next to Dave", AISummary: summary,
					}, nil); err != nil {
						t.Fatal(err)
					}
				}
				got, err := tools.LookupContact(`{"query":"Dave"}`)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) > searchResultMaxBytes {
					t.Errorf("result is %d bytes, over the %d budget", len(got), searchResultMaxBytes)
				}
				listed := strings.Count(got, "\n  contact_id ")
				note := fmt.Sprintf("\nListed %d of the %d contacts found; the last %d are left off", listed, SearchLimit, SearchLimit-listed)
				if !strings.HasPrefix(got, fmt.Sprintf("Found %d contact(s)", SearchLimit)) || listed >= SearchLimit || !strings.Contains(got, note) {
					t.Errorf("result lists %d rows without %q:\n...%s", listed, note, got[max(len(got)-600, 0):])
				}
				if stopped := strings.HasSuffix(got, fmt.Sprintf(stopNoteFormat, SearchLimit, "Dave")); stopped != (seeded > SearchLimit) {
					t.Errorf("result ends with the stop note = %v, want %v", stopped, seeded > SearchLimit)
				}
			})
		}
		t.Run(fmt.Sprintf("a long query matching nothing/fts=%v", fts), func(t *testing.T) {
			tools := newTestTools(t)
			tools.store.ftsEnabled = fts
			got, err := tools.LookupContact(custodyArgs(t, map[string]any{"query": strings.Repeat("q", 20000)}))
			if err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("No contacts matching %q", strings.Repeat("q", searchFieldMaxBytes-len(searchCutMarker))+searchCutMarker); got != want {
				t.Errorf("result = %.300q, want %.300q", got, want)
			}
		})
	}
}

// TestFormatContactList_Budget pins a kind, key/value or contact_list
// result within searchResultMaxBytes: rows that would pass it are left
// off the end in the order the store returned them, and the result says
// how many it lists, how many it left off and how to reach them.
func TestFormatContactList_Budget(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		n, nameBytes, summaries int
	}{
		{name: "100 rows with long names and summaries", n: 100, nameBytes: 300, summaries: 600},
		{name: "100 ordinary rows", n: 100, nameBytes: 20, summaries: 300},
		{name: "50 ordinary rows", n: 50, nameBytes: 20, summaries: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contacts := longSearchContacts(tc.n, tc.nameBytes, tc.summaries)
			got := formatContactList(contacts)
			if len(got) > searchResultMaxBytes {
				t.Errorf("result is %d bytes, over the %d budget", len(got), searchResultMaxBytes)
			}
			if !utf8.ValidString(got) {
				t.Error("result is not valid UTF-8")
			}
			if !strings.HasPrefix(got, fmt.Sprintf("Found %d contact(s):\n\n", tc.n)) {
				t.Errorf("result does not open with the count found:\n%.200s", got)
			}
			listed := strings.Count(got, "\n**")
			if listed == 0 || listed >= tc.n {
				t.Fatalf("listed %d of %d rows, want some left off", listed, tc.n)
			}
			for i := range contacts {
				if want := i < listed; strings.Contains(got, fmt.Sprintf("\n**%03d ", i)) != want {
					t.Errorf("row %d in the result = %v, want %v", i, !want, want)
				}
			}
			if tail := fmt.Sprintf(listNoteFormat, listed, tc.n, tc.n-listed); !strings.HasSuffix(got, tail) {
				t.Errorf("result does not end with %q:\n...%s", tail, got[max(len(got)-600, 0):])
			}
		})
	}
}

// TestFormatContactList_ClipsLongFields pins a single pathological row
// in a kind, key/value or contact_list result: its name, organization
// and summary are each cut on a rune boundary and marked, as a query
// row's are.
func TestFormatContactList_ClipsLongFields(t *testing.T) {
	c := &Contact{
		ID:            uuid.New(),
		FormattedName: strings.Repeat("€", 1000),
		Org:           strings.Repeat("é", 500),
		AISummary:     "x" + strings.Repeat("日", 2000),
		TrustZone:     ZoneTrusted,
	}
	got := formatContactList([]*Contact{c})
	want := fmt.Sprintf("Found 1 contact(s):\n\n**%s** (%s) — %s\n",
		strings.Repeat("€", 82)+searchCutMarker, strings.Repeat("é", 124)+searchCutMarker, "x"+strings.Repeat("日", 167)+searchCutMarker)
	if got != want {
		t.Errorf("result =\n%s\nwant\n%s", got, want)
	}
}

// TestFormatContactList_SmallResultUnchanged pins that a kind, key/value
// or contact_list result within every bound renders byte for byte as it
// did before the budget.
func TestFormatContactList_SmallResultUnchanged(t *testing.T) {
	alice := &Contact{ID: uuid.New(), FormattedName: "Alice Rivera", Org: "Acme", AISummary: "Backend developer", TrustZone: ZoneHousehold}
	bob := &Contact{ID: uuid.New(), FormattedName: "Bob Stone", TrustZone: ZoneKnown}
	want := "Found 2 contact(s):\n\n**Alice Rivera** (Acme) — Backend developer\n**Bob Stone**\n"
	if got := formatContactList([]*Contact{alice, bob}); got != want {
		t.Errorf("result =\n%s\nwant\n%s", got, want)
	}
}

// TestContactLists_StayWithinBudget pins the budget through every tool
// path that lists contacts without a query: contact_lookup by kind and
// by key/value, and contact_list with and without kind. A hundred
// individuals with sentence-long summaries pass 16 KB unbounded, and so
// does one contact whose AI summary alone is 20 KB.
func TestContactLists_StayWithinBudget(t *testing.T) {
	tools := newTestTools(t)
	summary := strings.Repeat("a quiet neighbour who waters the plants; ", 8)
	for i := range 100 {
		if _, err := tools.store.UpsertWithProperties(&Contact{
			FormattedName: fmt.Sprintf("Neighbour %03d", i), Kind: "individual", TrustZone: ZoneKnown, AISummary: summary,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tools.store.UpsertWithProperties(&Contact{
		FormattedName: "Bob Big", Kind: "org", TrustZone: ZoneKnown, AISummary: strings.Repeat("b", 20<<10),
	}, []Property{{Property: "EMAIL", Value: "bob@example.com"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		call  func() (string, error)
		found int
	}{
		{"contact_lookup by kind", func() (string, error) { return tools.LookupContact(`{"kind":"individual"}`) }, 100},
		{"contact_lookup by key/value", func() (string, error) {
			return tools.LookupContact(`{"key":"email","value":"bob@example.com"}`)
		}, 1},
		{"contact_list", func() (string, error) { return tools.ListContacts(`{}`) }, 100},
		{"contact_list by kind", func() (string, error) { return tools.ListContacts(`{"kind":"individual"}`) }, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call()
			if err != nil {
				t.Fatal(err)
			}
			if len(got) > searchResultMaxBytes {
				t.Errorf("result is %d bytes, over the %d budget", len(got), searchResultMaxBytes)
			}
			if !strings.HasPrefix(got, fmt.Sprintf("Found %d contact(s):\n\n", tc.found)) {
				t.Errorf("result does not open with the count found:\n%.200s", got)
			}
			listed := strings.Count(got, "\n**")
			if tc.found == 1 {
				if listed != 1 || !strings.Contains(got, searchCutMarker) || len(got) > 2<<10 {
					t.Errorf("one long row renders as %d bytes, %d rows:\n%.300s", len(got), listed, got)
				}
				return
			}
			if tail := fmt.Sprintf(listNoteFormat, listed, tc.found, tc.found-listed); listed >= tc.found || !strings.HasSuffix(got, tail) {
				t.Errorf("result lists %d rows without ending %q:\n...%s", listed, tail, got[max(len(got)-600, 0):])
			}
		})
	}
}

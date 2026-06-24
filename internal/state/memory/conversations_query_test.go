package memory

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

func newConvQueryStore(t *testing.T, maxMessages int) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(t.TempDir()+"/memory.db", maxMessages)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedConv inserts a conversation row with exact timestamp strings (so tests
// can simulate the mixed-zone on-disk reality) and optional metadata.
func seedConv(t *testing.T, s *SQLiteStore, id, created, updated, metadata string) {
	t.Helper()
	var meta any
	if metadata != "" {
		meta = metadata
	}
	if _, err := s.db.Exec(
		`INSERT INTO conversations (id, created_at, updated_at, metadata) VALUES (?,?,?,?)`,
		id, created, updated, meta); err != nil {
		t.Fatalf("seed conversation %s: %v", id, err)
	}
}

func seedMessages(t *testing.T, s *SQLiteStore, convID, status string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := s.db.Exec(
			`INSERT INTO messages (id, conversation_id, role, content, timestamp, status) VALUES (?,?,?,?,?,?)`,
			fmt.Sprintf("%s-%s-m%d", convID, status, i), convID, "user", "x",
			"2026-06-24T00:00:00Z", status); err != nil {
			t.Fatalf("seed message %s/%d: %v", convID, i, err)
		}
	}
}

func convIDs(page *ConversationPage) []string {
	out := make([]string, 0, len(page.Conversations))
	for _, c := range page.Conversations {
		out = append(out, c.ID)
	}
	return out
}

// TestQueryConversationsMixedZoneOrdering is the regression guard for the
// reproduced bug: a later instant stored in local-offset form must NOT
// mis-sort against an earlier instant stored as UTC. Raw-column ordering
// would return the wrong order; the strftime normalization fixes it.
func TestQueryConversationsMixedZoneOrdering(t *testing.T) {
	s := newConvQueryStore(t, 100)
	// X = 23:00Z written in driver-native local-offset form (sorts BEFORE the
	// UTC rows lexically, though it is the LATEST instant).
	seedConv(t, s, "X", "2026-06-24T10:00:00Z", "2026-06-24 18:00:00.000000000-05:00", "")
	seedConv(t, s, "Y", "2026-06-24T11:00:00Z", "2026-06-24T20:00:00Z", "")
	seedConv(t, s, "Z", "2026-06-24T12:00:00Z", "2026-06-24T21:00:00Z", "")

	desc, err := s.QueryConversations(ConversationQuery{Sort: "updated_at", Order: "desc", Limit: 10})
	if err != nil {
		t.Fatalf("desc query: %v", err)
	}
	if got := convIDs(desc); !equalSlice(got, []string{"X", "Z", "Y"}) {
		t.Fatalf("updated_at desc = %v, want [X Z Y] (X is 23:00Z despite local-offset storage)", got)
	}

	asc, err := s.QueryConversations(ConversationQuery{Sort: "updated_at", Order: "asc", Limit: 10})
	if err != nil {
		t.Fatalf("asc query: %v", err)
	}
	if got := convIDs(asc); !equalSlice(got, []string{"Y", "Z", "X"}) {
		t.Fatalf("updated_at asc = %v, want [Y Z X]", got)
	}
}

// TestQueryConversationsInvalidMetadata proves the json_valid guard is
// load-bearing: a conversation with broken metadata must not abort metadata
// filters, and must still be returned by non-metadata queries.
func TestQueryConversationsInvalidMetadata(t *testing.T) {
	s := newConvQueryStore(t, 100)
	seedConv(t, s, "good", "2026-06-24T10:00:00Z", "2026-06-24T10:00:00Z",
		`{"channel_binding":{"channel":"signal","contact_name":"Alice","address":"+15551234567"}}`)
	seedConv(t, s, "broken", "2026-06-24T11:00:00Z", "2026-06-24T11:00:00Z", `{broken json`)

	// Unfiltered: both returned (broken row tolerated, ChannelBinding nil).
	all, err := s.QueryConversations(ConversationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("unfiltered query errored (guard missing?): %v", err)
	}
	if len(all.Conversations) != 2 {
		t.Fatalf("unfiltered len = %d, want 2", len(all.Conversations))
	}

	for _, tc := range []struct {
		name  string
		query ConversationQuery
	}{
		{"channel", ConversationQuery{Channel: "signal", Limit: 10}},
		{"contact_via_q", ConversationQuery{Q: "Alice", Limit: 10}},
		{"address", ConversationQuery{Address: "+15551234567", Limit: 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := s.QueryConversations(tc.query)
			if err != nil {
				t.Fatalf("query errored (json_valid guard missing): %v", err)
			}
			if got := convIDs(page); !equalSlice(got, []string{"good"}) {
				t.Fatalf("got %v, want [good]", got)
			}
		})
	}
}

// TestQueryConversationsKeysetPaging walks every sort/order through full
// pagination and asserts the union of pages equals the correct total order
// with no dupes or skips — including a tie-heavy message_count ranking.
func TestQueryConversationsKeysetPaging(t *testing.T) {
	s := newConvQueryStore(t, 100)
	base := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	type seed struct {
		id      string
		updated time.Time
		created time.Time
		count   int
	}
	seeds := []seed{
		{"c1", base.Add(1 * time.Minute), base.Add(6 * time.Minute), 0},
		{"c2", base.Add(2 * time.Minute), base.Add(5 * time.Minute), 0},
		{"c3", base.Add(3 * time.Minute), base.Add(4 * time.Minute), 5},
		{"c4", base.Add(4 * time.Minute), base.Add(3 * time.Minute), 5},
		{"c5", base.Add(5 * time.Minute), base.Add(2 * time.Minute), 2},
		{"c6", base.Add(6 * time.Minute), base.Add(1 * time.Minute), 2},
	}
	for _, sd := range seeds {
		seedConv(t, s, sd.id,
			sd.created.Format(time.RFC3339Nano), sd.updated.Format(time.RFC3339Nano), "")
		seedMessages(t, s, sd.id, "active", sd.count)
	}

	expected := func(sortKey, order string) []string {
		cp := append([]seed(nil), seeds...)
		sort.Slice(cp, func(i, j int) bool {
			var c int
			switch sortKey {
			case "updated_at":
				c = cp[i].updated.Compare(cp[j].updated)
			case "created_at":
				c = cp[i].created.Compare(cp[j].created)
			case "message_count":
				c = cp[i].count - cp[j].count
			}
			if c == 0 {
				switch {
				case cp[i].id < cp[j].id:
					c = -1
				case cp[i].id > cp[j].id:
					c = 1
				}
			}
			if order == "desc" {
				return c > 0
			}
			return c < 0
		})
		out := make([]string, len(cp))
		for i, sd := range cp {
			out[i] = sd.id
		}
		return out
	}

	for _, sortKey := range []string{"updated_at", "created_at", "message_count"} {
		for _, order := range []string{"desc", "asc"} {
			name := sortKey + "_" + order
			t.Run(name, func(t *testing.T) {
				q := ConversationQuery{Sort: sortKey, Order: order, Limit: 2}
				var got []string
				for i := 0; i < 100; i++ {
					page, err := s.QueryConversations(q)
					if err != nil {
						t.Fatalf("page %d: %v", i, err)
					}
					if page.Total != len(seeds) {
						t.Fatalf("total = %d, want %d", page.Total, len(seeds))
					}
					got = append(got, convIDs(page)...)
					if page.NextCursor == nil {
						break
					}
					q.Cursor = page.NextCursor
				}
				if want := expected(sortKey, order); !equalSlice(got, want) {
					t.Fatalf("paged order = %v, want %v", got, want)
				}
			})
		}
	}
}

// TestQueryConversationsMessageFilters covers min/max via *int (absent vs 0),
// the empty-stub case, the impossible range, and total reflecting the filter.
func TestQueryConversationsMessageFilters(t *testing.T) {
	s := newConvQueryStore(t, 100)
	seedConv(t, s, "empty", "2026-06-24T10:00:00Z", "2026-06-24T10:00:00Z", "")
	seedConv(t, s, "one", "2026-06-24T11:00:00Z", "2026-06-24T11:00:00Z", "")
	seedMessages(t, s, "one", "active", 1)
	seedConv(t, s, "five", "2026-06-24T12:00:00Z", "2026-06-24T12:00:00Z", "")
	seedMessages(t, s, "five", "active", 5)

	zero := 0
	one := 1
	two := 2
	ten := 10

	cases := []struct {
		name      string
		min, max  *int
		wantIDs   []string
		wantTotal int
	}{
		{"max0_empty_stubs", nil, &zero, []string{"empty"}, 1},
		{"min1_excludes_empty", &one, nil, []string{"five", "one"}, 2},
		{"range_2_10", &two, &ten, []string{"five"}, 1},
		{"impossible_min_gt_max", &ten, &zero, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := s.QueryConversations(ConversationQuery{
				MinMessages: tc.min, MaxMessages: tc.max, Sort: "updated_at", Order: "desc", Limit: 10,
			})
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if got := convIDs(page); !equalSlice(got, tc.wantIDs) {
				t.Fatalf("ids = %v, want %v", got, tc.wantIDs)
			}
			if page.Total != tc.wantTotal {
				t.Fatalf("total = %d, want %d", page.Total, tc.wantTotal)
			}
		})
	}
}

// TestQueryConversationsTrueCount asserts message_count is the true uncapped
// active count, not the legacy min(active, maxMessages).
func TestQueryConversationsTrueCount(t *testing.T) {
	s := newConvQueryStore(t, 3) // small cap; legacy len(GetMessages) would report 3
	seedConv(t, s, "big", "2026-06-24T10:00:00Z", "2026-06-24T10:00:00Z", "")
	seedMessages(t, s, "big", "active", 5)
	seedMessages(t, s, "big", "compacted", 4) // must NOT be counted

	page, err := s.QueryConversations(ConversationQuery{IDs: []string{"big"}, Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Conversations) != 1 || page.Conversations[0].MessageCount != 5 {
		t.Fatalf("message_count = %+v, want 5 (uncapped active only)", page.Conversations)
	}
	// Sanity: the legacy capped path still reports the cap.
	if n := len(s.GetMessages("big")); n != 3 {
		t.Fatalf("GetMessages len = %d, want 3 (the cap this test contrasts)", n)
	}
}

// TestQueryConversationsFiltersAndEmpty covers ids, kind prefixes, channel
// binding population, and the zero-match contract.
func TestQueryConversationsFiltersAndEmpty(t *testing.T) {
	s := newConvQueryStore(t, 100)
	seedConv(t, s, "loop-1", "2026-06-24T10:00:00Z", "2026-06-24T10:00:00Z", "")
	seedConv(t, s, "sched-1", "2026-06-24T11:00:00Z", "2026-06-24T11:00:00Z", "")
	seedConv(t, s, "signal-+1555", "2026-06-24T12:00:00Z", "2026-06-24T12:00:00Z",
		`{"channel_binding":{"channel":"signal","contact_name":"Bob"}}`)

	t.Run("ids", func(t *testing.T) {
		page, err := s.QueryConversations(ConversationQuery{IDs: []string{"loop-1", "signal-+1555"}, Limit: 10})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if got := convIDs(page); !equalSlice(got, []string{"signal-+1555", "loop-1"}) {
			t.Fatalf("ids filter = %v", got)
		}
	})

	t.Run("kind_single", func(t *testing.T) {
		page, _ := s.QueryConversations(ConversationQuery{Kinds: []string{"signal"}, Limit: 10})
		if got := convIDs(page); !equalSlice(got, []string{"signal-+1555"}) {
			t.Fatalf("kind=signal = %v", got)
		}
		if cb := page.Conversations[0].ChannelBinding; cb == nil || cb.ContactName != "Bob" {
			t.Fatalf("channel_binding not populated: %+v", page.Conversations[0])
		}
	})

	t.Run("kind_multi", func(t *testing.T) {
		page, _ := s.QueryConversations(ConversationQuery{Kinds: []string{"loop", "sched"}, Order: "asc", Limit: 10})
		if got := convIDs(page); !equalSlice(got, []string{"loop-1", "sched-1"}) {
			t.Fatalf("kind=loop,sched = %v", got)
		}
	})

	t.Run("zero_match", func(t *testing.T) {
		page, err := s.QueryConversations(ConversationQuery{Kinds: []string{"nonexistent"}, Limit: 10})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(page.Conversations) != 0 || page.Total != 0 || page.NextCursor != nil {
			t.Fatalf("zero-match = %+v, want empty/total0/nilcursor", page)
		}
	})
}

// TestQueryConversationsUnparseableTimestamp covers a row whose timestamp
// strftime cannot parse (→ NULL). It must never crash the page; it is excluded
// from TIME-sorted views (no valid position in a time ordering, and it would
// otherwise yield an empty keyset cursor), but still surfaces under a
// non-time sort with a tolerated zero timestamp.
func TestQueryConversationsUnparseableTimestamp(t *testing.T) {
	s := newConvQueryStore(t, 100)
	seedConv(t, s, "ok", "2026-06-24T10:00:00Z", "2026-06-24T10:00:00Z", "")
	seedConv(t, s, "garbage", "not-a-timestamp", "not-a-timestamp", "")

	// Time sort (the default): garbage row excluded, no error, total agrees.
	timeSorted, err := s.QueryConversations(ConversationQuery{Sort: "updated_at", Limit: 10})
	if err != nil {
		t.Fatalf("time-sorted query errored on unparseable timestamp: %v", err)
	}
	if got := convIDs(timeSorted); !equalSlice(got, []string{"ok"}) {
		t.Fatalf("updated_at sort = %v, want [ok] (garbage excluded)", got)
	}
	if timeSorted.Total != 1 {
		t.Fatalf("time-sorted total = %d, want 1", timeSorted.Total)
	}

	// Non-time sort: garbage surfaces, with a tolerated zero timestamp.
	countSorted, err := s.QueryConversations(ConversationQuery{Sort: "message_count", Limit: 10})
	if err != nil {
		t.Fatalf("count-sorted query: %v", err)
	}
	if len(countSorted.Conversations) != 2 {
		t.Fatalf("message_count sort len = %d, want 2 (garbage included)", len(countSorted.Conversations))
	}
	for _, c := range countSorted.Conversations {
		if c.ID == "garbage" && !c.UpdatedAt.IsZero() {
			t.Fatalf("garbage row UpdatedAt = %v, want zero", c.UpdatedAt)
		}
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

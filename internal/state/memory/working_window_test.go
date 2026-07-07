package memory

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// insertActiveAt inserts a single active row with an explicit id and
// timestamp — used where two rows must share a timestamp (the shared-id
// helper insertMessageAt would collide on its ts-derived primary key).
func insertActiveAt(t *testing.T, store *SQLiteStore, convID, id, role, content string, ts time.Time) {
	t.Helper()
	if _, err := store.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, status)
		VALUES (?, ?, ?, ?, ?, 100, 'active')
	`, id, convID, role, content, ts); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

func newWindowStore(t *testing.T, maxMessages int) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(t.TempDir()+"/window.db", maxMessages)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.GetOrCreateConversation("conv-1"); err != nil {
		t.Fatalf("GetOrCreateConversation: %v", err)
	}
	return store
}

// TestGetMessages_OverflowReturnsNewestNotOldest is the core freeze
// regression guard: when the active set exceeds maxMessages, the window
// must be the NEWEST rows, not the oldest. The old query returned the
// oldest maxMessages, so context froze at that point forever.
func TestGetMessages_OverflowReturnsNewestNotOldest(t *testing.T) {
	store := newWindowStore(t, 5)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := 1; i <= 12; i++ {
		insertActiveAt(t, store, "conv-1",
			msgID(i), "user", msgContent(i), base.Add(time.Duration(i)*time.Minute))
	}

	got := store.GetMessages("conv-1")
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	// Chronological ASC, newest window: m08..m12.
	if got[0].Content != msgContent(8) {
		t.Errorf("first = %q, want %q (newest window, not the frozen oldest)", got[0].Content, msgContent(8))
	}
	if got[len(got)-1].Content != msgContent(12) {
		t.Errorf("last = %q, want %q (newest message)", got[len(got)-1].Content, msgContent(12))
	}
	for _, m := range got {
		if m.Content == msgContent(1) {
			t.Fatalf("oldest message m01 present — window is still frozen at the oldest rows")
		}
	}
	assertAscending(t, got)
}

// TestGetMessages_OverflowPreservesCompactionSummary proves the
// compaction summary (stamped at an OLD timestamp, outside a naive
// newest-N window) is force-included even when the active set overflows.
func TestGetMessages_OverflowPreservesCompactionSummary(t *testing.T) {
	store := newWindowStore(t, 5)
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

	// Summary at the oldest position.
	insertActiveAt(t, store, "conv-1", "summary-0", "system",
		CompactionSummaryPrefix+"\n\ncondensed older history", base)

	// 20 newer short dialogue rows (tokens stay well under any threshold).
	for i := 1; i <= 20; i++ {
		insertActiveAt(t, store, "conv-1",
			msgID(i), "user", msgContent(i), base.Add(time.Duration(i)*time.Minute))
	}

	got := store.GetMessages("conv-1")
	if len(got) != 6 { // 5 newest dialogue + 1 summary
		t.Fatalf("len = %d, want 6 (5 newest + summary)", len(got))
	}
	if got[0].Role != "system" || !strings.HasPrefix(got[0].Content, CompactionSummaryPrefix) {
		t.Fatalf("first row = %+v, want the compaction summary at the head", got[0])
	}
	summaries := 0
	for _, m := range got {
		if m.Role == "system" && strings.HasPrefix(m.Content, CompactionSummaryPrefix) {
			summaries++
		}
	}
	if summaries != 1 {
		t.Errorf("summary count = %d, want exactly 1", summaries)
	}
	// The five dialogue rows must be the newest: m16..m20.
	if got[len(got)-1].Content != msgContent(20) {
		t.Errorf("last = %q, want %q", got[len(got)-1].Content, msgContent(20))
	}
	assertAscending(t, got)
}

// TestGetMessages_ExcludesCompactedRows pins the recent-CTE's
// status='active' filter: a compacted original (replaced by a summary)
// must never leak back into the working window, even when the active set
// is under maxMessages and the newest-N reach extends back over it — the
// normal post-compaction steady state.
func TestGetMessages_ExcludesCompactedRows(t *testing.T) {
	store := newWindowStore(t, 10)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	// A raw row that compaction folded away (status='compacted'), at an
	// old ts within the newest-N reach.
	if _, err := store.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, status)
		VALUES ('compacted-orig', 'conv-1', 'user', 'raw row replaced by summary', ?, 100, 'compacted')
	`, base.Add(1*time.Minute)); err != nil {
		t.Fatalf("insert compacted: %v", err)
	}
	// Its replacement summary, plus live dialogue.
	insertActiveAt(t, store, "conv-1", "sum", "system",
		CompactionSummaryPrefix+"\n\nreplaces older history", base.Add(30*time.Second))
	for i := 1; i <= 3; i++ {
		insertActiveAt(t, store, "conv-1", msgID(i), "user", msgContent(i), base.Add(time.Duration(i+2)*time.Minute))
	}

	for _, m := range store.GetMessages("conv-1") {
		if m.ID == "compacted-orig" {
			t.Fatalf("compacted row leaked into working window: %+v", m)
		}
	}
}

// TestGetMessages_SummaryInsideWindowNotDuplicated guards against a
// UNION-ALL regression: a summary that also falls inside the newest-N
// window must appear exactly once.
func TestGetMessages_SummaryInsideWindowNotDuplicated(t *testing.T) {
	store := newWindowStore(t, 10)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	insertActiveAt(t, store, "conv-1", "u1", "user", "u1", base.Add(1*time.Minute))
	insertActiveAt(t, store, "conv-1", "u2", "user", "u2", base.Add(2*time.Minute))
	insertActiveAt(t, store, "conv-1", "summary-recent", "system",
		CompactionSummaryPrefix+"\n\nrecent summary", base.Add(3*time.Minute))
	insertActiveAt(t, store, "conv-1", "u3", "user", "u3", base.Add(4*time.Minute))

	got := store.GetMessages("conv-1")
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4 (no duplicate summary)", len(got))
	}
	summaries := 0
	for _, m := range got {
		if strings.HasPrefix(m.Content, CompactionSummaryPrefix) {
			summaries++
		}
	}
	if summaries != 1 {
		t.Errorf("summary count = %d, want 1 (UNION must dedupe)", summaries)
	}
	assertAscending(t, got)
}

// TestGetMessages_UnderMaxUnchanged confirms the common (non-overflow)
// path is a behavioral no-op: every active row, chronological ASC.
func TestGetMessages_UnderMaxUnchanged(t *testing.T) {
	store := newWindowStore(t, 100)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	roles := []string{"user", "assistant", "user", "assistant", "system", "user", "assistant", "user"}
	for i, role := range roles {
		insertActiveAt(t, store, "conv-1", msgID(i), role, msgContent(i), base.Add(time.Duration(i)*time.Minute))
	}
	got := store.GetMessages("conv-1")
	if len(got) != len(roles) {
		t.Fatalf("len = %d, want %d (all active rows)", len(got), len(roles))
	}
	assertAscending(t, got)
}

// TestGetMessages_SameTimestampTiebreakDeterministic simulates #1220's
// rapid mid-turn mailbox inserts that collide on the wall clock. The id
// tiebreak must make the window cut and display order stable.
func TestGetMessages_SameTimestampTiebreakDeterministic(t *testing.T) {
	store := newWindowStore(t, 3)
	ts := time.Now().Add(-time.Hour).Truncate(time.Second)
	// Six rows, identical timestamp, ascending ids (UUIDv7 is time-monotonic).
	for i := 1; i <= 6; i++ {
		insertActiveAt(t, store, "conv-1", msgID(i), "user", msgContent(i), ts)
	}
	first := store.GetMessages("conv-1")
	second := store.GetMessages("conv-1")
	if len(first) != 3 {
		t.Fatalf("len = %d, want 3", len(first))
	}
	if !sameContents(first, second) {
		t.Fatalf("nondeterministic window across calls: %v vs %v", contents(first), contents(second))
	}
	// Highest ids (latest inserts) win the window: m04, m05, m06.
	want := []string{msgContent(4), msgContent(5), msgContent(6)}
	if got := contents(first); !equalStrings(got, want) {
		t.Errorf("window = %v, want %v (highest-id rows, ASC)", got, want)
	}
}

// TestGetMessages_ClipWarnEmittedThenThrottled exercises the goal-3
// observability signal: a clip warns once, then throttles; no clip stays
// silent.
func TestGetMessages_ClipWarnEmittedThenThrottled(t *testing.T) {
	tests := []struct {
		name        string
		maxMessages int
		rows        int
		calls       int
		wantWarns   int
	}{
		{name: "overflow warns once then throttles", maxMessages: 3, rows: 10, calls: 2, wantWarns: 1},
		{name: "no overflow stays silent", maxMessages: 100, rows: 5, calls: 1, wantWarns: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			store, err := NewSQLiteStoreWithLogger(t.TempDir()+"/clip.db", tc.maxMessages, logger)
			if err != nil {
				t.Fatalf("NewSQLiteStoreWithLogger: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.GetOrCreateConversation("conv-1"); err != nil {
				t.Fatalf("GetOrCreateConversation: %v", err)
			}
			base := time.Now().Add(-time.Hour).Truncate(time.Second)
			for i := 1; i <= tc.rows; i++ {
				insertActiveAt(t, store, "conv-1", msgID(i), "user", msgContent(i), base.Add(time.Duration(i)*time.Minute))
			}
			for range tc.calls {
				_ = store.GetMessages("conv-1")
			}
			got := strings.Count(buf.String(), "read window clipped")
			if got != tc.wantWarns {
				t.Errorf("clip warnings = %d, want %d\nlog:\n%s", got, tc.wantWarns, buf.String())
			}
		})
	}
}

// TestClear_EvictsClipWarnEntry pins that Clear() removes the clip-warn
// rate-limit entry, so per-invocation delegate conversation IDs don't
// leak map entries for the life of the process.
func TestClear_EvictsClipWarnEntry(t *testing.T) {
	store := newWindowStore(t, 3)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := 1; i <= 5; i++ {
		insertActiveAt(t, store, "conv-1", msgID(i), "user", msgContent(i), base.Add(time.Duration(i)*time.Minute))
	}
	_ = store.GetMessages("conv-1") // overflow -> records a clip-warn entry

	if _, ok := store.clipWarnAt["conv-1"]; !ok {
		t.Fatalf("expected a clip-warn entry after an overflowing read")
	}
	if err := store.Clear("conv-1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok := store.clipWarnAt["conv-1"]; ok {
		t.Fatalf("clip-warn entry survived Clear() — map leaks per conversation")
	}
}

// TestActiveMessageCount_ExcludesSystemAndCompacted proves the count
// trigger measures only the reducible dialogue set.
func TestActiveMessageCount_ExcludesSystemAndCompacted(t *testing.T) {
	store := newWindowStore(t, 100)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	insertActiveAt(t, store, "conv-1", "u1", "user", "u1", base.Add(1*time.Minute))
	insertActiveAt(t, store, "conv-1", "a1", "assistant", "a1", base.Add(2*time.Minute))
	insertActiveAt(t, store, "conv-1", "u2", "user", "u2", base.Add(3*time.Minute))
	insertActiveAt(t, store, "conv-1", "a2", "assistant", "a2", base.Add(4*time.Minute))
	// An active summary system row (excluded — role system).
	insertActiveAt(t, store, "conv-1", "sum", "system", CompactionSummaryPrefix+"\n\nx", base)
	// A compacted dialogue row (excluded — not active).
	if _, err := store.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, status)
		VALUES ('old', 'conv-1', 'user', 'compacted away', ?, 100, 'compacted')
	`, base.Add(-time.Minute)); err != nil {
		t.Fatalf("insert compacted: %v", err)
	}

	if got := store.ActiveMessageCount("conv-1"); got != 4 {
		t.Errorf("ActiveMessageCount = %d, want 4 (excludes system + compacted)", got)
	}
}

// fakeCompactable isolates the two NeedsCompaction gates.
type fakeCompactable struct {
	tokens int
	count  int
}

func (f fakeCompactable) GetTokenCount(string) int                       { return f.tokens }
func (f fakeCompactable) ActiveMessageCount(string) int                  { return f.count }
func (f fakeCompactable) GetMessagesForCompaction(string, int) []Message { return nil }
func (f fakeCompactable) GetActiveCompactionSummaries(string) ([]Message, error) {
	return nil, nil
}
func (f fakeCompactable) ApplyCompaction(string, []string, string, time.Time) error { return nil }

func TestNeedsCompaction_TokenOrCountTrigger(t *testing.T) {
	// threshold = 2000 * 0.5 = 1000; count trigger at 6.
	cfg := CompactionConfig{MaxTokens: 2000, TriggerRatio: 0.5, MaxActiveMessages: 6}
	tests := []struct {
		name   string
		tokens int
		count  int
		want   bool
	}{
		{name: "short-message overflow: low tokens, high count", tokens: 100, count: 9, want: true},
		{name: "token overflow: high tokens, low count", tokens: 1500, count: 3, want: true},
		{name: "neither: quiet conversation", tokens: 100, count: 3, want: false},
		{name: "both", tokens: 1500, count: 9, want: true},
		{name: "count at threshold boundary", tokens: 100, count: 6, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCompactor(fakeCompactable{tokens: tc.tokens, count: tc.count}, cfg, nil, slog.Default())
			if got := c.NeedsCompaction("conv-1"); got != tc.want {
				t.Errorf("NeedsCompaction(tokens=%d,count=%d) = %v, want %v", tc.tokens, tc.count, got, tc.want)
			}
		})
	}
}

func TestNeedsCompaction_CountTriggerDisabledWhenZero(t *testing.T) {
	cfg := CompactionConfig{MaxTokens: 2000, TriggerRatio: 0.5, MaxActiveMessages: 0}
	c := NewCompactor(fakeCompactable{tokens: 100, count: 500}, cfg, nil, slog.Default())
	if c.NeedsCompaction("conv-1") {
		t.Errorf("NeedsCompaction = true, want false (MaxActiveMessages=0 disables the count gate)")
	}
}

// --- small test helpers ---

func msgID(i int) string      { return "id-" + pad2(i) }
func msgContent(i int) string { return "m" + pad2(i) }
func pad2(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func assertAscending(t *testing.T, msgs []Message) {
	t.Helper()
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Timestamp.Before(msgs[i-1].Timestamp) {
			t.Fatalf("messages not in ascending timestamp order at %d: %v before %v", i, msgs[i].Timestamp, msgs[i-1].Timestamp)
		}
	}
}

func contents(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func sameContents(a, b []Message) bool { return equalStrings(contents(a), contents(b)) }

func equalStrings(a, b []string) bool {
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

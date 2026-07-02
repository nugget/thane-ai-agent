package memory

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newCompactionTestStore returns a store seeded with n user/assistant
// exchange pairs on convID, with timestamps spaced one minute apart
// starting at base.
func newCompactionTestStore(t *testing.T, convID string, base time.Time, pairs int) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(t.TempDir()+"/compact.db", 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.GetOrCreateConversation(convID); err != nil {
		t.Fatalf("GetOrCreateConversation: %v", err)
	}
	for i := range pairs {
		ts := base.Add(time.Duration(2*i) * time.Minute)
		insertMessageAt(t, store, convID, "user", "question about topic number "+string(rune('a'+i%26))+" with padding to carry some weight", ts)
		insertMessageAt(t, store, convID, "assistant", "answer covering topic number "+string(rune('a'+i%26))+" with padding to carry some weight", ts.Add(time.Minute))
	}
	return store
}

// insertMessageAt writes a message with a controlled timestamp — the
// production AddMessage stamps now(), which these tests can't use.
func insertMessageAt(t *testing.T, store *SQLiteStore, convID, role, content string, ts time.Time) {
	t.Helper()
	id := "msg-" + ts.Format("20060102150405.000000000") + "-" + role
	if _, err := store.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, status)
		VALUES (?, ?, ?, ?, ?, 100, 'active')
	`, id, convID, role, content, ts); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

// countingSummarizer counts invocations and optionally blocks until
// released, for exercising the single-flight guard.
type countingSummarizer struct {
	calls   atomic.Int32
	block   chan struct{} // nil = don't block
	sawText []string
	mu      sync.Mutex
}

func (c *countingSummarizer) Summarize(_ context.Context, messages []Message, _ string) (string, error) {
	c.calls.Add(1)
	var sb strings.Builder
	for _, m := range messages {
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	c.mu.Lock()
	c.sawText = append(c.sawText, sb.String())
	c.mu.Unlock()
	if c.block != nil {
		<-c.block
	}
	return "condensed summary", nil
}

func compactorFor(store *SQLiteStore, sum Summarizer) *Compactor {
	return NewCompactor(store, CompactionConfig{
		MaxTokens:            2000, // low budget so the seeded rows trigger
		TriggerRatio:         0.5,
		KeepRecent:           4,
		MinMessagesToCompact: 6,
	}, sum, slog.Default())
}

func TestCompaction_SingleFlightCoalescesConcurrentRuns(t *testing.T) {
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	store := newCompactionTestStore(t, "conv-1", base, 15)

	sum := &countingSummarizer{block: make(chan struct{})}
	c := compactorFor(store, sum)

	first := make(chan error, 1)
	go func() { first <- c.Compact(context.Background(), "conv-1") }()

	// Wait for the first run to enter Summarize, then race a second.
	deadline := time.After(5 * time.Second)
	for sum.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("first compaction never reached the summarizer")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := c.Compact(context.Background(), "conv-1"); err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	if got := sum.calls.Load(); got != 1 {
		t.Errorf("summarizer calls during flight = %d, want 1 (second run must coalesce)", got)
	}

	close(sum.block)
	if err := <-first; err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	if got := len(store.GetActiveCompactionSummaries("conv-1")); got != 1 {
		t.Errorf("active summaries = %d, want exactly 1", got)
	}
}

func TestCompaction_FoldsPriorSummariesIntoOne(t *testing.T) {
	base := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	store := newCompactionTestStore(t, "conv-1", base, 15)

	sum := &countingSummarizer{}
	c := compactorFor(store, sum)

	if err := c.Compact(context.Background(), "conv-1"); err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	// New traffic re-arms the trigger past the first summary.
	for i := range 12 {
		ts := base.Add(2 * time.Hour).Add(time.Duration(2*i) * time.Minute)
		insertMessageAt(t, store, "conv-1", "user", "later question with enough padding to count tokens", ts)
		insertMessageAt(t, store, "conv-1", "assistant", "later answer with enough padding to count tokens", ts.Add(time.Minute))
	}
	if err := c.Compact(context.Background(), "conv-1"); err != nil {
		t.Fatalf("second Compact: %v", err)
	}

	summaries := store.GetActiveCompactionSummaries("conv-1")
	if len(summaries) != 1 {
		t.Fatalf("active summaries = %d, want exactly 1 (priors must fold, not stack)", len(summaries))
	}
	// The second summarizer invocation must have received the first
	// summary's content, so nothing silently vanishes in the fold.
	sum.mu.Lock()
	lastInput := sum.sawText[len(sum.sawText)-1]
	sum.mu.Unlock()
	if !strings.Contains(lastInput, CompactionSummaryPrefix) {
		t.Errorf("second summarize input missing the prior summary:\n%s", lastInput)
	}
}

func TestCompaction_SummaryTakesCompactedRegionPosition(t *testing.T) {
	base := time.Now().Add(-4 * time.Hour).Truncate(time.Second)
	store := newCompactionTestStore(t, "conv-1", base, 15)

	c := compactorFor(store, &countingSummarizer{})
	if err := c.Compact(context.Background(), "conv-1"); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// The summary must render FIRST in active history — at the
	// compacted region's position — never interleaved after surviving
	// messages the way a now() stamp would place it.
	messages := store.GetMessages("conv-1")
	if len(messages) == 0 {
		t.Fatal("no active messages after compaction")
	}
	if !strings.HasPrefix(messages[0].Content, CompactionSummaryPrefix) {
		t.Errorf("messages[0] is %q..., want the compaction summary at the head of history", messages[0].Content[:min(40, len(messages[0].Content))])
	}
	for i, m := range messages[1:] {
		if strings.HasPrefix(m.Content, CompactionSummaryPrefix) {
			t.Errorf("summary found at position %d — interleaved into live dialogue", i+1)
		}
	}
}

func TestCompaction_BoundarySnapsToTurnEdge(t *testing.T) {
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	store := newCompactionTestStore(t, "conv-1", base, 7)
	// Arrange the boundary to fall right after a user message: with
	// KeepRecent=4, the candidate set ends on the user half of the
	// 5th-from-last exchange.
	ts := base.Add(30 * time.Minute)
	insertMessageAt(t, store, "conv-1", "user", "dangling question whose answer is in the keep window", ts)
	insertMessageAt(t, store, "conv-1", "assistant", "the answer that must not be orphaned", ts.Add(time.Minute))

	c := compactorFor(store, &countingSummarizer{})
	if err := c.Compact(context.Background(), "conv-1"); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Every surviving non-summary message run must not START with an
	// orphaned assistant reply: the message right after the summary is
	// either a user message or nothing.
	messages := store.GetMessages("conv-1")
	if len(messages) < 2 {
		t.Fatalf("unexpectedly few messages: %d", len(messages))
	}
	if messages[1].Role == "assistant" {
		t.Errorf("first message after summary is an assistant reply (%q) — turn pair split", messages[1].Content[:min(40, len(messages[1].Content))])
	}
}

func TestCompaction_ReleasesFlightGuardOnSkip(t *testing.T) {
	// A conversation below MinMessages must release the single-flight
	// guard so later, larger compactions aren't locked out forever.
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	store := newCompactionTestStore(t, "conv-1", base, 3)

	sum := &countingSummarizer{}
	c := compactorFor(store, sum)
	if err := c.Compact(context.Background(), "conv-1"); err != nil {
		t.Fatalf("skip Compact: %v", err)
	}
	for i := range 12 {
		ts := base.Add(30 * time.Minute).Add(time.Duration(2*i) * time.Minute)
		insertMessageAt(t, store, "conv-1", "user", "more traffic with padding for the token estimator", ts)
		insertMessageAt(t, store, "conv-1", "assistant", "more replies with padding for the token estimator", ts.Add(time.Minute))
	}
	if err := c.Compact(context.Background(), "conv-1"); err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	if sum.calls.Load() == 0 {
		t.Error("second compaction never ran — flight guard leaked on the skip path")
	}
}

func TestCompactionStats(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir()+"/stats.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	config := DefaultCompactionConfig()
	compactor := NewCompactor(store, config, &SimpleSummarizer{}, slog.Default())

	stats := compactor.CompactionStats("test")
	if stats["max_tokens"] != config.MaxTokens {
		t.Errorf("Expected max_tokens %d, got %v", config.MaxTokens, stats["max_tokens"])
	}
}

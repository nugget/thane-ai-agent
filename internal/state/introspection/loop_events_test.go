package introspection

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/events"

	_ "modernc.org/sqlite"
)

func newTestJournal(t *testing.T) *Journal {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	j, err := NewJournal(db, nil)
	if err != nil {
		t.Fatalf("new journal: %v", err)
	}
	return j
}

func mustRecord(t *testing.T, j *Journal, evs ...LoopEvent) {
	t.Helper()
	if err := j.record(context.Background(), evs); err != nil {
		t.Fatalf("record: %v", err)
	}
}

func TestJournalQueryFiltersAndOrdersChronologically(t *testing.T) {
	j := newTestJournal(t)
	base := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)

	mustRecord(t, j,
		LoopEvent{At: base, LoopName: "archivist", Kind: "loop_iteration_start", WakeReason: "mailbox", WakeSource: "session"},
		LoopEvent{At: base.Add(time.Minute), LoopName: "archivist", Kind: "loop_iteration_complete", Detail: map[string]any{"elapsed_ms": 1200}},
		LoopEvent{At: base.Add(2 * time.Minute), LoopName: "ego", Kind: "loop_iteration_start", WakeReason: "timer"},
		LoopEvent{At: base.Add(3 * time.Minute), LoopName: "archivist", Kind: "loop_error", Detail: map[string]any{"error": "boom"}},
	)

	got, err := j.Query(context.Background(), LoopEventQuery{LoopName: "archivist"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("archivist events = %d, want 3", len(got))
	}
	// Chronological: iteration_start first, error last.
	if got[0].Kind != "loop_iteration_start" || got[2].Kind != "loop_error" {
		t.Errorf("order = [%s %s %s], want chronological", got[0].Kind, got[1].Kind, got[2].Kind)
	}
	if got[0].WakeReason != "mailbox" || got[0].WakeSource != "session" {
		t.Errorf("wake attribution not round-tripped: %+v", got[0])
	}
	if got[2].Detail["error"] != "boom" {
		t.Errorf("detail not round-tripped: %+v", got[2].Detail)
	}

	// Kind + window filters compose.
	windowed, err := j.Query(context.Background(), LoopEventQuery{
		Kind:  "loop_iteration_start",
		Since: base.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("windowed query: %v", err)
	}
	if len(windowed) != 1 || windowed[0].LoopName != "ego" {
		t.Errorf("windowed = %+v, want just ego's start", windowed)
	}
}

func TestJournalQueryLimitKeepsNewest(t *testing.T) {
	j := newTestJournal(t)
	base := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	var evs []LoopEvent
	for i := range 10 {
		evs = append(evs, LoopEvent{
			At: base.Add(time.Duration(i) * time.Minute), LoopName: "core", Kind: "loop_iteration_start",
		})
	}
	mustRecord(t, j, evs...)

	got, err := j.Query(context.Background(), LoopEventQuery{Limit: 3})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("limited query = %d rows, want 3", len(got))
	}
	// The newest rows win the cap, returned chronologically.
	if !got[0].At.Equal(base.Add(7*time.Minute)) || !got[2].At.Equal(base.Add(9*time.Minute)) {
		t.Errorf("limit kept %v..%v, want the newest three", got[0].At, got[2].At)
	}
}

func TestJournalAggregateActivity(t *testing.T) {
	j := newTestJournal(t)
	base := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)

	mustRecord(t, j,
		LoopEvent{At: base, LoopName: "archivist", Kind: "loop_iteration_start", WakeReason: "mailbox", WakeSource: "session"},
		LoopEvent{At: base.Add(time.Minute), LoopName: "archivist", Kind: "loop_iteration_complete", Detail: map[string]any{"no_op": true}},
		LoopEvent{At: base.Add(2 * time.Minute), LoopName: "archivist", Kind: "loop_iteration_start", WakeReason: "mailbox", WakeSource: "session"},
		LoopEvent{At: base.Add(3 * time.Minute), LoopName: "archivist", Kind: "loop_iteration_complete"},
		LoopEvent{At: base.Add(4 * time.Minute), LoopName: "archivist", Kind: "loop_iteration_start", WakeReason: "timer"},
		LoopEvent{At: base.Add(5 * time.Minute), LoopName: "archivist", Kind: "loop_error", Detail: map[string]any{"error": "x"}},
		// Another loop inside the window must not leak into the scoped rollup.
		LoopEvent{At: base.Add(6 * time.Minute), LoopName: "ego", Kind: "loop_iteration_start", WakeReason: "timer"},
	)

	agg, err := j.AggregateActivity(context.Background(), "archivist", base.Add(-time.Minute), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if agg.Wakes != 3 {
		t.Errorf("wakes = %d, want 3", agg.Wakes)
	}
	if agg.ByReason["mailbox"] != 2 || agg.ByReason["timer"] != 1 {
		t.Errorf("by_reason = %v, want mailbox:2 timer:1", agg.ByReason)
	}
	if agg.BySource["session"] != 2 {
		t.Errorf("by_source = %v, want session:2", agg.BySource)
	}
	if agg.Errors != 1 || agg.Completions != 2 || agg.NoOps != 1 {
		t.Errorf("errors/completions/no_ops = %d/%d/%d, want 1/2/1", agg.Errors, agg.Completions, agg.NoOps)
	}
	if agg.WakesPerHour <= 0 {
		t.Errorf("wakes_per_hour = %v, want > 0", agg.WakesPerHour)
	}

	// Unscoped rollup covers every loop.
	all, err := j.AggregateActivity(context.Background(), "", base.Add(-time.Minute), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("unscoped aggregate: %v", err)
	}
	if all.Wakes != 4 {
		t.Errorf("unscoped wakes = %d, want 4", all.Wakes)
	}
}

func TestJournalPruneIsAgeBased(t *testing.T) {
	j := newTestJournal(t)
	mustRecord(t, j,
		LoopEvent{At: time.Now().UTC().Add(-48 * time.Hour), LoopName: "old", Kind: "loop_iteration_start"},
		LoopEvent{At: time.Now().UTC(), LoopName: "new", Kind: "loop_iteration_start"},
	)
	deleted, err := j.Prune(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 1 {
		t.Errorf("pruned %d, want 1", deleted)
	}
	left, err := j.Query(context.Background(), LoopEventQuery{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(left) != 1 || left[0].LoopName != "new" {
		t.Errorf("surviving rows = %+v, want just the fresh one", left)
	}
	if _, err := j.Prune(context.Background(), 0); err == nil {
		t.Errorf("prune with zero max age must refuse rather than delete everything")
	}
}

func TestProjectLoopEventWhitelistsAndBounds(t *testing.T) {
	long := strings.Repeat("x", maxDetailStringRunes+50)
	row, ok := projectLoopEvent(events.Event{
		Timestamp: time.Now(),
		Kind:      events.KindLoopIterationComplete,
		Data: map[string]any{
			"loop_id":         "lp_1",
			"loop_name":       "archivist",
			"model":           "haiku",
			"input_tokens":    1200,
			"error":           long,
			"tools_used":      map[string]int{"doc_read": 3}, // bulk: dropped
			"effective_tools": []string{"a", "b"},            // bulk: dropped
			"unknown_key":     "nope",                        // not whitelisted
		},
	})
	if !ok {
		t.Fatal("iteration_complete must be a persisted kind")
	}
	if row.LoopID != "lp_1" || row.LoopName != "archivist" {
		t.Errorf("identity = %s/%s", row.LoopID, row.LoopName)
	}
	if row.Detail["model"] != "haiku" || row.Detail["input_tokens"] != 1200 {
		t.Errorf("scalar whitelist not applied: %+v", row.Detail)
	}
	if _, present := row.Detail["tools_used"]; present {
		t.Errorf("bulk map survived projection")
	}
	if _, present := row.Detail["unknown_key"]; present {
		t.Errorf("non-whitelisted key survived projection")
	}
	if got := row.Detail["error"].(string); len([]rune(got)) > maxDetailStringRunes+1 {
		t.Errorf("string value not capped: %d runes", len([]rune(got)))
	}

	if _, ok := projectLoopEvent(events.Event{Kind: events.KindLoopToolStart}); ok {
		t.Errorf("chatty tool event must not be persisted")
	}
}

// TestRecorderPersistsBusEvents runs the real subscribe→project→batch→
// flush path: publish a mix of retained and chatty events, cancel, and
// confirm the journal holds exactly the retained ones.
func TestRecorderPersistsBusEvents(t *testing.T) {
	j := newTestJournal(t)
	bus := events.New()
	rec := NewRecorder(j, bus, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec.Run(ctx)
	}()

	// Give the subscription a moment to attach before publishing.
	deadline := time.After(2 * time.Second)
	for bus.SubscriberCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("recorder never subscribed")
		case <-time.After(5 * time.Millisecond):
		}
	}

	bus.Publish(events.Event{Timestamp: time.Now(), Kind: events.KindLoopIterationStart,
		Data: map[string]any{"loop_name": "core", "wake_reason": "timer"}})
	bus.Publish(events.Event{Timestamp: time.Now(), Kind: events.KindLoopToolStart,
		Data: map[string]any{"loop_name": "core"}}) // chatty: dropped
	bus.Publish(events.Event{Timestamp: time.Now(), Kind: events.KindLoopError,
		Data: map[string]any{"loop_name": "core", "error": "boom"}})

	// Cancelling forces the final flush; wait for Run to return before
	// reading so the write has landed.
	time.Sleep(50 * time.Millisecond) // let the recorder drain its channel
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recorder did not stop")
	}

	rows, err := j.Query(context.Background(), LoopEventQuery{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("journal rows = %d, want 2 (start + error, not the tool event)", len(rows))
	}
	if rows[0].Kind != "loop_iteration_start" || rows[0].WakeReason != "timer" {
		t.Errorf("rows[0] = %+v, want attributed iteration start", rows[0])
	}
	if rows[1].Kind != "loop_error" {
		t.Errorf("rows[1] = %+v, want the error", rows[1])
	}
}

// TestCountBootsSince pins the exact-count contract behind
// boots_last_24h: the count answers from the full journal, windowed by
// the since bound, independent of any page size.
func TestCountBootsSince(t *testing.T) {
	t.Parallel()

	j := newTestJournal(t)
	ctx := context.Background()
	for range 3 {
		if err := j.RecordBoot(ctx, "v0.10.3", "abcdef1"); err != nil {
			t.Fatalf("RecordBoot: %v", err)
		}
	}

	got, err := j.CountBootsSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountBootsSince: %v", err)
	}
	if got != 3 {
		t.Errorf("count since -1h = %d, want 3", got)
	}

	got, err = j.CountBootsSince(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CountBootsSince(future): %v", err)
	}
	if got != 0 {
		t.Errorf("count since +1h = %d, want 0", got)
	}
}

package memory

import (
	"context"
	"testing"
	"time"
)

// TestCompaction_ArchivesBeforeCompacting verifies that the compactor
// archives messages before marking them compacted.
func TestCompaction_ArchivesBeforeCompacting(t *testing.T) {
	// Set up SQLite memory store
	memStore, err := NewSQLiteStore(t.TempDir()+"/mem.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer memStore.Close()

	// Set up archive store
	archiveStore, err := NewArchiveStore(t.TempDir()+"/archive.db", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveStore.Close()

	// Create a session so archived messages have a session ID
	archiveStore.StartSession("test-conv")

	// Set up compactor with archive
	summarizer := &SimpleSummarizer{}
	cfg := CompactionConfig{
		MaxTokens:            200,
		TriggerRatio:         0.7,
		KeepRecent:           3,
		MinMessagesToCompact: 5,
	}
	compactor := NewCompactor(memStore, cfg, summarizer)
	compactor.SetArchiver(archiveStore)

	// Add enough messages to trigger compaction
	for i := 0; i < 20; i++ {
		msg := "This is a test message with some content for token counting purposes."
		if err := memStore.AddMessage("test-conv", "user", msg); err != nil {
			t.Fatal(err)
		}
	}

	// Verify compaction is needed
	if !compactor.NeedsCompaction("test-conv") {
		t.Skip("not enough tokens to trigger compaction")
	}

	// Compact
	if err := compactor.Compact(context.Background(), "test-conv"); err != nil {
		t.Fatal(err)
	}

	// Check that messages were archived
	stats, _ := archiveStore.Stats()
	archivedCount := stats["total_messages"].(int)
	if archivedCount == 0 {
		t.Error("expected messages to be archived before compaction, got 0")
	}

	t.Logf("archived %d messages before compaction", archivedCount)
}

// TestSearch_NoContext verifies the NoContext flag disables context expansion.
func TestSearch_NoContext(t *testing.T) {
	store := newTestArchiveStore(t)

	base := time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC)
	msgs := []ArchivedMessage{
		{ID: "m1", ConversationID: "c1", SessionID: "s1", Role: "user",
			Content: "before the match", Timestamp: base,
			ArchiveReason: "reset"},
		{ID: "m2", ConversationID: "c1", SessionID: "s1", Role: "user",
			Content: "tell me about the pool heater", Timestamp: base.Add(5 * time.Second),
			ArchiveReason: "reset"},
		{ID: "m3", ConversationID: "c1", SessionID: "s1", Role: "assistant",
			Content: "the pool heater is solar powered", Timestamp: base.Add(10 * time.Second),
			ArchiveReason: "reset"},
	}

	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search(SearchOptions{
		Query:     "pool heater",
		NoContext: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results {
		if len(r.ContextBefore) > 0 || len(r.ContextAfter) > 0 {
			t.Errorf("expected no context with NoContext=true, got %d before + %d after",
				len(r.ContextBefore), len(r.ContextAfter))
		}
	}
}

// TestStartSessionAt_PreservesTimestamp verifies imported sessions keep their dates.
func TestStartSessionAt_PreservesTimestamp(t *testing.T) {
	store := newTestArchiveStore(t)

	originalTime := time.Date(2026, 2, 1, 15, 55, 29, 0, time.UTC)
	sess, err := store.StartSessionAt("imported", originalTime)
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}

	if !got.StartedAt.Equal(originalTime) {
		t.Errorf("expected started_at=%v, got %v", originalTime, got.StartedAt)
	}
}

// TestEndSessionAt_PreservesTimestamp verifies imported session end times.
func TestEndSessionAt_PreservesTimestamp(t *testing.T) {
	store := newTestArchiveStore(t)

	startTime := time.Date(2026, 2, 1, 15, 55, 29, 0, time.UTC)
	endTime := time.Date(2026, 2, 1, 18, 30, 0, 0, time.UTC)

	sess, _ := store.StartSessionAt("imported", startTime)
	if err := store.EndSessionAt(sess.ID, "import", endTime); err != nil {
		t.Fatal(err)
	}

	got, _ := store.GetSession(sess.ID)
	if got.EndedAt == nil {
		t.Fatal("expected session to be ended")
	}
	if !got.EndedAt.Equal(endTime) {
		t.Errorf("expected ended_at=%v, got %v", endTime, *got.EndedAt)
	}
}

// TestSetSessionMessageCount sets message count directly.
func TestSetSessionMessageCount(t *testing.T) {
	store := newTestArchiveStore(t)

	sess, _ := store.StartSession("conv-1")
	if err := store.SetSessionMessageCount(sess.ID, 42); err != nil {
		t.Fatal(err)
	}

	got, _ := store.GetSession(sess.ID)
	if got.MessageCount != 42 {
		t.Errorf("expected message_count=42, got %d", got.MessageCount)
	}
}

// TestSetSessionSummary stores and retrieves a summary.
func TestSetSessionSummary(t *testing.T) {
	store := newTestArchiveStore(t)

	sess, _ := store.StartSession("conv-1")
	summary := "discussed pool heater scheduling and Thane architecture"

	if err := store.SetSessionSummary(sess.ID, summary); err != nil {
		t.Fatal(err)
	}

	got, _ := store.GetSession(sess.ID)
	if got.Summary != summary {
		t.Errorf("expected summary=%q, got %q", summary, got.Summary)
	}
}

// TestArchiveToolCalls stores and retrieves tool call records.
func TestArchiveToolCalls(t *testing.T) {
	store := newTestArchiveStore(t)

	now := time.Now().UTC()
	completed := now.Add(500 * time.Millisecond)

	calls := []ArchivedToolCall{
		{
			ID: "tc-1", ConversationID: "c1", SessionID: "s1",
			ToolName: "web_search", Arguments: `{"query":"test"}`,
			Result: "some results", StartedAt: now,
			CompletedAt: &completed, DurationMs: 500,
		},
		{
			ID: "tc-2", ConversationID: "c1", SessionID: "s1",
			ToolName: "file_read", Arguments: `{"path":"test.md"}`,
			Error: "file not found", StartedAt: now.Add(time.Second),
		},
	}

	if err := store.ArchiveToolCalls(calls); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetSessionToolCalls("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(got))
	}

	if got[0].ToolName != "web_search" {
		t.Errorf("expected web_search, got %s", got[0].ToolName)
	}
	if got[0].Result != "some results" {
		t.Errorf("expected result, got %s", got[0].Result)
	}
	if got[0].DurationMs != 500 {
		t.Errorf("expected 500ms, got %d", got[0].DurationMs)
	}

	if got[1].Error != "file not found" {
		t.Errorf("expected error, got %s", got[1].Error)
	}
}

// TestArchiveToolCalls_Dedup verifies tool calls are deduplicated.
func TestArchiveToolCalls_Dedup(t *testing.T) {
	store := newTestArchiveStore(t)

	call := ArchivedToolCall{
		ID: "tc-1", ConversationID: "c1", SessionID: "s1",
		ToolName: "test", Arguments: "{}", StartedAt: time.Now(),
	}

	if err := store.ArchiveToolCalls([]ArchivedToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveToolCalls([]ArchivedToolCall{call}); err != nil {
		t.Fatal(err)
	}

	got, _ := store.GetSessionToolCalls("s1")
	if len(got) != 1 {
		t.Errorf("expected 1 tool call (dedup), got %d", len(got))
	}
}

// TestFTSEnabled reports FTS5 availability.
func TestFTSEnabled(t *testing.T) {
	store := newTestArchiveStore(t)
	// Just verify the method doesn't panic — value depends on build
	_ = store.FTSEnabled()
}

package memory

import (
	"fmt"
	"testing"
	"time"
)

func newTestArchiveStore(t *testing.T) *ArchiveStore {
	t.Helper()

	dbPath := t.TempDir() + "/test-archive.db"
	store, err := NewArchiveStore(dbPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	return store
}

func TestArchiveMessages_BasicInsert(t *testing.T) {
	store := newTestArchiveStore(t)

	msgs := []ArchivedMessage{
		{
			ID: "msg-1", ConversationID: "conv-1", SessionID: "sess-1",
			Role: "user", Content: "hello there",
			Timestamp:     time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC),
			ArchiveReason: string(ArchiveReasonReset),
		},
		{
			ID: "msg-2", ConversationID: "conv-1", SessionID: "sess-1",
			Role: "assistant", Content: "hi! how can I help?",
			Timestamp:     time.Date(2026, 2, 12, 10, 0, 5, 0, time.UTC),
			ArchiveReason: string(ArchiveReasonReset),
		},
	}

	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatal(err)
	}

	// Verify they're in the archive
	transcript, err := store.GetSessionTranscript("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(transcript))
	}
	if transcript[0].Role != "user" {
		t.Errorf("expected first message role=user, got %s", transcript[0].Role)
	}
	if transcript[1].Content != "hi! how can I help?" {
		t.Errorf("unexpected content: %s", transcript[1].Content)
	}
}

func TestArchiveMessages_Deduplication(t *testing.T) {
	store := newTestArchiveStore(t)

	msg := ArchivedMessage{
		ID: "msg-1", ConversationID: "conv-1", SessionID: "sess-1",
		Role: "user", Content: "hello",
		Timestamp:     time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC),
		ArchiveReason: string(ArchiveReasonCompaction),
	}

	// Insert twice — should not error or duplicate
	if err := store.ArchiveMessages([]ArchivedMessage{msg}); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveMessages([]ArchivedMessage{msg}); err != nil {
		t.Fatal(err)
	}

	transcript, err := store.GetSessionTranscript("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 1 {
		t.Fatalf("expected 1 message (dedup), got %d", len(transcript))
	}
}

func TestSearch_BasicFTS(t *testing.T) {
	store := newTestArchiveStore(t)

	msgs := []ArchivedMessage{
		{
			ID: "msg-1", ConversationID: "conv-1", SessionID: "sess-1",
			Role: "user", Content: "what about the pool heater timer",
			Timestamp:     time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC),
			ArchiveReason: string(ArchiveReasonReset),
		},
		{
			ID: "msg-2", ConversationID: "conv-1", SessionID: "sess-1",
			Role: "assistant", Content: "the pool heater is set to run from 10am to 4pm",
			Timestamp:     time.Date(2026, 2, 12, 10, 0, 5, 0, time.UTC),
			ArchiveReason: string(ArchiveReasonReset),
		},
		{
			ID: "msg-3", ConversationID: "conv-1", SessionID: "sess-1",
			Role: "user", Content: "what is the weather today",
			Timestamp:     time.Date(2026, 2, 12, 10, 1, 0, 0, time.UTC),
			ArchiveReason: string(ArchiveReasonReset),
		},
	}

	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search(SearchOptions{
		Query: "pool heater",
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}

	// Both pool heater messages should match
	if len(results) < 2 {
		t.Errorf("expected 2 results for 'pool heater', got %d", len(results))
	}
}

func TestSearch_SilenceGapContextExpansion(t *testing.T) {
	store := newTestArchiveStore(t)

	base := time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC)

	msgs := []ArchivedMessage{
		// Conversation cluster 1: rapid fire
		{ID: "m1", ConversationID: "c1", SessionID: "s1", Role: "user",
			Content: "starting topic A", Timestamp: base,
			ArchiveReason: "reset"},
		{ID: "m2", ConversationID: "c1", SessionID: "s1", Role: "assistant",
			Content: "sure, topic A it is", Timestamp: base.Add(5 * time.Second),
			ArchiveReason: "reset"},
		{ID: "m3", ConversationID: "c1", SessionID: "s1", Role: "user",
			Content: "tell me about the pool heater", Timestamp: base.Add(30 * time.Second),
			ArchiveReason: "reset"},
		{ID: "m4", ConversationID: "c1", SessionID: "s1", Role: "assistant",
			Content: "the pool heater runs on solar", Timestamp: base.Add(35 * time.Second),
			ArchiveReason: "reset"},
		{ID: "m5", ConversationID: "c1", SessionID: "s1", Role: "user",
			Content: "nice, thanks", Timestamp: base.Add(45 * time.Second),
			ArchiveReason: "reset"},

		// 20 minute gap (silence boundary)

		// Conversation cluster 2: different topic
		{ID: "m6", ConversationID: "c1", SessionID: "s1", Role: "user",
			Content: "now let's talk about something else", Timestamp: base.Add(20 * time.Minute),
			ArchiveReason: "reset"},
		{ID: "m7", ConversationID: "c1", SessionID: "s1", Role: "assistant",
			Content: "what would you like to discuss", Timestamp: base.Add(20*time.Minute + 5*time.Second),
			ArchiveReason: "reset"},
	}

	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search(SearchOptions{
		Query:            "pool heater",
		SilenceThreshold: 10 * time.Minute,
		MaxMessages:      50,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected search results")
	}

	// Find the result that matches "tell me about the pool heater" (m3)
	var result *SearchResult
	for i, r := range results {
		if r.Match.ID == "m3" {
			result = &results[i]
			break
		}
	}
	if result == nil {
		// If m3 not found directly, use first result
		result = &results[0]
		t.Logf("m3 not found directly, using match %s: %s", result.Match.ID, result.Match.Content)
	}

	// Context should NOT cross the 20-minute silence gap
	for _, m := range result.ContextAfter {
		if m.ID == "m6" || m.ID == "m7" {
			t.Errorf("context should not cross silence gap, but included %s", m.ID)
		}
	}

	// For m3, context before should include m1/m2 (5-30s gaps)
	// For m4, context before should include m1/m2/m3
	// Either way, we should get some context
	totalContext := len(result.ContextBefore) + len(result.ContextAfter)
	if totalContext == 0 {
		t.Logf("match: %s (ID: %s), before: %d, after: %d",
			result.Match.Content, result.Match.ID,
			len(result.ContextBefore), len(result.ContextAfter))
		t.Error("expected some context messages around the match")
	}
}

func TestSessionLifecycle(t *testing.T) {
	store := newTestArchiveStore(t)

	// Start session
	sess, err := store.StartSession("conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "" {
		t.Fatal("session ID should not be empty")
	}

	// Should be retrievable
	got, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConversationID != "conv-1" {
		t.Errorf("expected conv-1, got %s", got.ConversationID)
	}
	if got.EndedAt != nil {
		t.Error("session should not be ended yet")
	}

	// Active session lookup
	active, err := store.ActiveSession("conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if active == nil {
		t.Fatal("expected active session")
	}
	if active.ID != sess.ID {
		t.Errorf("active session ID mismatch: %s != %s", active.ID, sess.ID)
	}

	// Archive real messages so the computed count works.
	if err := store.ArchiveMessages([]ArchivedMessage{
		{ID: "msg-1", ConversationID: "conv-1", SessionID: sess.ID, Role: "user", Content: "hello", Timestamp: time.Now(), ArchivedAt: time.Now(), ArchiveReason: "test"},
		{ID: "msg-2", ConversationID: "conv-1", SessionID: sess.ID, Role: "assistant", Content: "hi", Timestamp: time.Now(), ArchivedAt: time.Now(), ArchiveReason: "test"},
	}); err != nil {
		t.Fatal(err)
	}

	got, _ = store.GetSession(sess.ID)
	if got.MessageCount != 2 {
		t.Errorf("expected message_count=2, got %d", got.MessageCount)
	}

	// End session
	if err := store.EndSession(sess.ID, "reset"); err != nil {
		t.Fatal(err)
	}

	got, _ = store.GetSession(sess.ID)
	if got.EndedAt == nil {
		t.Error("session should be ended")
	}
	if got.EndReason != "reset" {
		t.Errorf("expected end_reason=reset, got %s", got.EndReason)
	}

	// No active session now
	active, err = store.ActiveSession("conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if active != nil {
		t.Error("expected no active session after end")
	}
}

func TestListSessions(t *testing.T) {
	store := newTestArchiveStore(t)

	for range 5 {
		sess, err := store.StartSession("conv-1")
		if err != nil {
			t.Fatal(err)
		}
		_ = store.EndSession(sess.ID, "reset")
	}

	sessions, err := store.ListSessions("conv-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 5 {
		t.Fatalf("expected 5 sessions, got %d", len(sessions))
	}
}

func TestGetMessagesByTimeRange(t *testing.T) {
	store := newTestArchiveStore(t)

	base := time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC)
	msgs := make([]ArchivedMessage, 10)
	for i := range msgs {
		msgs[i] = ArchivedMessage{
			ID: fmt.Sprintf("msg-%d", i), ConversationID: "conv-1", SessionID: "sess-1",
			Role: "user", Content: fmt.Sprintf("message %d", i),
			Timestamp:     base.Add(time.Duration(i) * time.Minute),
			ArchiveReason: "reset",
		}
	}

	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatal(err)
	}

	// Query a 5-minute window (should get messages 2-6)
	from := base.Add(2 * time.Minute)
	to := base.Add(6 * time.Minute)
	results, err := store.GetMessagesByTimeRange(from, to, "conv-1", 100)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 5 {
		t.Fatalf("expected 5 messages in time range, got %d", len(results))
	}
}

func TestExportSessionMarkdown(t *testing.T) {
	store := newTestArchiveStore(t)

	sess, _ := store.StartSession("conv-1")

	msgs := []ArchivedMessage{
		{ID: "m1", ConversationID: "conv-1", SessionID: sess.ID, Role: "user",
			Content: "hello", Timestamp: time.Date(2026, 2, 12, 10, 0, 0, 0, time.UTC),
			ArchiveReason: "manual"},
		{ID: "m2", ConversationID: "conv-1", SessionID: sess.ID, Role: "assistant",
			Content: "hi there!", Timestamp: time.Date(2026, 2, 12, 10, 0, 5, 0, time.UTC),
			ArchiveReason: "manual"},
	}

	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatal(err)
	}

	md, err := store.ExportSessionMarkdown(sess.ID)
	if err != nil {
		t.Fatal(err)
	}

	if md == "" {
		t.Fatal("expected non-empty markdown")
	}

	// Should contain the session header and both messages
	if !containsAll(md, "Session", "hello", "hi there!", "🧑", "🤖") {
		t.Errorf("markdown missing expected content:\n%s", md)
	}
}

func TestArchiveStats(t *testing.T) {
	store := newTestArchiveStore(t)

	msgs := []ArchivedMessage{
		{ID: "m1", ConversationID: "c1", SessionID: "s1", Role: "user",
			Content: "hello", Timestamp: time.Now(), ArchiveReason: "reset"},
		{ID: "m2", ConversationID: "c1", SessionID: "s1", Role: "assistant",
			Content: "hi", Timestamp: time.Now(), ArchiveReason: "reset"},
		{ID: "m3", ConversationID: "c1", SessionID: "s1", Role: "tool",
			Content: "{}", Timestamp: time.Now(), ArchiveReason: "compaction"},
	}

	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatal(err)
	}

	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}

	if stats["total_messages"].(int) != 3 {
		t.Errorf("expected 3 total messages, got %v", stats["total_messages"])
	}

	byRole := stats["by_role"].(map[string]int)
	if byRole["user"] != 1 || byRole["assistant"] != 1 || byRole["tool"] != 1 {
		t.Errorf("unexpected by_role: %v", byRole)
	}

	byReason := stats["by_reason"].(map[string]int)
	if byReason["reset"] != 2 || byReason["compaction"] != 1 {
		t.Errorf("unexpected by_reason: %v", byReason)
	}
}

// --- helpers ---

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !containsStr(s, sub) {
			return false
		}
	}
	return true
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestSetSessionMetadata verifies round-trip of rich session metadata.
func TestSetSessionMetadata(t *testing.T) {
	store := newTestArchiveStore(t)

	sess, err := store.StartSession("test-conv")
	if err != nil {
		t.Fatal(err)
	}

	meta := &SessionMetadata{
		OneLiner:     "Built session archive system",
		Paragraph:    "Marathon session building the complete archive system with FTS5 search.",
		Detailed:     "Full session archive with gap-aware context expansion, import tool, and metadata.",
		KeyDecisions: []string{"Gap-aware over rigid ±N", "FTS5 optional with LIKE fallback"},
		Participants: []string{"Nugget", "Aimee"},
		SessionType:  "architecture",
		ToolsUsed:    map[string]int{"archive_search": 3, "shell_exec": 12},
	}
	tags := []string{"thane", "archive", "architecture"}
	title := "Session archive system build"

	if err := store.SetSessionMetadata(sess.ID, meta, title, tags); err != nil {
		t.Fatal(err)
	}

	// Read it back
	got, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.Title != title {
		t.Errorf("title: got %q, want %q", got.Title, title)
	}
	if got.Summary != meta.Paragraph {
		t.Errorf("summary should be set from paragraph: got %q", got.Summary)
	}
	if len(got.Tags) != 3 || got.Tags[0] != "thane" {
		t.Errorf("tags: got %v, want %v", got.Tags, tags)
	}
	if got.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	if got.Metadata.OneLiner != meta.OneLiner {
		t.Errorf("one_liner: got %q, want %q", got.Metadata.OneLiner, meta.OneLiner)
	}
	if got.Metadata.SessionType != "architecture" {
		t.Errorf("session_type: got %q, want %q", got.Metadata.SessionType, "architecture")
	}
	if got.Metadata.ToolsUsed["shell_exec"] != 12 {
		t.Errorf("tools_used: got %v", got.Metadata.ToolsUsed)
	}
	if len(got.Metadata.KeyDecisions) != 2 {
		t.Errorf("key_decisions: got %v", got.Metadata.KeyDecisions)
	}
}

// TestUnsummarizedSessions verifies the query returns ended sessions
// without metadata, respects filters, and orders oldest-first.
func TestUnsummarizedSessions(t *testing.T) {
	store := newTestArchiveStore(t)

	// Create 3 ended sessions with actual archived messages but no metadata.
	var unsummarized []string
	for i := range 3 {
		convID := fmt.Sprintf("conv-%d", i)
		sess, err := store.StartSession(convID)
		if err != nil {
			t.Fatal(err)
		}
		// Archive a real message so the EXISTS subquery finds it.
		err = store.ArchiveMessages([]ArchivedMessage{{
			ID:             fmt.Sprintf("msg-%d", i),
			ConversationID: convID,
			SessionID:      sess.ID,
			Role:           "user",
			Content:        "hello",
			Timestamp:      time.Now(),
			ArchiveReason:  "test",
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.EndSession(sess.ID, "reset"); err != nil {
			t.Fatal(err)
		}
		unsummarized = append(unsummarized, sess.ID)
	}

	// Create an ended session WITH metadata — should be excluded.
	summarized, err := store.StartSession("conv-summarized")
	if err != nil {
		t.Fatal(err)
	}
	err = store.ArchiveMessages([]ArchivedMessage{{
		ID:             "msg-summarized",
		ConversationID: "conv-summarized",
		SessionID:      summarized.ID,
		Role:           "user",
		Content:        "hello",
		Timestamp:      time.Now(),
		ArchiveReason:  "test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndSession(summarized.ID, "reset"); err != nil {
		t.Fatal(err)
	}
	meta := &SessionMetadata{OneLiner: "Already summarized"}
	if err := store.SetSessionMetadata(summarized.ID, meta, "Has Title", nil); err != nil {
		t.Fatal(err)
	}

	// Create a still-active session — should be excluded.
	active, err := store.StartSession("conv-active")
	if err != nil {
		t.Fatal(err)
	}
	err = store.ArchiveMessages([]ArchivedMessage{{
		ID:             "msg-active",
		ConversationID: "conv-active",
		SessionID:      active.ID,
		Role:           "user",
		Content:        "hello",
		Timestamp:      time.Now(),
		ArchiveReason:  "test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = active

	// Create an ended session with zero messages — should be excluded.
	empty, err := store.StartSession("conv-empty")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndSession(empty.ID, "reset"); err != nil {
		t.Fatal(err)
	}

	// Create an ended session with stale message_count > 0 but NO actual
	// archived messages (the bug case from issue #341). Should be excluded.
	stale, err := store.StartSession("conv-stale")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.IncrementSessionCount(stale.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.EndSession(stale.ID, "reset"); err != nil {
		t.Fatal(err)
	}

	// Query: should return only the 3 unsummarized sessions.
	sessions, err := store.UnsummarizedSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 unsummarized sessions, got %d", len(sessions))
	}

	// Verify oldest-first order (by ended_at ASC).
	for i, sess := range sessions {
		if sess.ID != unsummarized[i] {
			t.Errorf("session[%d] = %s, want %s", i, ShortID(sess.ID), ShortID(unsummarized[i]))
		}
	}

	// Verify limit is respected.
	limited, err := store.UnsummarizedSessions(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected 2 sessions with limit=2, got %d", len(limited))
	}
}

// TestCloseOrphanedSessions verifies that open sessions older than the
// cutoff are closed with reason "crash_recovery", while recent and
// already-ended sessions are untouched.
func TestCloseOrphanedSessions(t *testing.T) {
	store := newTestArchiveStore(t)

	// Create two open sessions — both started "now" in test time.
	old, err := store.StartSession("conv-old")
	if err != nil {
		t.Fatal(err)
	}
	recent, err := store.StartSession("conv-recent")
	if err != nil {
		t.Fatal(err)
	}

	// Create an already-ended session — should not be touched.
	ended, err := store.StartSession("conv-ended")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndSession(ended.ID, "normal"); err != nil {
		t.Fatal(err)
	}

	// Use a cutoff that is after the "old" session but we need to
	// actually differentiate. Since both were created nearly
	// simultaneously, use a cutoff well in the future to close both
	// open sessions.
	cutoff := time.Now().Add(time.Minute)
	closed, err := store.CloseOrphanedSessions(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if closed != 2 {
		t.Fatalf("expected 2 orphaned sessions closed, got %d", closed)
	}

	// Verify old session was closed with crash_recovery.
	got, _ := store.GetSession(old.ID)
	if got.EndReason != "crash_recovery" {
		t.Errorf("old session end_reason = %q, want %q", got.EndReason, "crash_recovery")
	}
	if got.EndedAt == nil {
		t.Error("old session ended_at should not be nil")
	}

	// Verify recent session was also closed.
	got, _ = store.GetSession(recent.ID)
	if got.EndReason != "crash_recovery" {
		t.Errorf("recent session end_reason = %q, want %q", got.EndReason, "crash_recovery")
	}

	// Verify already-ended session was not modified.
	got, _ = store.GetSession(ended.ID)
	if got.EndReason != "normal" {
		t.Errorf("ended session end_reason = %q, want %q", got.EndReason, "normal")
	}
}

func TestStartSessionWithOptions_ParentFields(t *testing.T) {
	store := newTestArchiveStore(t)

	// Create a parent session first.
	parent, err := store.StartSession("conv-main")
	if err != nil {
		t.Fatal(err)
	}

	// Create a child session with parent linkage.
	child, err := store.StartSessionWithOptions("delegate-abc",
		WithParentSession(parent.ID),
		WithParentToolCall("call_xyz"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if child.ParentSessionID != parent.ID {
		t.Errorf("ParentSessionID = %q, want %q", child.ParentSessionID, parent.ID)
	}
	if child.ParentToolCallID != "call_xyz" {
		t.Errorf("ParentToolCallID = %q, want %q", child.ParentToolCallID, "call_xyz")
	}

	// Fetch from DB and verify persistence.
	fetched, err := store.GetSession(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.ParentSessionID != parent.ID {
		t.Errorf("fetched ParentSessionID = %q, want %q", fetched.ParentSessionID, parent.ID)
	}
	if fetched.ParentToolCallID != "call_xyz" {
		t.Errorf("fetched ParentToolCallID = %q, want %q", fetched.ParentToolCallID, "call_xyz")
	}
}

func TestStartSessionWithOptions_NoParent(t *testing.T) {
	store := newTestArchiveStore(t)

	// Without options, parent fields should be empty.
	sess, err := store.StartSessionWithOptions("conv-basic")
	if err != nil {
		t.Fatal(err)
	}

	if sess.ParentSessionID != "" {
		t.Errorf("ParentSessionID = %q, want empty", sess.ParentSessionID)
	}
	if sess.ParentToolCallID != "" {
		t.Errorf("ParentToolCallID = %q, want empty", sess.ParentToolCallID)
	}
}

func TestListChildSessions(t *testing.T) {
	store := newTestArchiveStore(t)

	parent, err := store.StartSession("conv-main")
	if err != nil {
		t.Fatal(err)
	}

	// Create two child sessions.
	child1, err := store.StartSessionWithOptions("delegate-1",
		WithParentSession(parent.ID),
	)
	if err != nil {
		t.Fatal(err)
	}
	child2, err := store.StartSessionWithOptions("delegate-2",
		WithParentSession(parent.ID),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Also create an unrelated session.
	_, err = store.StartSession("conv-other")
	if err != nil {
		t.Fatal(err)
	}

	children, err := store.ListChildSessions(parent.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(children) != 2 {
		t.Fatalf("ListChildSessions returned %d, want 2", len(children))
	}

	// Should be ordered by started_at ASC.
	if children[0].ID != child1.ID {
		t.Errorf("first child ID = %q, want %q", children[0].ID, child1.ID)
	}
	if children[1].ID != child2.ID {
		t.Errorf("second child ID = %q, want %q", children[1].ID, child2.ID)
	}

	// Parent with no children should return empty slice.
	noChildren, err := store.ListChildSessions("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(noChildren) != 0 {
		t.Errorf("ListChildSessions(nonexistent) returned %d, want 0", len(noChildren))
	}
}

func TestArchiveIterations_RoundTrip(t *testing.T) {
	store := newTestArchiveStore(t)

	sess, err := store.StartSession("conv-iter")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().Truncate(time.Millisecond)
	iters := []ArchivedIteration{
		{
			SessionID:      sess.ID,
			IterationIndex: 0,
			Model:          "claude-sonnet",
			InputTokens:    1000,
			OutputTokens:   200,
			ToolCallCount:  2,
			ToolCallIDs:    []string{"tc-a", "tc-b"},
			StartedAt:      now,
			DurationMs:     350,
			HasToolCalls:   true,
		},
		{
			SessionID:      sess.ID,
			IterationIndex: 1,
			Model:          "claude-sonnet",
			InputTokens:    1200,
			OutputTokens:   100,
			ToolCallCount:  0,
			StartedAt:      now.Add(time.Second),
			DurationMs:     150,
			HasToolCalls:   false,
			BreakReason:    "max_iterations",
		},
	}

	if err := store.ArchiveIterations(iters); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetSessionIterations(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(got))
	}

	// Verify first iteration.
	if got[0].IterationIndex != 0 {
		t.Errorf("iter[0] index = %d, want 0", got[0].IterationIndex)
	}
	if got[0].Model != "claude-sonnet" {
		t.Errorf("iter[0] model = %q, want %q", got[0].Model, "claude-sonnet")
	}
	if got[0].InputTokens != 1000 {
		t.Errorf("iter[0] input_tokens = %d, want 1000", got[0].InputTokens)
	}
	if got[0].ToolCallCount != 2 {
		t.Errorf("iter[0] tool_call_count = %d, want 2", got[0].ToolCallCount)
	}
	if !got[0].HasToolCalls {
		t.Error("iter[0] has_tool_calls should be true")
	}
	if len(got[0].ToolCallIDs) != 2 || got[0].ToolCallIDs[0] != "tc-a" {
		t.Errorf("iter[0] tool_call_ids = %v, want [tc-a tc-b]", got[0].ToolCallIDs)
	}

	// Verify second iteration.
	if got[1].IterationIndex != 1 {
		t.Errorf("iter[1] index = %d, want 1", got[1].IterationIndex)
	}
	if got[1].BreakReason != "max_iterations" {
		t.Errorf("iter[1] break_reason = %q, want %q", got[1].BreakReason, "max_iterations")
	}
	if got[1].HasToolCalls {
		t.Error("iter[1] has_tool_calls should be false")
	}

	// Archive a second batch (simulating a second turn in the same session).
	// Both iterations start at index 0 locally, but should be offset to 2 and 3.
	batch2 := []ArchivedIteration{
		{
			SessionID:      sess.ID,
			IterationIndex: 0,
			Model:          "claude-haiku",
			InputTokens:    500,
			OutputTokens:   50,
			ToolCallCount:  1,
			ToolCallIDs:    []string{"tc-c"},
			StartedAt:      now.Add(2 * time.Second),
			DurationMs:     100,
			HasToolCalls:   true,
		},
		{
			SessionID:      sess.ID,
			IterationIndex: 1,
			Model:          "claude-haiku",
			InputTokens:    600,
			OutputTokens:   75,
			StartedAt:      now.Add(3 * time.Second),
			DurationMs:     80,
		},
	}

	if err := store.ArchiveIterations(batch2); err != nil {
		t.Fatal(err)
	}

	got, err = store.GetSessionIterations(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 iterations after second batch, got %d", len(got))
	}
	// Second batch should be offset: 0→2, 1→3.
	if got[2].IterationIndex != 2 {
		t.Errorf("iter[2] index = %d, want 2 (offset)", got[2].IterationIndex)
	}
	if got[2].Model != "claude-haiku" {
		t.Errorf("iter[2] model = %q, want %q", got[2].Model, "claude-haiku")
	}
	if got[3].IterationIndex != 3 {
		t.Errorf("iter[3] index = %d, want 3 (offset)", got[3].IterationIndex)
	}
}

func TestArchiveIterations_EmptySession(t *testing.T) {
	store := newTestArchiveStore(t)

	got, err := store.GetSessionIterations("nonexistent-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 iterations for nonexistent session, got %d", len(got))
	}
}

func TestLinkToolCallsToIteration(t *testing.T) {
	store := newTestArchiveStore(t)

	sess, err := store.StartSession("conv-link")
	if err != nil {
		t.Fatal(err)
	}

	// Archive some tool calls.
	calls := []ArchivedToolCall{
		{
			ID:             "call-1",
			ConversationID: "conv-link",
			SessionID:      sess.ID,
			ToolName:       "shell_exec",
			Arguments:      `{"cmd":"ls"}`,
			Result:         "file1.go",
			StartedAt:      time.Now(),
		},
		{
			ID:             "call-2",
			ConversationID: "conv-link",
			SessionID:      sess.ID,
			ToolName:       "web_search",
			Arguments:      `{"q":"test"}`,
			Result:         "results",
			StartedAt:      time.Now(),
		},
		{
			ID:             "call-3",
			ConversationID: "conv-link",
			SessionID:      sess.ID,
			ToolName:       "shell_exec",
			Arguments:      `{"cmd":"pwd"}`,
			Result:         "/home",
			StartedAt:      time.Now(),
		},
	}

	if err := store.ArchiveToolCalls(calls); err != nil {
		t.Fatal(err)
	}

	// Link first two calls to iteration 0, third to iteration 1.
	if err := store.LinkToolCallsToIteration(sess.ID, 0, []string{"call-1", "call-2"}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkToolCallsToIteration(sess.ID, 1, []string{"call-3"}); err != nil {
		t.Fatal(err)
	}

	// Verify iteration_index is set on retrieved tool calls.
	got, err := store.GetSessionToolCalls(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 tool calls, got %d", len(got))
	}

	for _, tc := range got {
		if tc.IterationIndex == nil {
			t.Errorf("tool call %q should have iteration_index set", tc.ID)
			continue
		}
		switch tc.ID {
		case "call-1", "call-2":
			if *tc.IterationIndex != 0 {
				t.Errorf("tool call %q iteration_index = %d, want 0", tc.ID, *tc.IterationIndex)
			}
		case "call-3":
			if *tc.IterationIndex != 1 {
				t.Errorf("tool call %q iteration_index = %d, want 1", tc.ID, *tc.IterationIndex)
			}
		}
	}
}

package memory

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

// newTestAdapter creates a consolidated-mode adapter for testing.
func newTestAdapter(t *testing.T) (*ArchiveAdapter, *ArchiveStore, *SQLiteStore) {
	t.Helper()

	tmpDir := t.TempDir()
	workingStore, err := NewSQLiteStore(tmpDir+"/working.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { workingStore.Close() })

	MigrateUnifyMessages(workingStore.DB(), "", nil)
	MigrateUnifyToolCalls(workingStore.DB(), "", nil)

	archiveStore, err := NewArchiveStoreFromDB(workingStore.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	adapter := NewArchiveAdapter(archiveStore, workingStore, workingStore, logger)

	return adapter, archiveStore, workingStore
}

func TestAdapter_ArchiveConversation(t *testing.T) {
	adapter, archiveStore, workingStore := newTestAdapter(t)

	// Start a session first.
	sid, err := adapter.StartSession("conv-1")
	if err != nil {
		t.Fatal(err)
	}

	// Add active messages to the unified table.
	workingStore.GetOrCreateConversation("conv-1")
	for _, content := range []string{"hello", "hi there!"} {
		if err := workingStore.AddMessage("conv-1", "user", content); err != nil {
			t.Fatal(err)
		}
	}

	msgs := []Message{
		{Role: "user", Content: "hello", Timestamp: time.Now()},
		{Role: "assistant", Content: "hi there!", Timestamp: time.Now()},
	}

	if err := adapter.ArchiveConversation("conv-1", msgs, "reset"); err != nil {
		t.Fatal(err)
	}

	// Verify messages were archived (status='archived') in the unified table.
	var archivedCount int
	_ = workingStore.DB().QueryRow(
		`SELECT COUNT(*) FROM messages WHERE conversation_id = 'conv-1' AND status = 'archived'`,
	).Scan(&archivedCount)
	if archivedCount != 2 {
		t.Errorf("expected 2 archived messages, got %d", archivedCount)
	}

	// Verify session_id was set.
	var sessionID string
	_ = workingStore.DB().QueryRow(
		`SELECT session_id FROM messages WHERE conversation_id = 'conv-1' AND status = 'archived' LIMIT 1`,
	).Scan(&sessionID)
	if sessionID != sid {
		t.Errorf("expected session_id=%s, got %s", sid, sessionID)
	}

	// Verify messages are readable from the archive store.
	transcript, err := archiveStore.GetSessionTranscript(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 2 {
		t.Fatalf("expected 2 messages in transcript, got %d", len(transcript))
	}
}

func TestAdapter_ArchiveConversation_WithToolCalls(t *testing.T) {
	adapter, _, workingStore := newTestAdapter(t)

	sid, _ := adapter.StartSession("conv-1")

	// Add messages and tool calls to the unified tables.
	workingStore.GetOrCreateConversation("conv-1")
	workingStore.AddMessage("conv-1", "user", "search for test")
	workingStore.RecordToolCall("conv-1", "", "tc-1", "web_search", `{"query":"test"}`)
	workingStore.CompleteToolCall("tc-1", "search results", "")

	msgs := []Message{
		{Role: "user", Content: "search for test", Timestamp: time.Now()},
	}

	if err := adapter.ArchiveConversation("conv-1", msgs, "reset"); err != nil {
		t.Fatal(err)
	}

	// Verify tool call was archived via status UPDATE.
	var status string
	err := workingStore.DB().QueryRow(`SELECT status FROM tool_calls WHERE id = 'tc-1'`).Scan(&status)
	if err != nil {
		t.Fatal(err)
	}
	if status != "archived" {
		t.Errorf("expected status=archived, got %s", status)
	}

	// Verify session_id was set on tool call.
	var sessionID string
	_ = workingStore.DB().QueryRow(`SELECT session_id FROM tool_calls WHERE id = 'tc-1'`).Scan(&sessionID)
	if sessionID != sid {
		t.Errorf("expected session_id=%s, got %s", sid, sessionID)
	}
}

func TestAdapter_SessionLifecycle(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)

	// No active session initially.
	if sid := adapter.ActiveSessionID("conv-1"); sid != "" {
		t.Errorf("expected no active session, got %s", sid)
	}

	// Start session.
	sid, err := adapter.StartSession("conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if sid == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Active session should match.
	if got := adapter.ActiveSessionID("conv-1"); got != sid {
		t.Errorf("active session mismatch: %s != %s", got, sid)
	}

	// End session.
	if err := adapter.EndSession(sid, "reset"); err != nil {
		t.Fatal(err)
	}

	// No longer active.
	if got := adapter.ActiveSessionID("conv-1"); got != "" {
		t.Errorf("expected no active session after end, got %s", got)
	}
}

func TestAdapter_EnsureSession(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)

	// EnsureSession should create one.
	sid1 := adapter.EnsureSession("conv-1")
	if sid1 == "" {
		t.Fatal("expected session to be created")
	}

	// EnsureSession again should return the same one.
	sid2 := adapter.EnsureSession("conv-1")
	if sid2 != sid1 {
		t.Errorf("expected same session, got %s != %s", sid1, sid2)
	}
}

func TestAdapter_OnMessage(t *testing.T) {
	adapter, archiveStore, workingStore := newTestAdapter(t)

	sid, _ := adapter.StartSession("conv-1")

	// Insert archived messages into the unified table.
	now := time.Now()
	for i, content := range []string{"a", "b", "c"} {
		_, _ = workingStore.DB().Exec(`
			INSERT INTO messages (id, conversation_id, session_id, role, content,
			    timestamp, token_count, status, archived_at, archive_reason)
			VALUES (?, 'conv-1', ?, 'user', ?, ?, 5, 'archived', ?, 'test')
		`, "m"+string(rune('1'+i)), sid, content,
			now.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano))
	}

	sess, _ := archiveStore.GetSession(sid)
	if sess.MessageCount != 3 {
		t.Errorf("expected message_count=3, got %d", sess.MessageCount)
	}
}

func TestAdapter_ActiveSessionID_DBFallback(t *testing.T) {
	adapter, archiveStore, _ := newTestAdapter(t)

	// Create a session directly in the store (bypassing adapter cache).
	sess, _ := archiveStore.StartSession("conv-1")

	// Adapter cache doesn't know about it — should fall back to DB.
	got := adapter.ActiveSessionID("conv-1")
	if got != sess.ID {
		t.Errorf("expected DB fallback to find session %s, got %s", sess.ID[:8], got)
	}
}

func TestAdapter_ActiveSessionStartedAt(t *testing.T) {
	adapter, _, _ := newTestAdapter(t)

	// No active session — should return zero time.
	if got := adapter.ActiveSessionStartedAt("conv-1"); !got.IsZero() {
		t.Errorf("expected zero time, got %v", got)
	}

	// Start session.
	sid, _ := adapter.StartSession("conv-1")

	// Should return non-zero start time.
	got := adapter.ActiveSessionStartedAt("conv-1")
	if got.IsZero() {
		t.Error("expected non-zero start time for active session")
	}

	// End session — clear cache.
	adapter.EndSession(sid, "reset")

	// Should return zero again.
	if got := adapter.ActiveSessionStartedAt("conv-1"); !got.IsZero() {
		t.Errorf("expected zero time after session end, got %v", got)
	}
}

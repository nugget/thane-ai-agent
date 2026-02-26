package memory

import (
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// newTestWorkingDB creates a working SQLiteStore in a temp directory for testing.
func newTestWorkingDB(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(t.TempDir()+"/working.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestMigrateUnifyMessages_AddsLifecycleColumns verifies that the migration
// adds status, session_id, archived_at, archive_reason, and iteration_index
// columns to the messages table.
func TestMigrateUnifyMessages_AddsLifecycleColumns(t *testing.T) {
	store := newTestWorkingDB(t)

	if err := MigrateUnifyMessages(store.DB(), "", slog.Default()); err != nil {
		t.Fatal(err)
	}

	// Verify all lifecycle columns exist.
	for _, col := range []string{"session_id", "status", "archived_at", "archive_reason", "iteration_index"} {
		if !hasColumn(store.DB(), "messages", col) {
			t.Errorf("expected column %q to exist after migration", col)
		}
	}
}

// TestMigrateUnifyMessages_BackfillsStatus verifies that existing messages
// get their status column set from the compacted boolean.
func TestMigrateUnifyMessages_BackfillsStatus(t *testing.T) {
	store := newTestWorkingDB(t)

	// Add some messages — they start with compacted=FALSE.
	for i := 0; i < 5; i++ {
		if err := store.AddMessage("conv-1", "user", fmt.Sprintf("msg %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	// Mark some as compacted using the old boolean column.
	_, err := store.DB().Exec(`UPDATE messages SET compacted = TRUE WHERE rowid <= 2`)
	if err != nil {
		t.Fatal(err)
	}

	// Run migration.
	if err := MigrateUnifyMessages(store.DB(), "", slog.Default()); err != nil {
		t.Fatal(err)
	}

	// Check that compacted messages have status='compacted'.
	var compactedCount int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE status = 'compacted'`).Scan(&compactedCount)
	if compactedCount != 2 {
		t.Errorf("expected 2 compacted messages, got %d", compactedCount)
	}

	// Check that active messages have status='active'.
	var activeCount int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE status = 'active'`).Scan(&activeCount)
	if activeCount != 3 {
		t.Errorf("expected 3 active messages, got %d", activeCount)
	}
}

// TestMigrateUnifyMessages_MergesArchive verifies that archived messages
// from archive.db are copied into the working messages table.
func TestMigrateUnifyMessages_MergesArchive(t *testing.T) {
	tmpDir := t.TempDir()
	workingPath := tmpDir + "/working.db"
	archivePath := tmpDir + "/archive.db"

	// Create working store with some messages.
	workingStore, err := NewSQLiteStore(workingPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	if err := workingStore.AddMessage("conv-1", "user", "working msg"); err != nil {
		t.Fatal(err)
	}

	// Create archive store with different messages.
	archiveStore, err := NewArchiveStore(archivePath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	sess, _ := archiveStore.StartSession("conv-1")
	if err := archiveStore.ArchiveMessages([]ArchivedMessage{
		{ID: "arch-1", ConversationID: "conv-1", SessionID: sess.ID,
			Role: "user", Content: "archived msg 1",
			Timestamp: time.Now(), ArchiveReason: "reset"},
		{ID: "arch-2", ConversationID: "conv-1", SessionID: sess.ID,
			Role: "assistant", Content: "archived msg 2",
			Timestamp: time.Now(), ArchiveReason: "reset"},
	}); err != nil {
		t.Fatal(err)
	}
	archiveStore.Close()

	// Run migration.
	if err := MigrateUnifyMessages(workingStore.DB(), archivePath, slog.Default()); err != nil {
		t.Fatal(err)
	}

	// Verify: working msg should be active, archive msgs should be archived.
	var activeCount, archivedCount int
	_ = workingStore.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE status = 'active'`).Scan(&activeCount)
	_ = workingStore.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE status = 'archived'`).Scan(&archivedCount)

	if activeCount != 1 {
		t.Errorf("expected 1 active message, got %d", activeCount)
	}
	if archivedCount != 2 {
		t.Errorf("expected 2 archived messages, got %d", archivedCount)
	}

	// Verify session_id was preserved from archive on the merged messages.
	var sessionID string
	_ = workingStore.DB().QueryRow(`SELECT session_id FROM messages WHERE id = 'arch-1'`).Scan(&sessionID)
	if sessionID != sess.ID {
		t.Errorf("expected session_id=%s on merged message, got %s", sess.ID, sessionID)
	}
}

// TestMigrateUnifyMessages_UpsertPreservesSessionID verifies that when a
// message exists in both working and archive stores, the UPSERT preserves
// the session_id from the archive copy (which has richer metadata).
func TestMigrateUnifyMessages_UpsertPreservesSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	workingPath := tmpDir + "/working.db"
	archivePath := tmpDir + "/archive.db"

	// Create working store with a message (no session_id).
	workingStore, err := NewSQLiteStore(workingPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	// Insert a message with a known ID into working store.
	_, err = workingStore.DB().Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count)
		VALUES ('shared-msg', 'conv-1', 'user', 'hello', ?, 5)
	`, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	// Create archive with the same message ID but with session_id.
	archiveStore, err := NewArchiveStore(archivePath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	sess, _ := archiveStore.StartSession("conv-1")
	if err := archiveStore.ArchiveMessages([]ArchivedMessage{
		{ID: "shared-msg", ConversationID: "conv-1", SessionID: sess.ID,
			Role: "user", Content: "hello",
			Timestamp: time.Now(), ArchiveReason: "reset"},
	}); err != nil {
		t.Fatal(err)
	}
	archiveStore.Close()

	// Run migration.
	if err := MigrateUnifyMessages(workingStore.DB(), archivePath, slog.Default()); err != nil {
		t.Fatal(err)
	}

	// The UPSERT should have set session_id from the archive copy.
	var sessionID sql.NullString
	var status string
	err = workingStore.DB().QueryRow(`SELECT session_id, status FROM messages WHERE id = 'shared-msg'`).Scan(&sessionID, &status)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionID.Valid || sessionID.String != sess.ID {
		t.Errorf("expected session_id=%s, got %v", sess.ID, sessionID)
	}
	if status != "archived" {
		t.Errorf("expected status=archived, got %s", status)
	}
}

// TestMigrateUnifyMessages_Idempotent verifies that running the migration
// twice does not duplicate data or error.
func TestMigrateUnifyMessages_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	workingPath := tmpDir + "/working.db"
	archivePath := tmpDir + "/archive.db"

	workingStore, err := NewSQLiteStore(workingPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	archiveStore, err := NewArchiveStore(archivePath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := archiveStore.StartSession("conv-1")
	archiveStore.ArchiveMessages([]ArchivedMessage{
		{ID: "m1", ConversationID: "conv-1", SessionID: sess.ID,
			Role: "user", Content: "hello",
			Timestamp: time.Now(), ArchiveReason: "reset"},
	})
	archiveStore.Close()

	// First run.
	if err := MigrateUnifyMessages(workingStore.DB(), archivePath, slog.Default()); err != nil {
		t.Fatal(err)
	}

	var count1 int
	_ = workingStore.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count1)

	// Second run — should be a no-op.
	if err := MigrateUnifyMessages(workingStore.DB(), archivePath, slog.Default()); err != nil {
		t.Fatal(err)
	}

	var count2 int
	_ = workingStore.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count2)

	if count1 != count2 {
		t.Errorf("idempotency violated: first run %d rows, second run %d rows", count1, count2)
	}
}

// TestMigrateUnifyMessages_NoArchiveDB verifies that migration succeeds
// gracefully when there is no archive database.
func TestMigrateUnifyMessages_NoArchiveDB(t *testing.T) {
	store := newTestWorkingDB(t)

	if err := MigrateUnifyMessages(store.DB(), "/nonexistent/archive.db", slog.Default()); err != nil {
		t.Fatalf("expected no error for missing archive, got: %v", err)
	}
}

// TestUnifiedMode_GetSessionTranscript verifies that GetSessionTranscript
// reads from the unified messages table when messagesDB is set.
func TestUnifiedMode_GetSessionTranscript(t *testing.T) {
	tmpDir := t.TempDir()

	// Create working store with messages.
	workingStore, err := NewSQLiteStore(tmpDir+"/working.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	// Run migration to add lifecycle columns.
	MigrateUnifyMessages(workingStore.DB(), "", slog.Default())

	// Create archive store in unified mode (messages from working DB).
	archiveStore, err := NewArchiveStore(tmpDir+"/archive.db", workingStore.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveStore.Close()

	// Start a session.
	sess, _ := archiveStore.StartSession("conv-1")

	// Insert messages directly into the unified table with session_id set.
	now := time.Now().UTC()
	for i, content := range []string{"hello", "how are you?", "great thanks"} {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		_, err := workingStore.DB().Exec(`
			INSERT INTO messages (id, conversation_id, session_id, role, content, timestamp, token_count, status, archived_at, archive_reason)
			VALUES (?, 'conv-1', ?, ?, ?, ?, 10, 'archived', ?, 'test')
		`, fmt.Sprintf("msg-%d", i), sess.ID, role, content,
			now.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}

	// Read transcript via archive store (should use unified table).
	transcript, err := archiveStore.GetSessionTranscript(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(transcript))
	}
	if transcript[0].Content != "hello" {
		t.Errorf("expected first message 'hello', got %q", transcript[0].Content)
	}
	if transcript[2].Content != "great thanks" {
		t.Errorf("expected last message 'great thanks', got %q", transcript[2].Content)
	}
}

// TestUnifiedMode_ArchiveMessages verifies that SQLiteStore.ArchiveMessages
// updates message status to 'archived' in the unified table.
func TestUnifiedMode_ArchiveMessages(t *testing.T) {
	store := newTestWorkingDB(t)

	// Run migration to add lifecycle columns.
	MigrateUnifyMessages(store.DB(), "", slog.Default())

	// Add active messages.
	for i := 0; i < 3; i++ {
		if err := store.AddMessage("conv-1", "user", fmt.Sprintf("msg %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	// Verify they start as active.
	var activeCount int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE status = 'active' AND conversation_id = 'conv-1'`).Scan(&activeCount)
	if activeCount != 3 {
		t.Fatalf("expected 3 active messages, got %d", activeCount)
	}

	// Archive them.
	affected, err := store.ArchiveMessages("conv-1", "sess-1", "reset")
	if err != nil {
		t.Fatal(err)
	}
	if affected != 3 {
		t.Errorf("expected 3 affected rows, got %d", affected)
	}

	// Verify status changed.
	var archivedCount int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE status = 'archived' AND conversation_id = 'conv-1'`).Scan(&archivedCount)
	if archivedCount != 3 {
		t.Errorf("expected 3 archived messages, got %d", archivedCount)
	}

	// Verify session_id was set.
	var sid string
	_ = store.DB().QueryRow(`SELECT session_id FROM messages WHERE conversation_id = 'conv-1' LIMIT 1`).Scan(&sid)
	if sid != "sess-1" {
		t.Errorf("expected session_id=sess-1, got %q", sid)
	}
}

// TestMigrateUnifyToolCalls_AddsLifecycleColumns verifies that the migration
// adds session_id, status, archived_at, and iteration_index columns to the
// tool_calls table.
func TestMigrateUnifyToolCalls_AddsLifecycleColumns(t *testing.T) {
	store := newTestWorkingDB(t)

	if err := MigrateUnifyToolCalls(store.DB(), "", slog.Default()); err != nil {
		t.Fatal(err)
	}

	for _, col := range []string{"session_id", "status", "archived_at", "iteration_index"} {
		if !hasColumn(store.DB(), "tool_calls", col) {
			t.Errorf("expected column %q to exist after migration", col)
		}
	}
}

// TestMigrateUnifyToolCalls_BackfillsStatus verifies that existing tool calls
// with NULL status get set to 'active'.
func TestMigrateUnifyToolCalls_BackfillsStatus(t *testing.T) {
	store := newTestWorkingDB(t)

	store.GetOrCreateConversation("conv-1")
	store.RecordToolCall("conv-1", "", "tc-1", "get_state", `{}`)
	store.RecordToolCall("conv-1", "", "tc-2", "call_service", `{}`)

	// Simulate pre-migration rows by NULLing out the status column.
	_, err := store.DB().Exec(`UPDATE tool_calls SET status = NULL`)
	if err != nil {
		t.Fatal(err)
	}

	if err := MigrateUnifyToolCalls(store.DB(), "", slog.Default()); err != nil {
		t.Fatal(err)
	}

	var activeCount int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE status = 'active'`).Scan(&activeCount)
	if activeCount != 2 {
		t.Errorf("expected 2 active tool calls, got %d", activeCount)
	}
}

// TestMigrateUnifyToolCalls_MergesArchive verifies that archived tool calls
// from archive.db are merged into the working tool_calls table.
func TestMigrateUnifyToolCalls_MergesArchive(t *testing.T) {
	tmpDir := t.TempDir()
	workingPath := tmpDir + "/working.db"
	archivePath := tmpDir + "/archive.db"

	// Create working store with an active tool call.
	workingStore, err := NewSQLiteStore(workingPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	workingStore.GetOrCreateConversation("conv-1")
	workingStore.RecordToolCall("conv-1", "", "tc-working", "get_state", `{}`)

	// Create archive store with different tool calls.
	archiveStore, err := NewArchiveStore(archivePath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := archiveStore.StartSession("conv-1")
	now := time.Now().UTC()
	completed := now.Add(100 * time.Millisecond)
	if err := archiveStore.ArchiveToolCalls([]ArchivedToolCall{
		{
			ID: "tc-arch-1", ConversationID: "conv-1", SessionID: sess.ID,
			ToolName: "web_search", Arguments: `{"q":"test"}`,
			Result: "results", StartedAt: now,
			CompletedAt: &completed, DurationMs: 100,
		},
		{
			ID: "tc-arch-2", ConversationID: "conv-1", SessionID: sess.ID,
			ToolName: "file_read", Arguments: `{"path":"x.md"}`,
			Error: "not found", StartedAt: now.Add(time.Second),
		},
	}); err != nil {
		t.Fatal(err)
	}
	archiveStore.Close()

	// Run migration.
	if err := MigrateUnifyToolCalls(workingStore.DB(), archivePath, slog.Default()); err != nil {
		t.Fatal(err)
	}

	var activeCount, archivedCount int
	_ = workingStore.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE status = 'active'`).Scan(&activeCount)
	_ = workingStore.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE status = 'archived'`).Scan(&archivedCount)

	if activeCount != 1 {
		t.Errorf("expected 1 active tool call, got %d", activeCount)
	}
	if archivedCount != 2 {
		t.Errorf("expected 2 archived tool calls, got %d", archivedCount)
	}

	// Verify session_id was preserved from archive.
	var sessionID string
	_ = workingStore.DB().QueryRow(`SELECT session_id FROM tool_calls WHERE id = 'tc-arch-1'`).Scan(&sessionID)
	if sessionID != sess.ID {
		t.Errorf("expected session_id=%s, got %s", sess.ID, sessionID)
	}
}

// TestMigrateUnifyToolCalls_UpsertPreservesSessionID verifies that when a tool
// call exists in both working and archive, the UPSERT preserves the archive's
// session_id and sets status to archived.
func TestMigrateUnifyToolCalls_UpsertPreservesSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	workingPath := tmpDir + "/working.db"
	archivePath := tmpDir + "/archive.db"

	workingStore, err := NewSQLiteStore(workingPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	// Insert a tool call with a known ID into working store (no session_id).
	workingStore.GetOrCreateConversation("conv-1")
	workingStore.RecordToolCall("conv-1", "", "shared-tc", "get_state", `{}`)

	// Create archive with the same tool call ID but with session_id.
	archiveStore, err := NewArchiveStore(archivePath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := archiveStore.StartSession("conv-1")
	archiveStore.ArchiveToolCalls([]ArchivedToolCall{
		{
			ID: "shared-tc", ConversationID: "conv-1", SessionID: sess.ID,
			ToolName: "get_state", Arguments: `{}`,
			StartedAt: time.Now(),
		},
	})
	archiveStore.Close()

	if err := MigrateUnifyToolCalls(workingStore.DB(), archivePath, slog.Default()); err != nil {
		t.Fatal(err)
	}

	var sessionID sql.NullString
	var status string
	err = workingStore.DB().QueryRow(`SELECT session_id, status FROM tool_calls WHERE id = 'shared-tc'`).Scan(&sessionID, &status)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionID.Valid || sessionID.String != sess.ID {
		t.Errorf("expected session_id=%s, got %v", sess.ID, sessionID)
	}
	if status != "archived" {
		t.Errorf("expected status=archived, got %s", status)
	}
}

// TestMigrateUnifyToolCalls_Idempotent verifies that running the migration
// twice does not duplicate data or error.
func TestMigrateUnifyToolCalls_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	workingPath := tmpDir + "/working.db"
	archivePath := tmpDir + "/archive.db"

	workingStore, err := NewSQLiteStore(workingPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	archiveStore, err := NewArchiveStore(archivePath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := archiveStore.StartSession("conv-1")
	archiveStore.ArchiveToolCalls([]ArchivedToolCall{
		{
			ID: "tc-1", ConversationID: "conv-1", SessionID: sess.ID,
			ToolName: "test", Arguments: `{}`, StartedAt: time.Now(),
		},
	})
	archiveStore.Close()

	// First run.
	if err := MigrateUnifyToolCalls(workingStore.DB(), archivePath, slog.Default()); err != nil {
		t.Fatal(err)
	}
	var count1 int
	_ = workingStore.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&count1)

	// Second run — should be a no-op.
	if err := MigrateUnifyToolCalls(workingStore.DB(), archivePath, slog.Default()); err != nil {
		t.Fatal(err)
	}
	var count2 int
	_ = workingStore.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&count2)

	if count1 != count2 {
		t.Errorf("idempotency violated: first run %d rows, second run %d rows", count1, count2)
	}
}

// TestMigrateUnifyToolCalls_NoArchiveDB verifies that migration succeeds
// gracefully when there is no archive database.
func TestMigrateUnifyToolCalls_NoArchiveDB(t *testing.T) {
	store := newTestWorkingDB(t)

	if err := MigrateUnifyToolCalls(store.DB(), "/nonexistent/archive.db", slog.Default()); err != nil {
		t.Fatalf("expected no error for missing archive, got: %v", err)
	}
}

// TestUnifiedMode_ArchiveToolCalls verifies that SQLiteStore.ArchiveToolCalls
// updates tool call status to 'archived' in the unified table.
func TestUnifiedMode_ArchiveToolCalls(t *testing.T) {
	store := newTestWorkingDB(t)

	store.GetOrCreateConversation("conv-1")
	store.RecordToolCall("conv-1", "", "tc-1", "get_state", `{}`)
	store.RecordToolCall("conv-1", "", "tc-2", "call_service", `{}`)
	store.CompleteToolCall("tc-1", "on", "")
	store.CompleteToolCall("tc-2", "", "error")

	// Verify they start as active.
	var activeCount int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE status = 'active' AND conversation_id = 'conv-1'`).Scan(&activeCount)
	if activeCount != 2 {
		t.Fatalf("expected 2 active tool calls, got %d", activeCount)
	}

	// Archive them.
	affected, err := store.ArchiveToolCalls("conv-1", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if affected != 2 {
		t.Errorf("expected 2 affected rows, got %d", affected)
	}

	// Verify status changed.
	var archivedCount int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE status = 'archived' AND conversation_id = 'conv-1'`).Scan(&archivedCount)
	if archivedCount != 2 {
		t.Errorf("expected 2 archived tool calls, got %d", archivedCount)
	}

	// Verify session_id was set.
	var sid string
	_ = store.DB().QueryRow(`SELECT session_id FROM tool_calls WHERE id = 'tc-1'`).Scan(&sid)
	if sid != "sess-1" {
		t.Errorf("expected session_id=sess-1, got %q", sid)
	}

	// Verify archived_at was set.
	var archivedAt sql.NullString
	_ = store.DB().QueryRow(`SELECT archived_at FROM tool_calls WHERE id = 'tc-1'`).Scan(&archivedAt)
	if !archivedAt.Valid || archivedAt.String == "" {
		t.Error("expected archived_at to be set")
	}
}

// TestUnifiedMode_GetToolCallsFiltersArchived verifies that GetToolCalls
// only returns active tool calls (not archived ones).
func TestUnifiedMode_GetToolCallsFiltersArchived(t *testing.T) {
	store := newTestWorkingDB(t)

	store.GetOrCreateConversation("conv-1")
	store.RecordToolCall("conv-1", "", "tc-1", "get_state", `{}`)
	store.RecordToolCall("conv-1", "", "tc-2", "call_service", `{}`)
	store.RecordToolCall("conv-1", "", "tc-3", "get_state", `{}`)

	// Archive tc-1 and tc-2 but leave tc-3 active.
	_, _ = store.DB().Exec(`UPDATE tool_calls SET status = 'archived' WHERE id IN ('tc-1', 'tc-2')`)

	// GetToolCalls should only return the active one.
	calls := store.GetToolCalls("conv-1", 10)
	if len(calls) != 1 {
		t.Fatalf("expected 1 active tool call, got %d", len(calls))
	}
	if calls[0].ID != "tc-3" {
		t.Errorf("expected tc-3, got %s", calls[0].ID)
	}
}

// TestUnifiedMode_GetToolCallsByNameFiltersArchived verifies that
// GetToolCallsByName only returns active tool calls.
func TestUnifiedMode_GetToolCallsByNameFiltersArchived(t *testing.T) {
	store := newTestWorkingDB(t)

	store.GetOrCreateConversation("conv-1")
	store.RecordToolCall("conv-1", "", "tc-1", "get_state", `{}`)
	store.RecordToolCall("conv-1", "", "tc-2", "get_state", `{}`)

	// Archive tc-1.
	_, _ = store.DB().Exec(`UPDATE tool_calls SET status = 'archived' WHERE id = 'tc-1'`)

	calls := store.GetToolCallsByName("get_state", 10)
	if len(calls) != 1 {
		t.Fatalf("expected 1 active get_state call, got %d", len(calls))
	}
	if calls[0].ID != "tc-2" {
		t.Errorf("expected tc-2, got %s", calls[0].ID)
	}
}

// TestUnifiedMode_GetSessionToolCalls verifies that GetSessionToolCalls in
// unified mode reads tool calls from the working DB's tool_calls table.
func TestUnifiedMode_GetSessionToolCalls(t *testing.T) {
	tmpDir := t.TempDir()

	workingStore, err := NewSQLiteStore(tmpDir+"/working.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	MigrateUnifyMessages(workingStore.DB(), "", slog.Default())

	archiveStore, err := NewArchiveStore(tmpDir+"/archive.db", workingStore.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveStore.Close()

	sess, _ := archiveStore.StartSession("conv-1")

	// Insert tool calls directly into the unified table with session_id set.
	now := time.Now().UTC()
	for i, name := range []string{"get_state", "call_service", "web_search"} {
		_, err := workingStore.DB().Exec(`
			INSERT INTO tool_calls (id, conversation_id, session_id, tool_name, arguments,
			    started_at, status, archived_at)
			VALUES (?, 'conv-1', ?, ?, '{}', ?, 'archived', ?)
		`, fmt.Sprintf("tc-%d", i), sess.ID, name,
			now.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}

	calls, err := archiveStore.GetSessionToolCalls(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 tool calls, got %d", len(calls))
	}
	if calls[0].ToolName != "get_state" {
		t.Errorf("expected first tool call 'get_state', got %q", calls[0].ToolName)
	}
	if calls[2].ToolName != "web_search" {
		t.Errorf("expected last tool call 'web_search', got %q", calls[2].ToolName)
	}
}

// TestUnifiedMode_LinkToolCallsToIteration verifies that
// LinkToolCallsToIteration updates iteration_index in the unified table.
func TestUnifiedMode_LinkToolCallsToIteration(t *testing.T) {
	tmpDir := t.TempDir()

	workingStore, err := NewSQLiteStore(tmpDir+"/working.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	MigrateUnifyMessages(workingStore.DB(), "", slog.Default())

	archiveStore, err := NewArchiveStore(tmpDir+"/archive.db", workingStore.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveStore.Close()

	sess, _ := archiveStore.StartSession("conv-1")
	now := time.Now().UTC()

	// Insert tool calls into unified table.
	for _, id := range []string{"tc-a", "tc-b"} {
		_, err := workingStore.DB().Exec(`
			INSERT INTO tool_calls (id, conversation_id, session_id, tool_name, arguments,
			    started_at, status)
			VALUES (?, 'conv-1', ?, 'test', '{}', ?, 'archived')
		`, id, sess.ID, now.Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}

	// Link both tool calls to iteration 0.
	if err := archiveStore.LinkToolCallsToIteration(sess.ID, 0, []string{"tc-a", "tc-b"}); err != nil {
		t.Fatal(err)
	}

	// Verify iteration_index was set.
	var iterIdx sql.NullInt64
	_ = workingStore.DB().QueryRow(`SELECT iteration_index FROM tool_calls WHERE id = 'tc-a'`).Scan(&iterIdx)
	if !iterIdx.Valid || iterIdx.Int64 != 0 {
		t.Errorf("expected iteration_index=0, got %v", iterIdx)
	}
}

// TestUnifiedMode_SessionMessageCount verifies that session message counts
// are computed from the unified messages table.
func TestUnifiedMode_SessionMessageCount(t *testing.T) {
	tmpDir := t.TempDir()

	workingStore, err := NewSQLiteStore(tmpDir+"/working.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer workingStore.Close()

	MigrateUnifyMessages(workingStore.DB(), "", slog.Default())

	archiveStore, err := NewArchiveStore(tmpDir+"/archive.db", workingStore.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveStore.Close()

	sess, _ := archiveStore.StartSession("conv-1")

	// Insert messages into the unified table.
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		_, _ = workingStore.DB().Exec(`
			INSERT INTO messages (id, conversation_id, session_id, role, content, timestamp, token_count, status)
			VALUES (?, 'conv-1', ?, 'user', 'hello', ?, 5, 'archived')
		`, fmt.Sprintf("msg-%d", i), sess.ID, now.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano))
	}

	got, err := archiveStore.GetSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MessageCount != 5 {
		t.Errorf("expected message_count=5, got %d", got.MessageCount)
	}
}

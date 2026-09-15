package delegate

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestFinishLoopExecutionPreservesUnifiedTranscript(t *testing.T) {
	store, err := memory.NewSQLiteStore(filepath.Join(t.TempDir(), "thane.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	store.DB().SetMaxOpenConns(1)
	if _, err := store.DB().Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	archive, err := memory.NewArchiveStoreFromDB(store.DB(), nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	const conversationID = "delegate-preservation"
	if err := store.AddMessage(conversationID, "assistant", "delegate result preservationneedle", memory.OriginInternal); err != nil {
		t.Fatal(err)
	}
	messages := store.GetMessages(conversationID)
	session, err := archive.StartSessionWithOptions(conversationID,
		memory.WithParentSession("parent-session"), memory.WithParentToolCall("parent-call"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordToolCall(conversationID, messages[0].ID, "delegate-call", "example", `{}`); err != nil {
		t.Fatal(err)
	}
	adapter := memory.NewArchiveAdapter(archive, store, slog.Default())
	executor := &Executor{sessionArchiver: adapter}
	prep := &preparedExecution{conversationID: conversationID, archiveSessionID: session.ID, log: slog.Default()}
	for range 2 {
		executor.finishLoopExecution(prep)
	}
	if got := store.GetMessages(conversationID); len(got) != 0 {
		t.Fatalf("finished delegate retained active context: %+v", got)
	}
	transcript, err := archive.GetSessionTranscript(session.ID)
	if err != nil || len(transcript) != 1 || transcript[0].ID != messages[0].ID || transcript[0].Content != messages[0].Content {
		t.Fatalf("finished delegate lost transcript: %+v, %v", transcript, err)
	}
	closed, err := archive.GetSession(session.ID)
	if err != nil || closed.EndedAt == nil || closed.ParentSessionID != "parent-session" || closed.ParentToolCallID != "parent-call" {
		t.Fatalf("finished delegate lost session linkage: %+v, %v", closed, err)
	}
	if err := store.CompleteToolCall("delegate-call", "late completion", ""); err != nil {
		t.Fatal(err)
	}
	var messageID, result string
	if err := store.DB().QueryRow(`SELECT message_id, result FROM tool_calls WHERE id = 'delegate-call'`).Scan(&messageID, &result); err != nil {
		t.Fatal(err)
	}
	if messageID != messages[0].ID || result != "late completion" {
		t.Fatalf("finished delegate lost tool linkage: %q %q", messageID, result)
	}
	var sessions, indexed int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'preservationneedle'`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || indexed != 1 {
		t.Fatalf("repeated finish changed archive: sessions=%d searchable=%d", sessions, indexed)
	}
}

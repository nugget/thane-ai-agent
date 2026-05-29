package app

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/runtime/archivist"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"

	_ "github.com/mattn/go-sqlite3"
)

func newQueueTestApp(t *testing.T) (*App, *loopqueue.Store) {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new queue store: %v", err)
	}
	return &App{loopQueue: store}, store
}

// TestEnqueueSessionCloseWork verifies a closed session becomes a pending
// archivist work item keyed dedup on session:<id> — and is NOT delivered
// as a loop notification (the decoupling fix, #1024).
func TestEnqueueSessionCloseWork(t *testing.T) {
	a, store := newQueueTestApp(t)
	const sessionID = "019e6867-00fc-7d6d-88be-58fab5c173c4"

	if err := a.enqueueSessionCloseWork(context.Background(), sessionID, "idle_timeout"); err != nil {
		t.Fatalf("enqueueSessionCloseWork: %v", err)
	}

	items, err := store.Peek(archivist.DefinitionName, 10)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("archivist queue has %d items, want 1", len(items))
	}
	if items[0].DedupKey != "session:"+sessionID {
		t.Errorf("dedup_key = %q, want session:%s", items[0].DedupKey, sessionID)
	}
	source, _ := projectQueuePayload(items[0].Payload)
	if source != "session_close" {
		t.Errorf("payload source = %q, want session_close", source)
	}

	// Re-enqueue of the same session coalesces (dedup), not duplicates.
	if err := a.enqueueSessionCloseWork(context.Background(), sessionID, "again"); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	if n, _ := store.PendingCount(archivist.DefinitionName); n != 1 {
		t.Errorf("pending after re-enqueue = %d, want 1 (coalesced)", n)
	}
}

func TestEnqueueSessionCloseWork_EmptySessionID(t *testing.T) {
	a, _ := newQueueTestApp(t)
	if err := a.enqueueSessionCloseWork(context.Background(), "", "x"); err == nil {
		t.Fatal("enqueueSessionCloseWork with empty session_id should error")
	}
}

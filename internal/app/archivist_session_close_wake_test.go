package app

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/runtime/archivist"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"

	_ "modernc.org/sqlite"
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
	const convID = "signal-15125551234" // interactive origin → archivable

	if err := a.enqueueSessionCloseWork(context.Background(), sessionID, convID, "idle_timeout"); err != nil {
		t.Fatalf("enqueueSessionCloseWork: %v", err)
	}

	items, err := store.Peek(context.Background(), archivist.DefinitionName, 10)
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
	if err := a.enqueueSessionCloseWork(context.Background(), sessionID, convID, "again"); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	if n, _ := store.PendingCount(context.Background(), archivist.DefinitionName); n != 1 {
		t.Errorf("pending after re-enqueue = %d, want 1 (coalesced)", n)
	}
}

func TestEnqueueSessionCloseWork_EmptySessionID(t *testing.T) {
	a, _ := newQueueTestApp(t)
	if err := a.enqueueSessionCloseWork(context.Background(), "", "signal-x", "x"); err == nil {
		t.Fatal("enqueueSessionCloseWork with empty session_id should error")
	}
}

// TestEnqueueSessionCloseWork_SkipsAutomationOrigins verifies the archival
// policy (issue #1024): sessions from autonomous/automation/auxiliary origins
// are not enqueued for the archivist, so it isn't drowned in service-loop and
// scheduled-task bookkeeping — while an interactive origin still enqueues.
func TestEnqueueSessionCloseWork_SkipsAutomationOrigins(t *testing.T) {
	a, store := newQueueTestApp(t)
	for _, convID := range []string{
		"loop-metacognitive-3-1780000000000",
		"sched-019c8366-b115-7203-88f7-b765f7c068be-019d6487",
		"metacog-abc",
		"owu-auxiliary",
	} {
		if err := a.enqueueSessionCloseWork(context.Background(), "sess-"+convID, convID, "idle_timeout"); err != nil {
			t.Fatalf("enqueue %s: %v", convID, err)
		}
	}
	if n, _ := store.PendingCount(context.Background(), archivist.DefinitionName); n != 0 {
		t.Errorf("automation-origin sessions enqueued %d items, want 0", n)
	}

	if err := a.enqueueSessionCloseWork(context.Background(), "sess-real", "signal-15125551234", "idle_timeout"); err != nil {
		t.Fatalf("enqueue interactive: %v", err)
	}
	if n, _ := store.PendingCount(context.Background(), archivist.DefinitionName); n != 1 {
		t.Errorf("after interactive enqueue, pending = %d, want 1", n)
	}
}

func TestIsArchivableSession(t *testing.T) {
	cases := []struct {
		conv string
		want bool
	}{
		{"signal-15125551234", true},
		{"email-handler-1", true},
		{"delegate-abc", true},
		{"media-feed-1", true},
		{"owu-a1b2c3d4e5f6a7b8", true}, // real OWU chat (owu-<hash>) is substantive
		{"", true},                     // unknown/empty origin defaults archivable (don't drop substance)
		{"loop-metacognitive-1-2", false},
		{"sched-task-exec", false},
		{"metacog-1", false},
		{"owu-auxiliary", false}, // only the fixed auxiliary id is skipped
	}
	for _, tc := range cases {
		if got := isArchivableSession(tc.conv); got != tc.want {
			t.Errorf("isArchivableSession(%q) = %v, want %v", tc.conv, got, tc.want)
		}
	}
}

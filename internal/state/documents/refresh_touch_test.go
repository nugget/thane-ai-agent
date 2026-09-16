package documents

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// heldRefreshGrace bounds how long a mutation may take while a refresh
// pass holds refreshMu. It is a negative control, not a sleep: the pass
// here never ends, so a mutation that blocks on refreshMu blocks forever
// and a mutation that does not finishes in microseconds. A longer grace
// only slows the passing case down.
const heldRefreshGrace = 2 * time.Second

// TestStoreMutationDoesNotWaitOnARefreshPass pins that a document write
// completes while Refresh holds refreshMu. Refresh takes that lock before
// its throttle check and keeps it across the walk of every root, which
// periodically includes a full git re-verification pass; a mutation that
// blocked on it would do so holding its root's mutation lock, and queue
// every other mutation on that root behind a pass they have no business
// waiting for.
func TestStoreMutationDoesNotWaitOnARefreshPass(t *testing.T) {
	t.Parallel()

	kbDir := filepath.Join(t.TempDir(), "kb")
	writeFile(t, filepath.Join(kbDir, "doc.md"), "# Doc\n\nIndexed once.\n")

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}

	// Stand in for a refresh pass in flight: anything that waits on
	// refreshMu waits for as long as this test chooses to hold it.
	store.refreshMu.Lock()

	written := make(chan error, 1)
	go func() {
		_, writeErr := store.Write(ctx, WriteArgs{
			Ref:   "kb:note.md",
			Title: "Note",
			Body:  stringPtr("Bob keeps bees.\n"),
		})
		written <- writeErr
	}()

	blocked := false
	var writeErr error
	select {
	case writeErr = <-written:
	case <-time.After(heldRefreshGrace):
		blocked = true
	}
	// Ending the pass before reporting anything: a write that did block
	// is holding its root's mutation lock, and it must be let go before
	// this test tears the store down, or the failure becomes a hang.
	store.refreshMu.Unlock()
	if blocked {
		<-written
		t.Fatal("Write did not finish while a refresh pass held refreshMu; it is waiting on that pass with its root's mutation lock held")
	}
	if writeErr != nil {
		t.Fatalf("Write: %v", writeErr)
	}

	// The touch still happened, so the next Refresh inside the interval
	// can still skip its walk.
	if store.lastRefreshAt().IsZero() {
		t.Error("lastRefreshAt is zero after a write; the write did not record that the index is current")
	}
}

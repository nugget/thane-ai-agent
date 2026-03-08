package notifications

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newTestTimeoutWatcher(t *testing.T) (*TimeoutWatcher, *RecordStore, *mockInjector, *mockDelegateSpawner) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewRecordStore(filepath.Join(dir, "test.db"), slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	inj := &mockInjector{alive: false}
	del := newMockDelegateSpawner()
	dispatcher := NewCallbackDispatcher(store, inj, del, slog.Default())
	watcher := NewTimeoutWatcher(store, dispatcher, nil, 10*time.Millisecond, slog.Default())
	return watcher, store, inj, del
}

func TestTimeoutWatcher_NoExpiredRecords(t *testing.T) {
	watcher, _, _, del := newTestTimeoutWatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	// Run one check cycle then cancel.
	watcher.check(ctx)
	cancel()

	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns, got %d", len(del.spawns))
	}
}

func TestTimeoutWatcher_CancelAction(t *testing.T) {
	watcher, store, _, del := newTestTimeoutWatcher(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID:     "req-cancel",
		Recipient:     "nugget",
		Actions:       []Action{{ID: "ok", Label: "OK"}},
		TimeoutAction: "cancel",
		CreatedAt:     now.Add(-35 * time.Minute),
		ExpiresAt:     now.Add(-5 * time.Minute),
	}
	if err := store.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	watcher.check(context.Background())

	// Should be expired, not dispatched.
	rec, _ := store.Get("req-cancel")
	if rec.Status != StatusExpired {
		t.Errorf("Status = %q, want %q", rec.Status, StatusExpired)
	}
	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns for cancel, got %d", len(del.spawns))
	}
}

func TestTimeoutWatcher_EmptyTimeoutAction(t *testing.T) {
	watcher, store, _, del := newTestTimeoutWatcher(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID:     "req-empty",
		Recipient:     "nugget",
		Actions:       []Action{{ID: "ok", Label: "OK"}},
		TimeoutAction: "", // empty = cancel
		CreatedAt:     now.Add(-35 * time.Minute),
		ExpiresAt:     now.Add(-5 * time.Minute),
	}
	if err := store.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	watcher.check(context.Background())

	rec, _ := store.Get("req-empty")
	if rec.Status != StatusExpired {
		t.Errorf("Status = %q, want %q", rec.Status, StatusExpired)
	}
	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns, got %d", len(del.spawns))
	}
}

func TestTimeoutWatcher_ActionIDDispatch(t *testing.T) {
	watcher, store, _, del := newTestTimeoutWatcher(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID:          "req-auto",
		Recipient:          "nugget",
		OriginConversation: "signal-15551234567",
		Context:            "Auto-deny context",
		Actions:            []Action{{ID: "approve", Label: "Yes"}, {ID: "deny", Label: "No"}},
		TimeoutAction:      "deny",
		CreatedAt:          now.Add(-35 * time.Minute),
		ExpiresAt:          now.Add(-5 * time.Minute),
	}
	if err := store.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	watcher.check(context.Background())

	rec, _ := store.Get("req-auto")
	if rec.Status != StatusExpired {
		t.Errorf("Status = %q, want %q", rec.Status, StatusExpired)
	}

	// Should have dispatched a delegate (session is dead in mock).
	// Delegate spawn runs in a goroutine — wait for it.
	del.waitSpawn(t)
	if len(del.spawns) != 1 {
		t.Fatalf("expected 1 delegate spawn, got %d", len(del.spawns))
	}

	// Verify the auto-executed action was persisted via SetResponseAction.
	rec, _ = store.Get("req-auto")
	if rec.ResponseAction != "deny" {
		t.Errorf("ResponseAction = %q, want %q (auto-executed)", rec.ResponseAction, "deny")
	}
}

func TestTimeoutWatcher_ContextCancellation(t *testing.T) {
	watcher, _, _, _ := newTestTimeoutWatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watcher.Start(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// Success — Start returned.
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

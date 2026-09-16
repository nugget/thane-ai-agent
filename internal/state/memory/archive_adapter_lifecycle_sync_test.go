package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAdapterSessionOperationsWaitForCommittedCachePublication(t *testing.T) {
	for _, operation := range []string{"id", "timing", "ensure", "enumerate", "start", "close"} {
		t.Run(operation, func(t *testing.T) {
			adapter, archive, store := newTestAdapter(t)
			if err := store.AddMessage("conv", "user", "before transition", OriginChannel); err != nil {
				t.Fatal(err)
			}
			oldID, err := adapter.StartSession("conv")
			if err != nil {
				t.Fatal(err)
			}

			// Stop at the actual gap under review: SQLite has committed the
			// boundary, while the cache still advertises the closed session.
			// Holding the real lifecycle gate makes this state deterministic
			// without a production-only timing hook or racing ResetSession.
			adapter.lifecycleMu.Lock()
			locked := true
			defer func() {
				if locked {
					adapter.lifecycleMu.Unlock()
				}
			}()
			result, err := archive.transitionSession("conv", sessionTransition{reason: "reset", restart: operation != "enumerate"})
			if err != nil {
				t.Fatal(err)
			}
			closed, err := archive.GetSession(oldID)
			if err != nil || closed.EndedAt == nil {
				t.Fatalf("boundary was not committed: %+v, %v", closed, err)
			}
			adapter.mu.RLock()
			cached := adapter.sessions["conv"].id
			adapter.mu.RUnlock()
			if cached != oldID {
				t.Fatalf("test did not establish stale cache: got %q, want %q", cached, oldID)
			}
			if operation == "close" {
				if err := store.AddMessage("conv", "user", "successor message", OriginChannel); err != nil {
					t.Fatal(err)
				}
			}

			type observation struct {
				value string
				err   error
			}
			started := make(chan struct{})
			done := make(chan observation, 1)
			go func() {
				close(started)
				var got observation
				switch operation {
				case "id":
					got.value = adapter.ActiveSessionID("conv")
				case "timing":
					got.value = adapter.ActiveSessionStartedAt("conv").Format(time.RFC3339Nano)
				case "ensure":
					got.value = adapter.EnsureSession("conv")
				case "enumerate":
					got.value = strings.Join(adapter.ActiveConversationIDs(), ",")
				case "start":
					got.value, got.err = adapter.StartSession("conv")
				case "close":
					got.err = adapter.CloseConversation("conv", "closed after publication")
				}
				done <- got
			}()
			<-started
			select {
			case got := <-done:
				t.Fatalf("%s crossed the unpublished boundary: %+v", operation, got)
			case <-time.After(50 * time.Millisecond):
			}

			adapter.publishSessionTransitionLocked("conv", result)
			adapter.lifecycleMu.Unlock()
			locked = false
			var got observation
			select {
			case got = <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s remained blocked after cache publication", operation)
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			switch operation {
			case "id", "ensure":
				if got.value != result.current.ID {
					t.Fatalf("returned session %q, want published successor %q", got.value, result.current.ID)
				}
			case "timing":
				if want := result.current.StartedAt.Format(time.RFC3339Nano); got.value != want {
					t.Fatalf("returned timing %q, want published successor %q", got.value, want)
				}
			case "enumerate":
				if got.value != "" {
					t.Fatalf("enumerated closed conversation %q", got.value)
				}
			case "start":
				if got.value == "" || got.value == result.current.ID || adapter.ActiveSessionID("conv") != got.value {
					t.Fatalf("start did not follow boundary publication: %+v", got)
				}
			case "close":
				if sid := adapter.ActiveSessionID("conv"); sid != "" {
					t.Fatalf("close left closed session cached: %q", sid)
				}
				var owner string
				if err := store.DB().QueryRow(`SELECT session_id FROM messages WHERE content = 'successor message'`).Scan(&owner); err != nil {
					t.Fatal(err)
				}
				if owner != result.current.ID {
					t.Fatalf("archived successor message into stale session %q", owner)
				}
			}
		})
	}
}

func TestAdapterEnsureSessionCreatesOnceForConcurrentCallers(t *testing.T) {
	adapter, _, store := newTestAdapter(t)
	const callers = 16
	start := make(chan struct{})
	ids := make(chan string, callers)
	for range callers {
		go func() {
			<-start
			ids <- adapter.EnsureSession("conv")
		}()
	}
	close(start)
	var sessionID string
	for range callers {
		select {
		case id := <-ids:
			if id == "" || (sessionID != "" && sessionID != id) {
				t.Fatalf("concurrent ensure returned %q after %q", id, sessionID)
			}
			sessionID = id
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent ensure deadlocked")
		}
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE conversation_id = 'conv'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent ensure created %d sessions", count)
	}
}

func TestAdapterSessionCloseCallbacksCanReenter(t *testing.T) {
	for _, operation := range []string{"reset", "close"} {
		t.Run(operation, func(t *testing.T) {
			adapter, archive, store := newTestAdapter(t)
			if err := store.AddMessage("conv", "user", "before close", OriginChannel); err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.StartSession("conv"); err != nil {
				t.Fatal(err)
			}
			callbackResult := make(chan error, 1)
			archive.SetSessionCloseCallback(func(closedID, reason string) {
				closed, err := archive.GetSession(closedID)
				if err != nil || closed.EndedAt == nil {
					callbackResult <- fmt.Errorf("callback preceded committed close: %v", err)
					return
				}
				if got := adapter.ActiveSessionID("conv"); got == closedID {
					callbackResult <- fmt.Errorf("callback read stale closed session %q", got)
					return
				}
				// EnsureSession takes the exclusive lifecycle lock, so this
				// also proves the notifier released more than the cache lock.
				current := adapter.EnsureSession("conv")
				if current == "" || current == closedID || adapter.ActiveSessionStartedAt("conv").IsZero() {
					callbackResult <- fmt.Errorf("callback could not establish successor: %q", current)
					return
				}
				if ids := adapter.ActiveConversationIDs(); len(ids) != 1 || ids[0] != "conv" {
					callbackResult <- fmt.Errorf("callback enumeration = %v", ids)
					return
				}
				callbackResult <- nil
			})
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "reset":
					done <- adapter.ResetSession("conv", "reset", "")
				case "close":
					done <- adapter.CloseConversation("conv", "close")
				}
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("session callback deadlocked while re-entering the adapter")
			}
			select {
			case err := <-callbackResult:
				if err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("session close callback was not delivered")
			}
		})
	}
}

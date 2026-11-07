package memory

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
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

	archiveStore, err := NewArchiveStoreFromDB(workingStore.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	adapter := NewArchiveAdapter(archiveStore, workingStore, logger)

	return adapter, archiveStore, workingStore
}

func TestAdapter_CloseConversation(t *testing.T) {
	adapter, archiveStore, workingStore := newTestAdapter(t)

	// Start a session first.
	sid, err := adapter.StartSession("conv-1")
	if err != nil {
		t.Fatal(err)
	}

	// Add active messages to the unified table.
	workingStore.GetOrCreateConversation("conv-1")
	for _, content := range []string{"hello", "hi there!"} {
		if err := workingStore.AddMessage("conv-1", "user", content, ""); err != nil {
			t.Fatal(err)
		}
	}

	if err := adapter.CloseConversation("conv-1", "reset"); err != nil {
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

func TestAdapter_CloseConversation_WithToolCalls(t *testing.T) {
	adapter, _, workingStore := newTestAdapter(t)

	sid, _ := adapter.StartSession("conv-1")

	// Add messages and tool calls to the unified tables.
	workingStore.GetOrCreateConversation("conv-1")
	workingStore.AddMessage("conv-1", "user", "search for test", "")
	workingStore.RecordToolCall("conv-1", "", "tc-1", "web_search", `{"query":"test"}`)
	workingStore.CompleteToolCall("tc-1", "search results", "")

	if err := adapter.CloseConversation("conv-1", "reset"); err != nil {
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
	if err := adapter.CloseConversation("conv-1", "reset"); err != nil {
		t.Fatal(err)
	}

	// No longer active.
	if got := adapter.ActiveSessionID("conv-1"); got != "" {
		t.Errorf("expected no active session after end, got %s", got)
	}
}

func TestAdapter_StartSessionSnapshotsConversationBinding(t *testing.T) {
	adapter, archiveStore, workingStore := newTestAdapter(t)

	err := workingStore.BindConversationChannel("signal-15551234567", &ChannelBinding{
		Channel:     "signal",
		Address:     "+15551234567",
		ContactID:   "contact-1",
		ContactName: "Alice Smith",
		TrustZone:   "known",
		LinkSource:  "tel",
	})
	if err != nil {
		t.Fatalf("BindConversationChannel: %v", err)
	}

	sid, err := adapter.StartSession("signal-15551234567")
	if err != nil {
		t.Fatal(err)
	}

	sess, err := archiveStore.GetSession(sid)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Metadata == nil || sess.Metadata.ChannelBinding == nil {
		t.Fatalf("session metadata = %#v", sess.Metadata)
	}
	if sess.Metadata.ChannelBinding.ContactName != "Alice Smith" || sess.Metadata.ChannelBinding.TrustZone != "known" {
		t.Fatalf("session channel binding = %#v", sess.Metadata.ChannelBinding)
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

func TestAdapter_MessageCountTracksUnifiedRows(t *testing.T) {
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
	_, _ = adapter.StartSession("conv-1")

	// Should return non-zero start time.
	got := adapter.ActiveSessionStartedAt("conv-1")
	if got.IsZero() {
		t.Error("expected non-zero start time for active session")
	}

	// End session — clear cache.
	adapter.CloseConversation("conv-1", "reset")

	// Should return zero again.
	if got := adapter.ActiveSessionStartedAt("conv-1"); !got.IsZero() {
		t.Errorf("expected zero time after session end, got %v", got)
	}
}

func TestAdapter_ActiveConversationIDs(t *testing.T) {
	adapter, archiveStore, _ := newTestAdapter(t)

	// Cached active session.
	if _, err := adapter.StartSession("conv-cached"); err != nil {
		t.Fatal(err)
	}

	// Store-only active session (simulates restart / cache miss).
	if _, err := archiveStore.StartSession("conv-store"); err != nil {
		t.Fatal(err)
	}

	got := adapter.ActiveConversationIDs()
	want := []string{"conv-cached", "conv-store"}
	if !slices.Equal(got, want) {
		t.Fatalf("ActiveConversationIDs() = %v, want %v", got, want)
	}
}

// cachedSessionID reads the adapter's in-memory entry without the store
// fallback, so tests can tell an eviction from a cache hit.
func cachedSessionID(a *ArchiveAdapter, conversationID string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.sessions[conversationID].id
}

func TestAdapter_ForgetSession(t *testing.T) {
	const conv = "conv-forget"
	tests := []struct {
		name string
		// prepare returns the session ID to forget, the entry the cache
		// must hold afterwards, and what ActiveSessionID must report.
		prepare func(t *testing.T, a *ArchiveAdapter, store *ArchiveStore) (forget, wantCached, wantActive string)
	}{
		{
			name: "closed session is evicted",
			prepare: func(t *testing.T, a *ArchiveAdapter, store *ArchiveStore) (string, string, string) {
				sid := a.EnsureSession(conv)
				if err := store.EndSession(sid, "idle_timeout"); err != nil {
					t.Fatal(err)
				}
				return sid, "", ""
			},
		},
		{
			name: "stale ID after reset keeps the successor cached",
			prepare: func(t *testing.T, a *ArchiveAdapter, _ *ArchiveStore) (string, string, string) {
				old := a.EnsureSession(conv)
				if err := a.ResetSession(conv, "reset", ""); err != nil {
					t.Fatal(err)
				}
				successor := cachedSessionID(a, conv)
				if successor == "" || successor == old {
					t.Fatalf("reset cached %q, want a successor to %q", successor, old)
				}
				return old, successor, successor
			},
		},
		{
			name: "unrelated ID keeps the open session cached",
			prepare: func(t *testing.T, a *ArchiveAdapter, _ *ArchiveStore) (string, string, string) {
				sid := a.EnsureSession(conv)
				return "unrelated-session", sid, sid
			},
		},
		{
			name: "uncached conversation is a no-op",
			prepare: func(t *testing.T, _ *ArchiveAdapter, _ *ArchiveStore) (string, string, string) {
				return "unrelated-session", "", ""
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, store, _ := newTestAdapter(t)
			forget, wantCached, wantActive := tt.prepare(t, adapter, store)

			adapter.ForgetSession(conv, forget)

			if got := cachedSessionID(adapter, conv); got != wantCached {
				t.Errorf("cached session = %q, want %q", got, wantCached)
			}
			if got := adapter.ActiveSessionID(conv); got != wantActive {
				t.Errorf("ActiveSessionID = %q, want %q", got, wantActive)
			}
		})
	}
}

// TestAdapter_IdleCloseStartsFreshSession drives the summarizer's idle
// close against an adapter that already cached the session, the shape of
// a long-lived conversation that goes quiet past the idle timeout.
func TestAdapter_IdleCloseStartsFreshSession(t *testing.T) {
	const conv = "conv-idle"
	tests := []struct {
		name string
		// idleFor backdates the session start; the timeout is 30 minutes.
		idleFor   time.Duration
		wantClose bool
	}{
		{name: "idle session is evicted and replaced", idleFor: 2 * time.Hour, wantClose: true},
		{name: "recent session stays cached", idleFor: time.Minute, wantClose: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, store, workingStore := newTestAdapter(t)
			old := adapter.EnsureSession(conv)
			if old == "" {
				t.Fatal("EnsureSession returned no session")
			}
			started := time.Now().UTC().Add(-tt.idleFor).Format(time.RFC3339Nano)
			if _, err := workingStore.DB().Exec(`UPDATE sessions SET started_at = ? WHERE id = ?`, started, old); err != nil {
				t.Fatal(err)
			}

			type closed struct{ conv, session string }
			var calls []closed
			var logs bytes.Buffer
			cfg := SummarizerConfig{
				IdleTimeout: 30 * time.Minute,
				OnIdleClose: func(conversationID, sessionID string) {
					// The close is the cause and the eviction its effect,
					// so the backstop line must already be in the log.
					if !strings.Contains(logs.String(), "closed idle session (backstop)") {
						t.Errorf("OnIdleClose ran before the backstop close was logged; log so far:\n%s", logs.String())
					}
					calls = append(calls, closed{conversationID, sessionID})
					adapter.ForgetSession(conversationID, sessionID)
				},
			}
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			w := NewSummarizerWorker(store, &mockLLMClient{}, newTestRouter(), logger, cfg)
			w.closeIdleSessions(context.Background())

			if !tt.wantClose {
				if len(calls) != 0 {
					t.Fatalf("OnIdleClose called %v for a session within the timeout", calls)
				}
				if got := adapter.ActiveSessionID(conv); got != old {
					t.Errorf("ActiveSessionID = %q, want the open session %q", got, old)
				}
				return
			}

			if want := []closed{{conv, old}}; !slices.Equal(calls, want) {
				t.Fatalf("OnIdleClose calls = %v, want %v", calls, want)
			}
			if got := adapter.ActiveSessionID(conv); got != "" {
				t.Fatalf("ActiveSessionID after idle close = %q, want none", got)
			}
			fresh := adapter.EnsureSession(conv)
			if fresh == "" || fresh == old {
				t.Fatalf("EnsureSession after idle close = %q, want a new session (closed %q)", fresh, old)
			}
			sess, err := store.GetSession(fresh)
			if err != nil {
				t.Fatal(err)
			}
			if sess.EndedAt != nil {
				t.Errorf("fresh session %q is already ended", fresh)
			}
		})
	}
}

// TestAdapter_ForgetSessionConcurrent races the idle-close eviction
// against cache-miss repopulation. Each round cools the cache while the
// session is still open, so concurrent lookups read it from the store,
// then ends it in the store and evicts it the way the summarizer's hook
// does, and checks that the very next EnsureSession returns an open
// session. If ForgetSession stopped holding lifecycleMu, a lookup that
// read the session just before EndSession committed could cache it again
// after the eviction, and the check would see the closed session. Workers
// also replay superseded session IDs, which the match guard must ignore.
func TestAdapter_ForgetSessionConcurrent(t *testing.T) {
	const conv = "conv-race"
	adapter, store, _ := newTestAdapter(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var superseded string
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = adapter.ActiveSessionID(conv)
				sid := adapter.EnsureSession(conv)
				if superseded != "" && superseded != sid {
					adapter.ForgetSession(conv, superseded)
				}
				superseded = sid
			}
		}()
	}

	for i := range 200 {
		sid := adapter.EnsureSession(conv)
		adapter.ForgetSession(conv, sid)
		if err := store.EndSession(sid, "idle_timeout"); err != nil {
			t.Error(err)
			break
		}
		adapter.ForgetSession(conv, sid)

		next := adapter.EnsureSession(conv)
		sess, err := store.GetSession(next)
		if err != nil {
			t.Error(err)
			break
		}
		if sess.EndedAt != nil {
			t.Errorf("round %d: EnsureSession after evicting %q returned closed session %q", i, sid, next)
			break
		}
	}
	close(stop)
	wg.Wait()
}

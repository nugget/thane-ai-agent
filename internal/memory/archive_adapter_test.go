package memory

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func newTestAdapter(t *testing.T) (*ArchiveAdapter, *ArchiveStore) {
	t.Helper()

	dbPath := t.TempDir() + "/test-adapter.db"
	store, err := NewArchiveStore(dbPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	adapter := NewArchiveAdapter(store, logger)

	return adapter, store
}

func TestAdapter_ArchiveConversation(t *testing.T) {
	adapter, store := newTestAdapter(t)

	// Start a session first
	sid, err := adapter.StartSession("conv-1")
	if err != nil {
		t.Fatal(err)
	}

	msgs := []Message{
		{Role: "user", Content: "hello", Timestamp: time.Now()},
		{Role: "assistant", Content: "hi there!", Timestamp: time.Now()},
	}

	if err := adapter.ArchiveConversation("conv-1", msgs, "reset"); err != nil {
		t.Fatal(err)
	}

	// Verify messages are in the archive
	transcript, err := store.GetSessionTranscript(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 2 {
		t.Fatalf("expected 2 archived messages, got %d", len(transcript))
	}
	if transcript[0].ArchiveReason != "reset" {
		t.Errorf("expected reason=reset, got %s", transcript[0].ArchiveReason)
	}
}

func TestAdapter_ArchiveConversation_WithToolCalls(t *testing.T) {
	adapter, store := newTestAdapter(t)

	// Create a mock tool call source
	mockSource := &mockToolCallSource{
		calls: map[string][]ToolCall{
			"conv-1": {
				{
					ID:             "tc-1",
					ConversationID: "conv-1",
					ToolName:       "web_search",
					Arguments:      `{"query":"test"}`,
					Result:         "search results here",
					StartedAt:      time.Now(),
				},
			},
		},
	}
	adapter.SetToolCallSource(mockSource)
	adapter.StartSession("conv-1")

	msgs := []Message{
		{Role: "user", Content: "search for test", Timestamp: time.Now()},
		{Role: "assistant", Content: "let me search", Timestamp: time.Now()},
	}

	if err := adapter.ArchiveConversation("conv-1", msgs, "reset"); err != nil {
		t.Fatal(err)
	}

	// Verify tool calls were archived
	sid := adapter.ActiveSessionID("conv-1")
	if sid == "" {
		// Session was ended by ArchiveConversation via reset flow — check the store directly
		sessions, _ := store.ListSessions("conv-1", 1)
		if len(sessions) == 0 {
			t.Fatal("no sessions found")
		}
		sid = sessions[0].ID
	}

	calls, err := store.GetSessionToolCalls(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].ToolName != "web_search" {
		t.Errorf("expected tool=web_search, got %s", calls[0].ToolName)
	}
}

func TestAdapter_ArchiveConversation_WithToolCallFields(t *testing.T) {
	adapter, store := newTestAdapter(t)
	sid, _ := adapter.StartSession("conv-1")

	now := time.Now()
	msgs := []Message{
		{Role: "user", Content: "do something", Timestamp: now},
		{Role: "assistant", Content: "calling tool", Timestamp: now.Add(time.Second),
			ToolCalls: `[{"id":"tc-1","name":"test_tool"}]`},
		{Role: "tool", Content: "tool result", Timestamp: now.Add(2 * time.Second),
			ToolCallID: "tc-1"},
	}

	if err := adapter.ArchiveConversation("conv-1", msgs, "manual"); err != nil {
		t.Fatal(err)
	}

	transcript, _ := store.GetSessionTranscript(sid)
	if len(transcript) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(transcript))
	}

	// Check tool call fields carried through
	if transcript[1].ToolCalls != `[{"id":"tc-1","name":"test_tool"}]` {
		t.Errorf("tool_calls not preserved: %s", transcript[1].ToolCalls)
	}
	if transcript[2].ToolCallID != "tc-1" {
		t.Errorf("tool_call_id not preserved: %s", transcript[2].ToolCallID)
	}
}

func TestAdapter_SessionLifecycle(t *testing.T) {
	adapter, _ := newTestAdapter(t)

	// No active session initially
	if sid := adapter.ActiveSessionID("conv-1"); sid != "" {
		t.Errorf("expected no active session, got %s", sid)
	}

	// Start session
	sid, err := adapter.StartSession("conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if sid == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Active session should match
	if got := adapter.ActiveSessionID("conv-1"); got != sid {
		t.Errorf("active session mismatch: %s != %s", got, sid)
	}

	// End session
	if err := adapter.EndSession(sid, "reset"); err != nil {
		t.Fatal(err)
	}

	// No longer active
	if got := adapter.ActiveSessionID("conv-1"); got != "" {
		t.Errorf("expected no active session after end, got %s", got)
	}
}

func TestAdapter_EnsureSession(t *testing.T) {
	adapter, _ := newTestAdapter(t)

	// EnsureSession should create one
	sid1 := adapter.EnsureSession("conv-1")
	if sid1 == "" {
		t.Fatal("expected session to be created")
	}

	// EnsureSession again should return the same one
	sid2 := adapter.EnsureSession("conv-1")
	if sid2 != sid1 {
		t.Errorf("expected same session, got %s != %s", sid1, sid2)
	}
}

func TestAdapter_OnMessage(t *testing.T) {
	adapter, store := newTestAdapter(t)

	sid, _ := adapter.StartSession("conv-1")

	adapter.OnMessage("conv-1")
	adapter.OnMessage("conv-1")
	adapter.OnMessage("conv-1")

	sess, _ := store.GetSession(sid)
	if sess.MessageCount != 3 {
		t.Errorf("expected message_count=3, got %d", sess.MessageCount)
	}
}

func TestAdapter_Summarizer(t *testing.T) {
	adapter, store := newTestAdapter(t)

	summarized := make(chan string, 1)
	adapter.SetSummarizer(func(ctx context.Context, messages []ArchivedMessage) (string, error) {
		summary := "discussed testing and architecture"
		summarized <- summary
		return summary, nil
	})

	sid, _ := adapter.StartSession("conv-1")

	// Archive some messages so the summarizer has something to work with
	msgs := []Message{
		{Role: "user", Content: "let's talk about tests", Timestamp: time.Now()},
		{Role: "assistant", Content: "sure, let's discuss", Timestamp: time.Now()},
	}
	_ = adapter.ArchiveConversation("conv-1", msgs, "manual")

	// End session triggers async summary
	adapter.EndSession(sid, "reset")

	// Wait for the summary goroutine
	select {
	case <-summarized:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for summary")
	}

	// Give a moment for the DB write after the channel send
	time.Sleep(100 * time.Millisecond)

	sess, _ := store.GetSession(sid)
	if sess.Summary != "discussed testing and architecture" {
		t.Errorf("expected summary, got %q", sess.Summary)
	}
}

func TestAdapter_ActiveSessionID_DBFallback(t *testing.T) {
	adapter, store := newTestAdapter(t)

	// Create a session directly in the store (bypassing adapter cache)
	sess, _ := store.StartSession("conv-1")

	// Adapter cache doesn't know about it — should fall back to DB
	got := adapter.ActiveSessionID("conv-1")
	if got != sess.ID {
		t.Errorf("expected DB fallback to find session %s, got %s", sess.ID[:8], got)
	}
}

// --- mocks ---

type mockToolCallSource struct {
	calls map[string][]ToolCall
}

func (m *mockToolCallSource) GetToolCalls(conversationID string, limit int) []ToolCall {
	return m.calls[conversationID]
}

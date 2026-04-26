package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func newArchiveTestRegistry(t *testing.T) (*Registry, *memory.ArchiveStore) {
	t.Helper()
	store, err := memory.NewArchiveStore(t.TempDir()+"/archive.db", nil, nil, nil)
	if err != nil {
		t.Fatalf("NewArchiveStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r := NewEmptyRegistry()
	r.SetArchiveStore(store)
	return r, store
}

func seedArchiveMessages(t *testing.T, store *memory.ArchiveStore, base time.Time, n int, conversationID, sessionID string) {
	t.Helper()
	msgs := make([]memory.Message, n)
	for i := range msgs {
		msgs[i] = memory.Message{
			ID:             "msg-" + sessionID + "-" + itoa(i),
			ConversationID: conversationID,
			SessionID:      sessionID,
			Role:           "user",
			Content:        "message " + itoa(i),
			Timestamp:      base.Add(time.Duration(i) * time.Minute),
			ArchiveReason:  "reset",
		}
	}
	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatalf("ArchiveMessages: %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func TestArchiveRangeTool_TimeWindow(t *testing.T) {
	r, store := newArchiveTestRegistry(t)
	now := time.Now()
	base := now.Add(-30 * time.Minute)
	seedArchiveMessages(t, store, base, 10, "conv-1", "sess-1")

	tool := r.Get("archive_range")
	if tool == nil {
		t.Fatal("archive_range tool not registered")
	}

	out, err := tool.Handler(context.Background(), map[string]any{
		"conversation_id": "conv-1",
		"min_time":        "-3600s", // 60 min ago — wider than seed window
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	var parsed struct {
		Messages  []memory.MessageView `json:"messages"`
		Truncated bool                 `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(parsed.Messages) == 0 {
		t.Fatal("no messages returned")
	}
	for _, m := range parsed.Messages {
		if m.SessionID != "sess-1" {
			t.Errorf("message session_id = %q, want sess-1", m.SessionID)
		}
		if !strings.HasPrefix(m.T, "-") {
			t.Errorf("delta should be negative for past messages, got %q", m.T)
		}
	}
}

func TestArchiveRangeTool_MinMessagesFloor(t *testing.T) {
	r, store := newArchiveTestRegistry(t)
	now := time.Now()
	base := now.Add(-2 * time.Hour)
	seedArchiveMessages(t, store, base, 8, "conv-1", "sess-1")

	tool := r.Get("archive_range")
	out, err := tool.Handler(context.Background(), map[string]any{
		"conversation_id": "conv-1",
		// Tight window that would normally return 0 messages.
		"min_time":     "-60s",
		"min_messages": float64(5),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	var parsed struct {
		Messages  []memory.MessageView `json:"messages"`
		Truncated bool                 `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Messages) != 5 {
		t.Fatalf("len = %d, want 5 (floor satisfied)", len(parsed.Messages))
	}
}

func TestArchiveRangeTool_MaxMessagesCap(t *testing.T) {
	r, store := newArchiveTestRegistry(t)
	now := time.Now()
	base := now.Add(-30 * time.Minute)
	seedArchiveMessages(t, store, base, 20, "conv-1", "sess-1")

	tool := r.Get("archive_range")
	out, err := tool.Handler(context.Background(), map[string]any{
		"conversation_id": "conv-1",
		"max_messages":    float64(5),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var parsed struct {
		Messages  []memory.MessageView `json:"messages"`
		Truncated bool                 `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Messages) != 5 {
		t.Fatalf("len = %d, want 5", len(parsed.Messages))
	}
	if !parsed.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestArchiveRangeTool_RFC3339Input(t *testing.T) {
	r, store := newArchiveTestRegistry(t)
	now := time.Now()
	base := now.Add(-1 * time.Hour)
	seedArchiveMessages(t, store, base, 5, "conv-1", "sess-1")

	tool := r.Get("archive_range")
	out, err := tool.Handler(context.Background(), map[string]any{
		"conversation_id": "conv-1",
		"min_time":        base.UTC().Format(time.RFC3339),
		"max_time":        base.Add(10 * time.Minute).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(out, `"messages":`) {
		t.Errorf("output missing messages array: %s", out)
	}
}

func TestArchiveRangeTool_InvalidTime(t *testing.T) {
	r, _ := newArchiveTestRegistry(t)
	tool := r.Get("archive_range")
	_, err := tool.Handler(context.Background(), map[string]any{
		"min_time": "yesterday",
	})
	if err == nil {
		t.Fatal("expected error for invalid min_time")
	}
	if !strings.Contains(err.Error(), "min_time") {
		t.Errorf("error should mention min_time, got: %v", err)
	}
}

func TestArchiveSessionsTool_JSONOutput(t *testing.T) {
	r, store := newArchiveTestRegistry(t)

	sess, err := store.StartSession("conv-1")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := store.EndSession(sess.ID, "manual"); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	tool := r.Get("archive_sessions")
	out, err := tool.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var parsed struct {
		Sessions  []memory.SessionView `json:"sessions"`
		Truncated bool                 `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(parsed.Sessions) != 1 {
		t.Fatalf("sessions len = %d, want 1", len(parsed.Sessions))
	}
	if parsed.Sessions[0].ID != sess.ID {
		t.Errorf("session id = %q, want %q", parsed.Sessions[0].ID, sess.ID)
	}
	if parsed.Sessions[0].Ended == "" {
		t.Error("ended delta empty for closed session")
	}
}

func TestArchiveSessionTranscriptTool_JSONOutput(t *testing.T) {
	r, store := newArchiveTestRegistry(t)

	now := time.Now()
	sess, err := store.StartSession("conv-1")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	msgs := []memory.Message{
		{ID: "m1", ConversationID: "conv-1", SessionID: sess.ID, Role: "user",
			Content: "hello", Timestamp: now.Add(-10 * time.Minute), ArchiveReason: "manual"},
		{ID: "m2", ConversationID: "conv-1", SessionID: sess.ID, Role: "assistant",
			Content: "hi", Timestamp: now.Add(-9 * time.Minute), ArchiveReason: "manual"},
	}
	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatalf("ArchiveMessages: %v", err)
	}

	tool := r.Get("archive_session_transcript")
	out, err := tool.Handler(context.Background(), map[string]any{
		"session_id": sess.ID,
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var parsed struct {
		Messages  []memory.MessageView `json:"messages"`
		Truncated bool                 `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "user" || parsed.Messages[1].Role != "assistant" {
		t.Errorf("roles = %q,%q", parsed.Messages[0].Role, parsed.Messages[1].Role)
	}
}

func TestArchiveSessionTranscriptTool_ShortIDPrefix(t *testing.T) {
	r, store := newArchiveTestRegistry(t)

	sess, err := store.StartSession("conv-1")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if len(sess.ID) < 8 {
		t.Skipf("session ID %q too short to truncate", sess.ID)
	}

	tool := r.Get("archive_session_transcript")
	_, err = tool.Handler(context.Background(), map[string]any{
		"session_id": sess.ID[:8],
	})
	if err != nil {
		t.Fatalf("handler with short ID: %v", err)
	}
}

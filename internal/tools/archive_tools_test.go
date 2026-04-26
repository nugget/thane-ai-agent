package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// newArchiveTestRegistry sets up a unified-mode ArchiveStore over a
// fresh SQLiteStore — the production wiring shape — and returns a
// helper that inserts messages via bound time.Time, matching the
// on-disk format go-sqlite3 produces in production.
func newArchiveTestRegistry(t *testing.T) (*Registry, *memory.ArchiveStore, func(convID, sessID, role, content string, ts time.Time)) {
	t.Helper()
	working, err := memory.NewSQLiteStore(t.TempDir()+"/working.db", 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = working.Close() })

	store, err := memory.NewArchiveStoreFromDB(working.DB(), nil, nil)
	if err != nil {
		t.Fatalf("NewArchiveStoreFromDB: %v", err)
	}

	r := NewEmptyRegistry()
	r.SetArchiveStore(store)

	insert := func(convID, sessID, role, content string, ts time.Time) {
		t.Helper()
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("uuid: %v", err)
		}
		_, err = working.DB().Exec(`
			INSERT INTO messages (id, conversation_id, session_id, role, content, timestamp, status)
			VALUES (?, ?, ?, ?, ?, ?, 'active')
		`, id.String(), convID, sessID, role, content, ts)
		if err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	return r, store, insert
}

func seedArchiveMessages(t *testing.T, insert func(convID, sessID, role, content string, ts time.Time), base time.Time, n int, conversationID, sessionID string) {
	t.Helper()
	for i := range n {
		insert(conversationID, sessionID, "user", "message "+itoa(i), base.Add(time.Duration(i)*time.Minute))
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
	r, _, insert := newArchiveTestRegistry(t)
	now := time.Now()
	base := now.Add(-30 * time.Minute)
	seedArchiveMessages(t, insert, base, 10, "conv-1", "sess-1")

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
	r, _, insert := newArchiveTestRegistry(t)
	now := time.Now()
	base := now.Add(-2 * time.Hour)
	seedArchiveMessages(t, insert, base, 8, "conv-1", "sess-1")

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
	// Floor semantics: when triggered, return up to MaxMessages (default
	// 200), not exactly MinMessages. With 8 archived messages and a
	// tight in-window query, the floor path delivers all 8.
	if len(parsed.Messages) != 8 {
		t.Fatalf("len = %d, want 8 (floor returns up to MaxMessages)", len(parsed.Messages))
	}
}

func TestArchiveRangeTool_MaxMessagesCap(t *testing.T) {
	r, _, insert := newArchiveTestRegistry(t)
	now := time.Now()
	base := now.Add(-30 * time.Minute)
	seedArchiveMessages(t, insert, base, 20, "conv-1", "sess-1")

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
	r, _, insert := newArchiveTestRegistry(t)
	now := time.Now()
	base := now.Add(-1 * time.Hour)
	seedArchiveMessages(t, insert, base, 5, "conv-1", "sess-1")

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
	r, _, _ := newArchiveTestRegistry(t)
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
	r, store, _ := newArchiveTestRegistry(t)

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
	r, store, insert := newArchiveTestRegistry(t)

	now := time.Now()
	sess, err := store.StartSession("conv-1")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	insert("conv-1", sess.ID, "user", "hello", now.Add(-10*time.Minute))
	insert("conv-1", sess.ID, "assistant", "hi", now.Add(-9*time.Minute))

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
	r, store, _ := newArchiveTestRegistry(t)

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

func TestArchiveRangeTool_ExcludeSessionID(t *testing.T) {
	r, _, insert := newArchiveTestRegistry(t)
	now := time.Now()
	base := now.Add(-30 * time.Minute)
	// Same conversation, two sessions: one we want to exclude, one we keep.
	insert("conv-1", "active", "user", "active-msg", base.Add(1*time.Minute))
	insert("conv-1", "archived", "user", "archived-msg", base.Add(2*time.Minute))

	tool := r.Get("archive_range")
	out, err := tool.Handler(context.Background(), map[string]any{
		"conversation_id":    "conv-1",
		"exclude_session_id": "active",
		"min_time":           "-3600s",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var parsed struct {
		Messages []memory.MessageView `json:"messages"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Messages) != 1 {
		t.Fatalf("len = %d, want 1 (only archived session)", len(parsed.Messages))
	}
	if parsed.Messages[0].SessionID != "archived" {
		t.Errorf("session_id = %q, want archived", parsed.Messages[0].SessionID)
	}
}

func TestArchiveSearchTool_NeverEmptyWhenResultsExist(t *testing.T) {
	// Regression: production hotfix for an archive_search that returned
	// `{"results":[],"truncated":true}` because each individual result
	// (match + up to 100 context messages × per-message content cap)
	// blew past archiveResultByteCap, so FitPrefix degenerated to 0
	// and silently swallowed every match.
	r, _, insert := newArchiveTestRegistry(t)
	now := time.Now()

	// Seed a session with the matched term plus enough surrounding
	// chatter that a real search expansion would balloon the result.
	insert("conv-1", "sess-1", "user", "looking for the freezer alarm details", now.Add(-30*time.Minute))
	for i := range 30 {
		ts := now.Add(-time.Duration(29-i) * time.Minute)
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		insert("conv-1", "sess-1", role, strings.Repeat("filler text ", 100), ts)
	}

	tool := r.Get("archive_search")
	out, err := tool.Handler(context.Background(), map[string]any{
		"query": "freezer",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var parsed struct {
		Results   []memory.SearchResultView `json:"results"`
		Truncated bool                      `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(parsed.Results) == 0 {
		t.Fatalf("results empty despite real matches existing — regression of the production bug:\n%s", out)
	}
}

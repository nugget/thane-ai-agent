package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// providerTestCtxKey is the context key the test uses to plumb a
// conversation ID into TagContext, mirroring what tools.WithConversationID
// does in production. Defined locally so the memory package's tests
// stay free of an upward import on the tools package.
type providerTestCtxKey string

const providerConvIDKey providerTestCtxKey = "conv_id"

func providerCtxConvID(ctx context.Context) string {
	if v, ok := ctx.Value(providerConvIDKey).(string); ok {
		return v
	}
	return ""
}

func providerCtxWithConvID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, providerConvIDKey, id)
}

// newMessageChannelTestSetup wires the provider against a
// production-shape store (NewArchiveStoreFromDB over a SQLiteStore) so
// the timestamp-format trap from #761 doesn't reappear.
func newMessageChannelTestSetup(t *testing.T) (*MessageChannelProvider, *ArchiveStore, func(convID, sessID, role, content string, ts time.Time)) {
	t.Helper()
	working, err := NewSQLiteStore(t.TempDir()+"/working.db", 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = working.Close() })

	archive, err := NewArchiveStoreFromDB(working.DB(), nil, nil)
	if err != nil {
		t.Fatalf("NewArchiveStoreFromDB: %v", err)
	}

	provider := NewMessageChannelProvider(archive, providerCtxConvID, MessageChannelProviderConfig{}, nil)

	insert := func(convID, sessID, role, content string, ts time.Time) {
		t.Helper()
		_, err := working.DB().Exec(`
			INSERT INTO messages (id, conversation_id, session_id, role, content, timestamp, status)
			VALUES (?, ?, ?, ?, ?, ?, 'active')
		`, "msg-"+sessID+"-"+content, convID, sessID, role, content, ts)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	return provider, archive, insert
}

// closedSession creates a closed session with n messages, started and
// ended at the given offsets from now.
func closedSession(t *testing.T, archive *ArchiveStore, insert func(convID, sessID, role, content string, ts time.Time), convID string, started, ended time.Time, n int) *Session {
	t.Helper()
	sess, err := archive.StartSessionAt(convID, started)
	if err != nil {
		t.Fatalf("StartSessionAt: %v", err)
	}
	for i := range n {
		insert(convID, sess.ID, "user", fmt.Sprintf("msg-%d-%s", i, sess.ID), started.Add(time.Duration(i)*time.Minute))
	}
	if err := archive.EndSessionAt(sess.ID, "idle", ended); err != nil {
		t.Fatalf("EndSessionAt: %v", err)
	}
	return sess
}

// olderSessionsFromOutput parses the Older Sessions fenced JSON block.
func olderSessionsFromOutput(t *testing.T, got string) (sessions []SessionView, truncated bool) {
	t.Helper()
	blocks := extractFencedJSON(got)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 fenced JSON block, got %d:\n%s", len(blocks), got)
	}
	var parsed struct {
		Sessions  []SessionView `json:"sessions"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(blocks[0]), &parsed); err != nil {
		t.Fatalf("unmarshal older sessions block: %v", err)
	}
	return parsed.Sessions, parsed.Truncated
}

func TestMessageChannelProvider_NoConversationIDIsSilent(t *testing.T) {
	provider, _, _ := newMessageChannelTestSetup(t)

	got, err := provider.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty output without conversation_id, got %q", got)
	}
}

func TestMessageChannelProvider_EmitsNoVerbatimHistory(t *testing.T) {
	// Stored history reaches the model as role-native messages in its
	// working message list. The provider must not emit a second
	// in-prompt transcript (#1160 finding 1) — only the session
	// catalog.
	provider, archive, insert := newMessageChannelTestSetup(t)
	now := time.Now()

	closedSession(t, archive, insert, "conv-1", now.Add(-48*time.Hour), now.Add(-47*time.Hour), 2)

	ctx := providerCtxWithConvID(context.Background(), "conv-1")
	got, err := provider.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if strings.Contains(got, "## Recent Conversation") {
		t.Errorf("output contains removed Recent Conversation block:\n%s", got)
	}
	if strings.Contains(got, "msg-0-") {
		t.Errorf("output leaked verbatim message content:\n%s", got)
	}
	if !strings.Contains(got, "## Older Sessions") {
		t.Errorf("output missing Older Sessions block:\n%s", got)
	}
}

func TestMessageChannelProvider_OlderSessionsExcludesActiveInWindowAndEmpty(t *testing.T) {
	provider, archive, insert := newMessageChannelTestSetup(t)
	now := time.Now()

	// Active session — messages are in the working list; must not appear.
	if _, err := archive.StartSessionAt("conv-1", now.Add(-1*time.Minute)); err != nil {
		t.Fatalf("StartSessionAt active: %v", err)
	}
	// Closed but ended inside RecentWindow — still in the working list;
	// must not appear.
	inWindow := closedSession(t, archive, insert, "conv-1", now.Add(-25*time.Minute), now.Add(-15*time.Minute), 1)
	// Closed before the window but empty — rotation noise; must not appear.
	empty := closedSession(t, archive, insert, "conv-1", now.Add(-4*time.Hour), now.Add(-3*time.Hour), 0)
	// Closed before the window with substance — the one entry expected.
	older, err := archive.StartSessionAt("conv-1", now.Add(-3*time.Hour))
	if err != nil {
		t.Fatalf("StartSessionAt older: %v", err)
	}
	if err := archive.SetSessionMetadata(older.ID, &SessionMetadata{OneLiner: "freezer alarm"}, "Freezer alarm troubleshooting", []string{"home-automation"}); err != nil {
		t.Fatalf("SetSessionMetadata: %v", err)
	}
	insert("conv-1", older.ID, "user", "old-msg", now.Add(-2*time.Hour))
	if err := archive.EndSessionAt(older.ID, "idle", now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("EndSessionAt older: %v", err)
	}

	ctx := providerCtxWithConvID(context.Background(), "conv-1")
	got, err := provider.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	sessions, truncated := olderSessionsFromOutput(t, got)
	if len(sessions) != 1 {
		t.Fatalf("older sessions len = %d, want 1 (in-window %s and empty %s must be excluded):\n%s",
			len(sessions), inWindow.ID, empty.ID, got)
	}
	if sessions[0].ID != older.ID {
		t.Errorf("older session id = %q, want %q", sessions[0].ID, older.ID)
	}
	if truncated {
		t.Errorf("truncated = true, want false for a single entry")
	}
}

func TestMessageChannelProvider_CapsSessionsNewestFirst(t *testing.T) {
	provider, archive, insert := newMessageChannelTestSetup(t)
	now := time.Now()

	// Eight closed non-empty sessions, oldest first. Default limit is 5.
	var ids []string
	for i := range 8 {
		started := now.Add(-time.Duration(30-i) * time.Hour)
		sess := closedSession(t, archive, insert, "conv-1", started, started.Add(30*time.Minute), 1)
		ids = append(ids, sess.ID)
	}

	ctx := providerCtxWithConvID(context.Background(), "conv-1")
	got, err := provider.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	sessions, truncated := olderSessionsFromOutput(t, got)
	if len(sessions) != 5 {
		t.Fatalf("older sessions len = %d, want 5:\n%s", len(sessions), got)
	}
	// Newest first: the last five created, in reverse creation order.
	for i, want := range []string{ids[7], ids[6], ids[5], ids[4], ids[3]} {
		if sessions[i].ID != want {
			t.Errorf("sessions[%d].ID = %q, want %q", i, sessions[i].ID, want)
		}
	}
	if !truncated {
		t.Errorf("truncated = false, want true when the limit drops sessions")
	}
}

func TestMessageChannelProvider_AllSessionsFilteredIsSilent(t *testing.T) {
	provider, archive, insert := newMessageChannelTestSetup(t)
	now := time.Now()

	// Only rotation noise: an empty closed session and an active one.
	closedSession(t, archive, insert, "conv-1", now.Add(-4*time.Hour), now.Add(-3*time.Hour), 0)
	if _, err := archive.StartSessionAt("conv-1", now.Add(-1*time.Minute)); err != nil {
		t.Fatalf("StartSessionAt active: %v", err)
	}

	ctx := providerCtxWithConvID(context.Background(), "conv-1")
	got, err := provider.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty output when every session is filtered, got:\n%s", got)
	}
}

// extractFencedJSON pulls fenced ```json``` blocks out of s for
// verifying the provider's structured output.
func extractFencedJSON(s string) []string {
	var out []string
	for {
		start := strings.Index(s, "```json\n")
		if start < 0 {
			return out
		}
		s = s[start+len("```json\n"):]
		end := strings.Index(s, "\n```")
		if end < 0 {
			return out
		}
		out = append(out, s[:end])
		s = s[end+len("\n```"):]
	}
}

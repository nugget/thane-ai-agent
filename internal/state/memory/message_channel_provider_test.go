package memory

import (
	"context"
	"encoding/json"
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

func TestMessageChannelProvider_EmitsBothBlocks(t *testing.T) {
	provider, archive, insert := newMessageChannelTestSetup(t)
	now := time.Now()

	// Closed older session 2 days ago — should appear in Older Sessions.
	older, err := archive.StartSessionAt("conv-1", now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("StartSessionAt older: %v", err)
	}
	if err := archive.EndSessionAt(older.ID, "idle", now.Add(-47*time.Hour)); err != nil {
		t.Fatalf("EndSessionAt older: %v", err)
	}
	insert("conv-1", older.ID, "user", "ancient", now.Add(-47*time.Hour))

	// Recent CLOSED session whose tail falls in the verbatim window.
	recent, err := archive.StartSessionAt("conv-1", now.Add(-25*time.Minute))
	if err != nil {
		t.Fatalf("StartSessionAt recent: %v", err)
	}
	if err := archive.EndSessionAt(recent.ID, "idle", now.Add(-15*time.Minute)); err != nil {
		t.Fatalf("EndSessionAt recent: %v", err)
	}
	insert("conv-1", recent.ID, "user", "recent-1", now.Add(-20*time.Minute))
	insert("conv-1", recent.ID, "assistant", "recent-2", now.Add(-19*time.Minute))

	// Active session with no messages — exists so the provider has
	// something to exclude. Mirrors a fresh conversation turn where
	// the model is about to reply.
	if _, err := archive.StartSessionAt("conv-1", now.Add(-1*time.Minute)); err != nil {
		t.Fatalf("StartSessionAt active: %v", err)
	}

	ctx := providerCtxWithConvID(context.Background(), "conv-1")
	got, err := provider.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(got, "## Recent Conversation") {
		t.Errorf("output missing Recent Conversation block:\n%s", got)
	}
	if !strings.Contains(got, "recent-1") || !strings.Contains(got, "recent-2") {
		t.Errorf("verbatim tail missing recent messages:\n%s", got)
	}
}

func TestMessageChannelProvider_ExcludesActiveSessionMessages(t *testing.T) {
	// Active session messages are already in the model's working
	// message list. The verbatim tail must drop them so the model
	// doesn't see them twice.
	provider, archive, insert := newMessageChannelTestSetup(t)
	now := time.Now()

	// Closed prior session that should appear in the tail.
	prior, err := archive.StartSessionAt("conv-1", now.Add(-25*time.Minute))
	if err != nil {
		t.Fatalf("StartSessionAt prior: %v", err)
	}
	if err := archive.EndSessionAt(prior.ID, "idle", now.Add(-15*time.Minute)); err != nil {
		t.Fatalf("EndSessionAt prior: %v", err)
	}
	insert("conv-1", prior.ID, "user", "prior-msg", now.Add(-20*time.Minute))

	// Active session — these messages should NOT appear in the tail.
	active, err := archive.StartSessionAt("conv-1", now.Add(-2*time.Minute))
	if err != nil {
		t.Fatalf("StartSessionAt active: %v", err)
	}
	insert("conv-1", active.ID, "user", "active-msg", now.Add(-1*time.Minute))

	ctx := providerCtxWithConvID(context.Background(), "conv-1")
	got, err := provider.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(got, "prior-msg") {
		t.Errorf("tail missing prior-session message:\n%s", got)
	}
	if strings.Contains(got, "active-msg") {
		t.Errorf("tail leaked active-session message — duplicates the model's working list:\n%s", got)
	}
}

func TestMessageChannelProvider_OlderSessionsExcludesActiveAndInWindow(t *testing.T) {
	provider, archive, insert := newMessageChannelTestSetup(t)
	now := time.Now()

	// Active session — should not appear in older sessions.
	if _, err := archive.StartSessionAt("conv-1", now.Add(-1*time.Minute)); err != nil {
		t.Fatalf("StartSessionAt active: %v", err)
	}
	// Closed but EndedAt inside the verbatim window — should not appear in older.
	inWindow, err := archive.StartSessionAt("conv-1", now.Add(-25*time.Minute))
	if err != nil {
		t.Fatalf("StartSessionAt inWindow: %v", err)
	}
	if err := archive.EndSessionAt(inWindow.ID, "idle", now.Add(-15*time.Minute)); err != nil {
		t.Fatalf("EndSessionAt inWindow: %v", err)
	}
	insert("conv-1", inWindow.ID, "user", "in-window", now.Add(-20*time.Minute))

	// Closed and EndedAt BEFORE the verbatim window — should appear.
	older, err := archive.StartSessionAt("conv-1", now.Add(-3*time.Hour))
	if err != nil {
		t.Fatalf("StartSessionAt older: %v", err)
	}
	if err := archive.SetSessionMetadata(older.ID, &SessionMetadata{OneLiner: "freezer alarm"}, "Freezer alarm troubleshooting", []string{"home-automation"}); err != nil {
		t.Fatalf("SetSessionMetadata: %v", err)
	}
	if err := archive.EndSessionAt(older.ID, "idle", now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("EndSessionAt older: %v", err)
	}
	insert("conv-1", older.ID, "user", "old-msg", now.Add(-2*time.Hour))

	ctx := providerCtxWithConvID(context.Background(), "conv-1")
	got, err := provider.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(got, "## Older Sessions") {
		t.Fatalf("output missing Older Sessions block:\n%s", got)
	}
	// Parse the JSON in the older sessions block to confirm exactly
	// the right session IDs are present.
	jsonBlocks := extractFencedJSON(got)
	if len(jsonBlocks) < 2 {
		t.Fatalf("expected 2 fenced JSON blocks, got %d:\n%s", len(jsonBlocks), got)
	}
	var olderParsed struct {
		Sessions []SessionView `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(jsonBlocks[len(jsonBlocks)-1]), &olderParsed); err != nil {
		t.Fatalf("unmarshal older block: %v", err)
	}
	if len(olderParsed.Sessions) != 1 {
		t.Fatalf("older sessions len = %d, want 1", len(olderParsed.Sessions))
	}
	if olderParsed.Sessions[0].ID != older.ID {
		t.Errorf("older session id = %q, want %q", olderParsed.Sessions[0].ID, older.ID)
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

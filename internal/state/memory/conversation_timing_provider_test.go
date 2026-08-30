package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

const providerOriginKey providerTestCtxKey = "message_origin"

func providerCtxOrigin(ctx context.Context) string {
	if v, ok := ctx.Value(providerOriginKey).(string); ok {
		return v
	}
	return ""
}

// newTimingTestSetup wires a provider with the conversation-timing
// inputs configured (origin extractor, household timezone, pinned
// clock), against the same production-shape store as the catalog tests.
func newTimingTestSetup(t *testing.T, now time.Time, hints map[string]string) (*MessageChannelProvider, *ArchiveStore, func(id, role, content string, origin any, ts time.Time)) {
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

	provider := NewMessageChannelProvider(archive, providerCtxConvID, MessageChannelProviderConfig{
		Timezone:          "America/Chicago",
		OriginFromContext: providerCtxOrigin,
		HintsFromContext:  func(context.Context) map[string]string { return hints },
	}, nil)
	provider.nowFunc = func() time.Time { return now }

	insert := func(id, role, content string, origin any, ts time.Time) {
		t.Helper()
		if _, err := working.DB().Exec(`
			INSERT INTO messages (id, conversation_id, role, content, timestamp, status, origin)
			VALUES (?, 'signal-15551234567', ?, ?, ?, 'active', ?)
		`, id, role, content, ts, origin); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	return provider, archive, insert
}

func timingCtx(origin string) context.Context {
	ctx := providerCtxWithConvID(context.Background(), "signal-15551234567")
	return context.WithValue(ctx, providerOriginKey, origin)
}

// TestConversationTiming_ActiveContactTurn covers the main interactive
// path: a contact turn inside an ongoing session renders the active
// sentence, skips the second contact row (the current message itself,
// already stored before prompt assembly), and stays clear of the
// previous-conversation clause.
func TestConversationTiming_ActiveContactTurn(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, loc)
	provider, archive, insert := newTimingTestSetup(t, now, nil)

	if _, err := archive.StartSessionAt("signal-15551234567", now.Add(-25*time.Minute)); err != nil {
		t.Fatalf("StartSessionAt: %v", err)
	}
	insert("prev", "user", "earlier message", OriginChannel, now.Add(-2*time.Minute))
	insert("current", "user", "the message being answered", OriginChannel, now)

	out, err := provider.TagContext(timingCtx(OriginChannel), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	want := "You're in an active conversation that's been going on for about 25 minutes; the previous message was about 2 minutes before this one."
	if !strings.Contains(out, "## Conversation Timing\n\n"+want) {
		t.Errorf("timing block missing or wrong.\n got: %q\nwant to contain: %q", out, want)
	}
}

// TestConversationTiming_WakeTurn covers the wake path: the wake prompt
// never counts as contact, the newest contact row is the previous
// contact (no self-skip), the waking loop is named from hints, and the
// archivist's one-liner supplies the previous-conversation topic.
func TestConversationTiming_WakeTurn(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, loc)
	provider, archive, insert := newTimingTestSetup(t, now, map[string]string{"loop_name": "annunciator"})

	sess, err := archive.StartSessionAt("signal-15551234567", now.Add(-4*time.Hour))
	if err != nil {
		t.Fatalf("StartSessionAt: %v", err)
	}
	insert("contact", "user", "last thing they said", OriginChannel, now.Add(-3*time.Hour))
	if err := archive.EndSessionAt(sess.ID, "idle", now.Add(-3*time.Hour)); err != nil {
		t.Fatalf("EndSessionAt: %v", err)
	}
	if err := archive.SetSessionMetadata(sess.ID, &SessionMetadata{OneLiner: "garage lighting circuit planning"}, "", nil); err != nil {
		t.Fatalf("SetSessionMetadata: %v", err)
	}
	insert("wakeprompt", "user", "annunciator wake payload", OriginWake, now)

	out, err := provider.TagContext(timingCtx(OriginWake), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	want := "This turn is an internal wake from the annunciator loop, not a message from them. The previous contact was about 3 hours ago. The previous conversation was earlier today, about garage lighting circuit planning."
	if !strings.Contains(out, "## Conversation Timing\n\n"+want) {
		t.Errorf("timing block missing or wrong.\n got: %q\nwant to contain: %q", out, want)
	}
}

// TestConversationTiming_FirstContactEver covers the self-skip working
// on an empty conversation: the only contact row is the current message,
// so the narrative reports a first conversation instead of measuring a
// gap against the message being answered.
func TestConversationTiming_FirstContactEver(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, loc)
	provider, _, insert := newTimingTestSetup(t, now, nil)

	insert("current", "user", "hello there", OriginChannel, now)

	out, err := provider.TagContext(timingCtx(OriginChannel), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	want := "This is the first conversation on this channel."
	if !strings.Contains(out, "## Conversation Timing\n\n"+want) {
		t.Errorf("timing block missing or wrong.\n got: %q\nwant to contain: %q", out, want)
	}
}

// TestConversationTiming_AnchorsToArrivalNotAssemblyTime pins the
// arrival-anchoring contract: the contact gap is measured to the current
// message's stored arrival time, not to prompt-assembly time. Here
// assembly runs 40 minutes after the message arrived; wall-clock
// anchoring would push a 2-minute gap across the recent window into the
// lull bucket, misnarrating an active exchange as a resumed one.
func TestConversationTiming_AnchorsToArrivalNotAssemblyTime(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	assembly := time.Date(2026, 8, 26, 9, 40, 0, 0, loc)
	arrival := assembly.Add(-40 * time.Minute)
	provider, archive, insert := newTimingTestSetup(t, assembly, nil)

	if _, err := archive.StartSessionAt("signal-15551234567", arrival.Add(-25*time.Minute)); err != nil {
		t.Fatalf("StartSessionAt: %v", err)
	}
	insert("prev", "user", "earlier message", OriginChannel, arrival.Add(-2*time.Minute))
	insert("current", "user", "the message being answered", OriginChannel, arrival)

	out, err := provider.TagContext(timingCtx(OriginChannel), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	want := "You're in an active conversation that's been going on for about 25 minutes; the previous message was about 2 minutes before this one."
	if !strings.Contains(out, "## Conversation Timing\n\n"+want) {
		t.Errorf("timing not anchored to arrival.\n got: %q\nwant to contain: %q", out, want)
	}
}

// TestConversationTiming_OmittedWithoutOriginExtractor pins the
// degradation contract: without an origin extractor the contact/wake
// distinction would be a guess, so the timing block is omitted entirely
// while the catalog still renders.
func TestConversationTiming_OmittedWithoutOriginExtractor(t *testing.T) {
	provider, archive, insert := newMessageChannelTestSetup(t)
	now := time.Now()
	closedSession(t, archive, insert, "conv-1", now.Add(-3*time.Hour), now.Add(-2*time.Hour), 3)

	out, err := provider.TagContext(providerCtxWithConvID(context.Background(), "conv-1"), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if strings.Contains(out, "Conversation Timing") {
		t.Errorf("timing block rendered without an origin extractor:\n%q", out)
	}
	if !strings.Contains(out, "## Older Sessions") {
		t.Errorf("catalog missing when timing is off:\n%q", out)
	}
}

package loop

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// TestBuildMailboxPullInputDeltaGatesAndBudgets exercises the #1221 loop-side
// PullInput mechanics: it delivers only newly-arrived items (delta-gating
// against the wake batch and prior pulls), accumulates delivered items for the
// turn-end ack, and stops pulling once the configurable drain budget is spent —
// the guarantee that a turn under continuous inbound flow still terminates.
func TestBuildMailboxPullInputDeltaGatesAndBudgets(t *testing.T) {
	t.Parallel()

	mailbox := newTestMailbox(t)
	l := mustNew(t, Config{
		Name:               "pull-test",
		Task:               "t",
		MidTurnInputBudget: 2,
	}, Deps{Runner: &noopRunner{}, Mailbox: mailbox})

	ctx := context.Background()
	enq := func(s string) MailboxItem {
		it, err := mailbox.Enqueue(ctx, l.Name(), "test", []byte(s))
		if err != nil {
			t.Fatalf("enqueue %q: %v", s, err)
		}
		return it
	}

	// The wake batch is peeked-and-rendered at wake; the closure must treat it
	// as already delivered.
	wakeBatch := []MailboxItem{enq("wake-1"), enq("wake-2")}

	render := func(_ context.Context, items []MailboxItem) []llm.Message {
		out := make([]llm.Message, 0, len(items))
		for _, it := range items {
			out = append(out, llm.Message{Role: "user", Content: string(it.Payload)})
		}
		return out
	}

	var pulled []MailboxItem
	pull := l.buildMailboxPullInput(wakeBatch, &pulled, render)

	// Nothing new beyond the wake batch → nil (never re-present delivered).
	if got := pull(ctx); got != nil {
		t.Fatalf("first pull = %+v, want nil (wake batch delta-gated)", got)
	}

	enq("mid-1")
	enq("mid-2")

	// Budget pull #1: both fresh items, in order.
	got := pull(ctx)
	if len(got) != 2 || got[0].Content != "mid-1" || got[1].Content != "mid-2" {
		t.Fatalf("second pull = %+v, want [mid-1 mid-2]", got)
	}
	if len(pulled) != 2 {
		t.Fatalf("ack accumulator = %d, want 2 after first delivering pull", len(pulled))
	}

	// Re-poll with nothing new → nil (delta-gated against prior pulls).
	if got := pull(ctx); got != nil {
		t.Fatalf("third pull = %+v, want nil (already delivered)", got)
	}

	// Budget pull #2.
	enq("mid-3")
	if got := pull(ctx); len(got) != 1 || got[0].Content != "mid-3" {
		t.Fatalf("fourth pull = %+v, want [mid-3]", got)
	}

	// Budget spent (2 delivering pulls): further arrivals do not extend the
	// turn — they ride the post-turn re-wake instead.
	enq("mid-4")
	if got := pull(ctx); got != nil {
		t.Fatalf("fifth pull = %+v, want nil (drain budget exhausted)", got)
	}

	// Everything delivered mid-turn is accumulated for the single turn-end ack
	// (mid-1, mid-2, mid-3) — the budget-blocked mid-4 is not.
	if len(pulled) != 3 {
		t.Fatalf("ack accumulator = %d, want 3 (mid-1, mid-2, mid-3)", len(pulled))
	}
}

// TestBuildMailboxPullInputDefaultBudget confirms the zero-value budget falls
// back to the package default rather than blocking all pulls.
func TestBuildMailboxPullInputDefaultBudget(t *testing.T) {
	t.Parallel()

	l := mustNew(t, Config{Name: "pull-default", Task: "t"},
		Deps{Runner: &noopRunner{}, Mailbox: newTestMailbox(t)})
	if got := l.midTurnInputBudget(); got != defaultMidTurnInputBudget {
		t.Fatalf("midTurnInputBudget() = %d, want default %d", got, defaultMidTurnInputBudget)
	}
}

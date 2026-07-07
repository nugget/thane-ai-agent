package loop

import (
	"context"
	"fmt"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
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
	pull := l.buildMailboxPullInput("conv-test", wakeBatch, &pulled, render)

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

// TestBuildMailboxPullInputCapsItemsPerPull confirms a single pull injects at
// most maxMidTurnItemsPerPull items even with a larger backlog, so prompt size
// and render/store work stay bounded; the remainder rides the next pull.
func TestBuildMailboxPullInputCapsItemsPerPull(t *testing.T) {
	t.Parallel()

	mailbox := newTestMailbox(t)
	l := mustNew(t, Config{Name: "pull-cap", Task: "t", MidTurnInputBudget: 8},
		Deps{Runner: &noopRunner{}, Mailbox: mailbox})
	ctx := context.Background()

	render := func(_ context.Context, items []MailboxItem) []llm.Message {
		out := make([]llm.Message, len(items))
		for i, it := range items {
			out[i] = llm.Message{Role: "user", Content: string(it.Payload)}
		}
		return out
	}
	var pulled []MailboxItem
	pull := l.buildMailboxPullInput("conv-test", nil, &pulled, render)

	overflow := 3
	total := maxMidTurnItemsPerPull + overflow
	for i := 0; i < total; i++ {
		if _, err := mailbox.Enqueue(ctx, l.Name(), "test", []byte(fmt.Sprintf("m%02d", i))); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	first := pull(ctx)
	if len(first) != maxMidTurnItemsPerPull {
		t.Fatalf("first pull delivered %d, want per-pull cap %d", len(first), maxMidTurnItemsPerPull)
	}
	// FIFO: the oldest items came first.
	if first[0].Content != "m00" {
		t.Errorf("first item = %q, want oldest (m00)", first[0].Content)
	}

	second := pull(ctx)
	if len(second) != overflow {
		t.Fatalf("second pull delivered %d, want the %d remainder", len(second), overflow)
	}
	if len(pulled) != total {
		t.Fatalf("ack accumulator = %d, want %d (all delivered across pulls)", len(pulled), total)
	}
}

// TestBuildMailboxPullInputPublishesMidTurnEvent verifies that a delivering
// pull emits a loop_midturn_input event (#1230) with Source=loop so it reaches
// /v1/loops/events and the archive, and that an empty pull publishes nothing.
func TestBuildMailboxPullInputPublishesMidTurnEvent(t *testing.T) {
	t.Parallel()

	bus := events.New()
	ch := bus.Subscribe(8)
	defer bus.Unsubscribe(ch)

	mailbox := newTestMailbox(t)
	l := mustNew(t, Config{Name: "pull-evt", Task: "t", MidTurnInputBudget: 4},
		Deps{Runner: &noopRunner{}, Mailbox: mailbox, EventBus: bus})
	ctx := context.Background()

	render := func(_ context.Context, items []MailboxItem) []llm.Message {
		out := make([]llm.Message, len(items))
		for i, it := range items {
			out[i] = llm.Message{Role: "user", Content: string(it.Payload)}
		}
		return out
	}
	var pulled []MailboxItem
	pull := l.buildMailboxPullInput("conv-42", nil, &pulled, render)

	if _, err := mailbox.Enqueue(ctx, l.Name(), "test", []byte("hello")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if got := pull(ctx); len(got) != 1 {
		t.Fatalf("pull delivered %d, want 1", len(got))
	}

	// Bus.Publish is synchronous (it sends to subscriber channels in the
	// caller's goroutine), so a delivering pull has already buffered the event
	// by the time it returns — a non-blocking receive suffices, no waiting.
	select {
	case evt := <-ch:
		if evt.Kind != events.KindLoopMidTurnInput {
			t.Fatalf("event kind = %q, want %q", evt.Kind, events.KindLoopMidTurnInput)
		}
		if evt.Source != events.SourceLoop {
			t.Errorf("event source = %q, want loop (so /v1/loops/events forwards it)", evt.Source)
		}
		if evt.Data["conversation_id"] != "conv-42" {
			t.Errorf("conversation_id = %v, want conv-42", evt.Data["conversation_id"])
		}
		if evt.Data["count"] != 1 {
			t.Errorf("count = %v, want 1", evt.Data["count"])
		}
		if evt.Data["loop_name"] != "pull-evt" {
			t.Errorf("loop_name = %v, want pull-evt", evt.Data["loop_name"])
		}
	default:
		t.Fatal("no loop_midturn_input event published for a delivering pull")
	}

	// A pull that delivers nothing must not publish an event.
	if got := pull(ctx); got != nil {
		t.Fatalf("second pull delivered %v, want nil (nothing new)", got)
	}
	select {
	case evt := <-ch:
		t.Fatalf("unexpected event on an empty pull: %+v", evt)
	default:
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

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestLoopSelfContextProvider_RendersCurrentLoop(t *testing.T) {
	state := "processing"
	view := loop.LoopView{Name: "ego", Operation: "service", State: &state, Eligible: true}
	p := NewLoopSelfContextProvider(func(id string) (loop.LoopView, bool) {
		if id == "lp_ego" {
			return view, true
		}
		return loop.LoopView{}, false
	})

	// With the loop_id in context, the block renders for that loop.
	ctx := loop.WithLoopIDForTest(context.Background(), "lp_ego")
	out, err := p.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(out, "### This loop") || !strings.Contains(out, "ego · service") {
		t.Errorf("expected the ego loop's self-context block, got %q", out)
	}

	// No loop_id in context (e.g. a delegate or ad-hoc turn) → empty, not an error.
	if empty, _ := p.TagContext(context.Background(), agentctx.ContextRequest{}); empty != "" {
		t.Errorf("no loop id should render empty, got %q", empty)
	}

	// An unknown loop id (not yet live) → empty.
	unknownCtx := loop.WithLoopIDForTest(context.Background(), "lp_missing")
	if empty, _ := p.TagContext(unknownCtx, agentctx.ContextRequest{}); empty != "" {
		t.Errorf("unknown loop id should render empty, got %q", empty)
	}

	// Self-context is live runtime state.
	if p.TagContextBucket() != agentctx.ContextBucketLiveState {
		t.Errorf("bucket = %v, want live_state", p.TagContextBucket())
	}
}

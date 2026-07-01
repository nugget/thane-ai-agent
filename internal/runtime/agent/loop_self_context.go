package agent

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// LoopSelfContextProvider injects a running loop's own canonical view into its
// system prompt each iteration (#1106 B3), so a loop sees its id/state/parent/
// intent/cadence/effective-tags without a loop_status tool call. It resolves the
// current loop from the loop_id the runtime stamps into the iteration context
// (loop.Loop.run), so a single registered provider serves every non-container
// loop. Suppressed for delegate runs like every always-on provider.
type LoopSelfContextProvider struct {
	viewByID func(loopID string) (loop.LoopView, bool)
}

// NewLoopSelfContextProvider builds the provider. viewByID resolves a live
// loop_id to its canonical LoopView — the app wires this over the loop registry
// and definition view — returning ok=false for an unknown or not-yet-live id.
func NewLoopSelfContextProvider(viewByID func(loopID string) (loop.LoopView, bool)) *LoopSelfContextProvider {
	return &LoopSelfContextProvider{viewByID: viewByID}
}

// TagContextBucket places the self-context under the live-state bucket — it is
// the loop's current runtime state.
func (p *LoopSelfContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

// TagContext renders the current loop's self-context block, or "" when there is
// no resolvable loop — a delegate run, or a turn with no loop_id in context.
func (p *LoopSelfContextProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	if p == nil || p.viewByID == nil {
		return "", nil
	}
	loopID := loop.LoopIDFromContext(ctx)
	if loopID == "" {
		return "", nil
	}
	v, ok := p.viewByID(loopID)
	if !ok {
		return "", nil
	}
	return v.SelfContextMarkdown(), nil
}

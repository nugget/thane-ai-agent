package loop

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// TurnInput is the loop wake context passed to a [TurnBuilder]. It is
// intentionally about the wake, not about the final agent request: the
// loop still applies routing, tool, progress, and accounting defaults
// after the builder returns.
type TurnInput struct {
	// Event is the payload returned by Config.WaitFunc for this wake,
	// or nil for timer-driven loops.
	Event any

	// Supervisor reports whether this wake should use supervisor
	// routing defaults.
	Supervisor bool

	// NotifyEnvelopes are one-shot notification envelopes queued for
	// this wake. Task-based turns render them into the prompt; custom
	// builders may inspect or ignore them.
	NotifyEnvelopes []messages.Envelope
}

// AgentTurn is an agent request prepared by loop-adjacent code for the
// loop runtime to execute. The request is not yet final; the runtime
// still merges loop defaults, launch overrides, progress callbacks,
// tool filters, and carried capability tags before calling the runner.
type AgentTurn struct {
	// Request is the model-facing request prepared for this wake before
	// loop-level request defaults are applied.
	Request Request

	// Summary is compact operator context copied onto the iteration
	// snapshot and completion event. Values should stay small because
	// they travel through dashboard/event payloads.
	Summary map[string]any
}

// TurnBuilder prepares model-facing work from a loop wake. A nil
// AgentTurn with nil error means the wake produced no work and should
// be treated as [ErrNoOp]. Builders should observe and prepare; the
// loop runtime owns execution, telemetry, snapshots, and completion.
type TurnBuilder func(ctx context.Context, input TurnInput) (*AgentTurn, error)

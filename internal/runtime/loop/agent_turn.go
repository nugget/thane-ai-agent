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
// loop runtime to execute. It is the handoff point between wake-specific
// adapters and the common runner path: builders describe the work, while
// the loop still applies defaults, launch overrides, progress callbacks,
// tool filters, fallback content, and carried capability tags before
// invoking the runner.
type AgentTurn struct {
	// Request is the model-facing request prepared for this wake before
	// loop-level defaults and overrides are applied. Builders should set
	// only the request fields they actually own.
	Request Request

	// RunContext is an optional caller-owned cancellation context for the
	// runner invocation. The loop merges it with the iteration context so
	// request/reply callers, such as HTTP handlers, can cancel model work
	// on disconnect without owning loop lifecycle or registration.
	RunContext context.Context

	// Stream receives raw runner stream events for this turn. The loop
	// still wires its own progress callback separately, so Stream is for
	// caller-facing delivery such as HTTP token streaming rather than
	// dashboard telemetry.
	Stream StreamCallback

	// ResultSink receives the runner response and error when preparation
	// or execution finishes. It is called synchronously from the loop
	// goroutine, so implementations should return promptly; a buffered
	// channel send is the usual pattern for synchronous request/reply
	// ingress paths.
	ResultSink TurnResultSink

	// Summary is compact operator context copied onto the iteration
	// snapshot and completion event. Values should stay small because
	// they travel through dashboard/event payloads.
	Summary map[string]any
}

// TurnResultSink receives the model response and error for a single
// loop-owned turn. On request-preparation failures the response may be
// nil; on runner failures it may contain partial metadata returned by
// the runner.
type TurnResultSink func(resp *Response, err error)

// TurnBuilder prepares model-facing work from a loop wake. A nil
// AgentTurn with nil error means the wake produced no work and should
// be treated as [ErrNoOp]. Builders should observe and prepare; the
// loop runtime owns execution, telemetry, snapshots, and completion.
type TurnBuilder func(ctx context.Context, input TurnInput) (*AgentTurn, error)

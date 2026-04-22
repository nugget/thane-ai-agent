package tools

import (
	"fmt"
	"log/slog"
)

// Provider is the uniform contract for subsystems that contribute
// tools to the registry. One subsystem owns one Provider (awareness,
// mqtt, signal, documents, …), and registration flows through
// [Registry.RegisterProvider].
//
// # Why Provider exists
//
// Before Provider, every new subsystem landed via a bespoke
// SetX/ConfigureX method on Registry, wired into some init phase,
// and sometimes required a deferredTools exemption when its handler
// bound asynchronously. The pattern worked but proliferated as
// subsystems grew, and it produced the init-order drift class
// described in #733.
//
// Provider replaces that pattern with a single method per subsystem.
// Tool declarations live with the subsystem (in
// internal/<subsystem>/provider.go), not in internal/tools/, so
// ownership is obvious and subsystems do not reach into Registry
// internals.
//
// # Async-binding pattern
//
// Tools whose handlers depend on a runtime that starts
// asynchronously (signal-cli, a WebSocket connection, a
// slow-initializing client) should *still* be declared at init time.
// The handler should check readiness internally and return
// [ErrUnavailable] until the runtime is ready. This keeps the tool
// visible to capability-tag resolution from the start, eliminating
// the "deferredTools" escape hatch that existed pre-#733:
//
//	func (p *signalProvider) Tools() []*Tool {
//	    return []*Tool{{
//	        Name: "signal_send_message",
//	        ...
//	        Handler: func(ctx context.Context, args map[string]any) (string, error) {
//	            p.mu.RLock(); c := p.client; p.mu.RUnlock()
//	            if c == nil {
//	                return "", ErrUnavailable{
//	                    Tool:   "signal_send_message",
//	                    Reason: "signal-cli not connected",
//	                }
//	            }
//	            return sendMessage(ctx, c, args)
//	        },
//	    }}
//	}
//
// # Migration status
//
// Not every existing subsystem has been migrated yet. New subsystems
// (iMessage, Calendar, Notes, container orchestration, …) should use
// Provider from day one. Remaining SetX/ConfigureX migrations are
// tracked as follow-ups to #733.
type Provider interface {
	// Name is a stable identifier for logging and deduplication. It
	// does not have to match any tool name; typical values are the
	// owning subsystem ("awareness", "mqtt", "signal").
	Name() string

	// Tools returns the tool declarations this provider contributes.
	// Each returned tool must have a non-nil Handler — providers
	// signal "runtime not ready" by returning [ErrUnavailable] from
	// the handler, not by omitting the tool from this list.
	Tools() []*Tool
}

// ErrUnavailable is the canonical error returned by a Provider's
// handler when the tool is declared but its backing runtime is not
// currently ready to serve invocations. Capability-tag resolution and
// the model-facing manifest treat declared-but-unavailable tools as
// present; only invocation fails with this error.
type ErrUnavailable struct {
	// Tool is the name of the tool that was invoked.
	Tool string
	// Reason is a short human-readable explanation suitable for
	// surfacing to the model or operator (e.g., "signal-cli not
	// connected", "mqtt broker not yet configured").
	Reason string
}

// Error implements the error interface with a consistent shape so
// callers can scan logs for unavailable-tool invocations.
func (e ErrUnavailable) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("tool %q is declared but not currently available", e.Tool)
	}
	return fmt.Sprintf("tool %q is declared but not currently available: %s", e.Tool, e.Reason)
}

// RegisterProvider registers every tool contributed by p through the
// standard [Registry.Register] path. Tools with nil handlers are
// rejected — Provider handlers must be non-nil even when the backing
// runtime is unavailable (return [ErrUnavailable] instead).
//
// Registration is idempotent at the tool-name level: re-registering
// the same name replaces the prior tool. This matches the existing
// Register semantics; providers should not contribute duplicate names
// across subsystems.
func (r *Registry) RegisterProvider(p Provider) {
	if p == nil {
		return
	}
	declared := p.Tools()
	for _, t := range declared {
		if t == nil {
			continue
		}
		if t.Handler == nil {
			slog.Default().Warn("tool provider contributed a nil handler; skipping",
				"provider", p.Name(), "tool", t.Name)
			continue
		}
		r.Register(t)
	}
}

package loop

import "context"

type loopIDKey struct{}
type fallbackContentKey struct{}
type bindingsKey struct{}

// withLoopID injects the loop ID into the run context so downstream
// code (e.g. handlers, turn builders, the agent runner, tool calls,
// and delegate launches) can discover which loop triggered the current
// execution. The loop's own run() applies this at the top of its
// goroutine so the loop's identity dominates over anything inherited
// from the spawning context — important when a loop is spawned from
// inside another loop's handler context.
func withLoopID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, loopIDKey{}, id)
}

func withFallbackContent(ctx context.Context, content string) context.Context {
	if content == "" {
		return ctx
	}
	return context.WithValue(ctx, fallbackContentKey{}, content)
}

// LoopIDFromContext extracts the originating loop ID from any context
// derived from a loop's run goroutine — handler, turn builder, agent
// runner, tool call, or delegate launch contexts all qualify. Returns
// an empty string when called from a context that did not originate
// inside a loop.
func LoopIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(loopIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithLoopIDForTest exposes [withLoopID] for tests in other
// packages that need to drive a context through a function which
// reads the loop ID (e.g. context-provider tests asserting
// behavior at iteration time). Production code outside this
// package should not need this; the loop's own run goroutine
// stamps the ID before any downstream code runs.
func WithLoopIDForTest(ctx context.Context, id string) context.Context {
	return withLoopID(ctx, id)
}

// FallbackContent returns the loop-configured response fallback from a
// handler context. Handler-backed interactive loops can pass this through
// to nested agent.Run calls and use it as a last-resort post-run reply.
func FallbackContent(ctx context.Context) string {
	if content, ok := ctx.Value(fallbackContentKey{}).(string); ok {
		return content
	}
	return ""
}

// WithBindings stamps the turn's resource bindings onto a context, so
// a subsystem can discover which instance of a shared resource this
// caller is scoped to. Nil or empty bindings return the context
// unchanged, which is what leaves unbound callers — interactive turns,
// API requests, anything outside a bound loop — behaving exactly as
// they did before any binding existed.
//
// It lives here rather than in the tools package because the
// subsystems that must honor a binding sit below tools in the import
// graph; the loop package is the common ancestor that already carries
// the loop's identity to the same places.
//
// The transport is deliberately indifferent to who declared the
// binding. Today a loop spec is the only writer, but a channel binding
// or a contact-trust policy could stamp the same key without any
// subsystem below needing to learn a second mechanism.
func WithBindings(ctx context.Context, bindings map[string]string) context.Context {
	if len(bindings) == 0 {
		return ctx
	}
	return context.WithValue(ctx, bindingsKey{}, CloneBindings(bindings))
}

// BindingsFromContext returns every binding on the context, or nil
// when the caller is unbound.
func BindingsFromContext(ctx context.Context) map[string]string {
	bindings, ok := ctx.Value(bindingsKey{}).(map[string]string)
	if !ok {
		return nil
	}
	return CloneBindings(bindings)
}

// BindingFromContext returns the value bound to key, or "" when the
// caller is unbound for that key. Subsystems read their own key and
// ignore the rest.
func BindingFromContext(ctx context.Context, key string) string {
	bindings, ok := ctx.Value(bindingsKey{}).(map[string]string)
	if !ok {
		return ""
	}
	return bindings[key]
}

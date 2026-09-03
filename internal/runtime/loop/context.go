package loop

import "context"

type loopIDKey struct{}
type conversationIDKey struct{}
type fallbackContentKey struct{}
type bindingsKey struct{}

// withConversationID stamps the wake's conversation ID onto the run
// context alongside the loop ID. Every wake mints a fresh conversation,
// so anything keyed by "which model consciousness saw this" — the hidden
// document read receipts above all — needs the pair, and needs it on the
// contexts that run BEFORE the agent turn as well as on the tool calls
// inside it: the output context that renders a loop's own documents into
// its prompt is built on this context, and the receipt it records has to
// land under the same scope the generated output tool will look in.
func withConversationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, conversationIDKey{}, id)
}

// ConversationIDFromContext returns the conversation the current wake
// runs under, or "" from a context that did not originate inside a
// loop wake. The tools package reads this as its fallback when its own
// conversation key is absent, so a turn-builder context and a tool-call
// context resolve to one document revision scope.
func ConversationIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(conversationIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithConversationIDForTest exposes [withConversationID] for tests in
// other packages that drive a turn-builder context; see
// [WithLoopIDForTest].
func WithConversationIDForTest(ctx context.Context, id string) context.Context {
	return withConversationID(ctx, id)
}

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

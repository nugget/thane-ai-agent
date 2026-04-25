package loop

import "context"

type loopIDKey struct{}
type fallbackContentKey struct{}

// withLoopID injects the loop ID into the handler context so downstream
// code (e.g. agent loop → delegate executor) can discover which loop
// triggered the current execution.
func withLoopID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, loopIDKey{}, id)
}

func withFallbackContent(ctx context.Context, content string) context.Context {
	if content == "" {
		return ctx
	}
	return context.WithValue(ctx, fallbackContentKey{}, content)
}

// LoopIDFromContext extracts the originating loop ID from a handler
// context. Returns an empty string if the context was not created by
// a loop handler dispatch.
func LoopIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(loopIDKey{}).(string); ok {
		return id
	}
	return ""
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

package loop

import "context"

type loopIDKey struct{}

// withLoopID injects the loop ID into the handler context so downstream
// code (e.g. agent loop → delegate executor) can discover which loop
// triggered the current execution.
func withLoopID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, loopIDKey{}, id)
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

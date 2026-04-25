package loop

import "context"

type progressFuncKey struct{}

// ProgressFunc returns the loop's progress callback from the handler
// context, or nil if not available. Handler implementations that
// dispatch LLM calls can use this to forward in-flight events (tool
// calls, LLM responses) to the event bus for dashboard visibility.
//
// The returned function has signature func(kind string, data map[string]any)
// where kind is an events.KindLoop* constant and data carries the
// event payload.
func ProgressFunc(ctx context.Context) func(string, map[string]any) {
	if fn, ok := ctx.Value(progressFuncKey{}).(func(string, map[string]any)); ok {
		return fn
	}
	return nil
}

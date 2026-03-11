package loop

import "context"

type iterSummaryKey struct{}

// IterationSummary returns the summary map for the current iteration
// from the handler context, or nil if not available. Handler
// implementations call this to report per-iteration metrics to the
// dashboard timeline. Values should be small scalars (int, string,
// bool) to keep SSE payloads compact.
func IterationSummary(ctx context.Context) map[string]any {
	if m, ok := ctx.Value(iterSummaryKey{}).(map[string]any); ok {
		return m
	}
	return nil
}

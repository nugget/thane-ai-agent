package tools

import "context"

type toolExecutionScopeKey struct{}

// A composite tool must honor the same filtered surface as its caller.
// Snapshot names instead of retaining the registry: later executions may
// have different tags or exclusions, and must not change an earlier scope.
func withToolExecutionScope(ctx context.Context, registry *Registry) context.Context {
	available := make(map[string]struct{}, len(registry.tools))
	for name := range registry.tools {
		available[name] = struct{}{}
	}
	return context.WithValue(ctx, toolExecutionScopeKey{}, available)
}

// Missing execution scope grants nothing. Checking the global registry here
// would let a composite tool expose sources excluded from the current run.
func toolAvailableInExecution(ctx context.Context, name string) bool {
	available, _ := ctx.Value(toolExecutionScopeKey{}).(map[string]struct{})
	_, ok := available[name]
	return ok
}

package iterate

import "context"

// ToolExecutor runs a single tool call. Implementations control
// timeout enforcement and error wrapping.
type ToolExecutor interface {
	Execute(ctx context.Context, name, argsJSON string) (string, error)
}

// DirectExecutor calls Execute on the underlying function directly.
// This is the default executor used by the agent loop.
type DirectExecutor struct {
	Exec func(ctx context.Context, name, argsJSON string) (string, error)
}

// Execute implements [ToolExecutor].
func (d *DirectExecutor) Execute(ctx context.Context, name, argsJSON string) (string, error) {
	return d.Exec(ctx, name, argsJSON)
}

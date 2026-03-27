package iterate

import (
	"context"
	"errors"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/logging"
)

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

// DeadlineExecutor wraps tool execution in a goroutine with deadline
// enforcement. If the handler does not respect context cancellation,
// the goroutine leaks but the caller is unblocked. This is the
// executor used by the delegate system for per-tool timeouts.
type DeadlineExecutor struct {
	Exec func(ctx context.Context, name, argsJSON string) (string, error)
}

// Execute implements [ToolExecutor].
func (d *DeadlineExecutor) Execute(ctx context.Context, name, argsJSON string) (string, error) {
	type result struct {
		value string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		v, e := d.Exec(ctx, name, argsJSON)
		ch <- result{v, e}
	}()
	select {
	case r := <-ch:
		return r.value, r.err
	case <-ctx.Done():
		logging.Logger(ctx).Warn("tool handler did not respect context cancellation; goroutine leaked",
			"tool", name)
		err := ctx.Err()
		reason := "ended due to context error"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timed out"
		case errors.Is(err, context.Canceled):
			reason = "canceled"
		}
		return "", fmt.Errorf("tool %s %s: %w", name, reason, err)
	}
}

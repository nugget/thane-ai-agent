package loop

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/messages"
)

// SignalLoop delivers one signal envelope to a live loop by ID.
func (r *Registry) SignalLoop(ctx context.Context, id string, env messages.Envelope) (SignalReceipt, error) {
	_ = ctx
	l := r.Get(id)
	if l == nil {
		return SignalReceipt{}, fmt.Errorf("loop %q not found", id)
	}
	return l.enqueueSignal(env)
}

// SignalLoopByName delivers one signal envelope to a live loop by exact name.
func (r *Registry) SignalLoopByName(ctx context.Context, name string, env messages.Envelope) (SignalReceipt, error) {
	_ = ctx
	matches := r.FindByName(name)
	switch len(matches) {
	case 0:
		return SignalReceipt{}, fmt.Errorf("loop named %q not found", name)
	case 1:
		return matches[0].enqueueSignal(env)
	default:
		ids := make([]string, 0, len(matches))
		for _, l := range matches {
			ids = append(ids, l.id)
		}
		return SignalReceipt{}, fmt.Errorf("loop name %q is ambiguous; retry with loop_id from %v", name, ids)
	}
}

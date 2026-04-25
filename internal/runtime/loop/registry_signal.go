package loop

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// NotifyLoop delivers one inter-loop process notification to a live loop by ID.
// This is loop-runtime control messaging, not Signal-channel transport.
func (r *Registry) NotifyLoop(ctx context.Context, id string, env messages.Envelope) (NotifyReceipt, error) {
	if err := ctx.Err(); err != nil {
		return NotifyReceipt{}, err
	}
	l := r.Get(id)
	if l == nil {
		return NotifyReceipt{}, fmt.Errorf("loop %q not found", id)
	}
	return l.enqueueNotify(env)
}

// NotifyLoopByName delivers one inter-loop process notification to a live loop
// by exact name. This is loop-runtime control messaging, not Signal-channel
// transport.
func (r *Registry) NotifyLoopByName(ctx context.Context, name string, env messages.Envelope) (NotifyReceipt, error) {
	if err := ctx.Err(); err != nil {
		return NotifyReceipt{}, err
	}
	matches := r.FindByName(name)
	switch len(matches) {
	case 0:
		return NotifyReceipt{}, fmt.Errorf("loop named %q not found", name)
	case 1:
		return matches[0].enqueueNotify(env)
	default:
		ids := make([]string, 0, len(matches))
		for _, l := range matches {
			ids = append(ids, l.id)
		}
		return NotifyReceipt{}, fmt.Errorf("loop name %q is ambiguous; retry with loop_id from %v", name, ids)
	}
}

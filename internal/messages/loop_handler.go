package messages

import (
	"context"
	"fmt"
)

// LoopNotifyByID delivers one notification envelope to a live loop identified by ID.
type LoopNotifyByID func(context.Context, string, Envelope) (DeliveryResult, error)

// LoopNotifyByName delivers one notification envelope to a live loop identified by exact name.
type LoopNotifyByName func(context.Context, string, Envelope) (DeliveryResult, error)

// LoopHandler routes loop-destination envelopes into the live loop registry.
type LoopHandler struct {
	ByID   LoopNotifyByID
	ByName LoopNotifyByName
}

// Deliver sends one notification envelope to a live loop.
func (h *LoopHandler) Deliver(ctx context.Context, env Envelope) (DeliveryResult, error) {
	if h == nil || (h.ByID == nil && h.ByName == nil) {
		return DeliveryResult{}, fmt.Errorf("loop message route is not configured")
	}
	if env.Type != TypeSignal {
		return DeliveryResult{}, fmt.Errorf("loop route only supports loop notifications (signal envelopes), got %q", env.Type)
	}

	var (
		result DeliveryResult
		err    error
	)
	switch env.To.Selector {
	case SelectorID:
		if h.ByID == nil {
			err = fmt.Errorf("loop-id signaling is not configured")
			break
		}
		result, err = h.ByID(ctx, env.To.Target, env)
	case "", SelectorName:
		if h.ByName == nil {
			err = fmt.Errorf("loop-name signaling is not configured")
			break
		}
		result, err = h.ByName(ctx, env.To.Target, env)
	default:
		err = fmt.Errorf("unsupported loop selector %q", env.To.Selector)
	}
	if err != nil {
		return DeliveryResult{}, err
	}
	if result.Route == "" {
		result.Route = "loop"
	}
	if result.Status == "" {
		result.Status = DeliveryDelivered
	}
	return result, nil
}

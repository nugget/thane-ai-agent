package messages

import (
	"context"
	"testing"
)

func TestLoopHandlerDeliverPreservesQueuedStatus(t *testing.T) {
	t.Parallel()

	handler := &LoopHandler{
		ByName: func(_ context.Context, target string, env Envelope) (DeliveryResult, error) {
			if target != "battery-watch" {
				t.Fatalf("target = %q", target)
			}
			if env.Type != TypeSignal {
				t.Fatalf("type = %q", env.Type)
			}
			return DeliveryResult{
				Status:  DeliveryQueued,
				Details: map[string]any{"queued": true},
			}, nil
		},
	}

	got, err := handler.Deliver(context.Background(), Envelope{
		To: Destination{
			Kind:     DestinationLoop,
			Target:   "battery-watch",
			Selector: SelectorName,
		},
		Type: TypeSignal,
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got.Status != DeliveryQueued {
		t.Fatalf("status = %q, want %q", got.Status, DeliveryQueued)
	}
	if got.Route != "loop" {
		t.Fatalf("route = %q, want loop", got.Route)
	}
}

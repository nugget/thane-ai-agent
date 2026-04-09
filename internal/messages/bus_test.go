package messages

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

type recordingHandler struct {
	env Envelope
}

func (h *recordingHandler) Deliver(_ context.Context, env Envelope) (DeliveryResult, error) {
	h.env = env
	return DeliveryResult{
		Route:  "test",
		Status: DeliveryDelivered,
	}, nil
}

func TestBusSendNormalizesAndRoutes(t *testing.T) {
	t.Parallel()

	bus := NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := &recordingHandler{}
	bus.RegisterRoute(DestinationLoop, handler.Deliver)

	result, err := bus.Send(context.Background(), Envelope{
		From: Identity{Kind: IdentityCore, Name: "core"},
		To: Destination{
			Kind:   DestinationLoop,
			Target: "battery-watch",
		},
		Type: TypeSignal,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.Route != "test" || result.Status != DeliveryDelivered {
		t.Fatalf("result = %#v", result)
	}
	if handler.env.ID == "" {
		t.Fatal("normalized envelope id is empty")
	}
	if handler.env.CreatedAt.IsZero() {
		t.Fatal("normalized envelope created_at is zero")
	}
	if handler.env.To.Selector != SelectorName {
		t.Fatalf("selector = %q, want %q", handler.env.To.Selector, SelectorName)
	}
}

func TestBusSendRejectsMissingRoute(t *testing.T) {
	t.Parallel()

	bus := NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := bus.Send(context.Background(), Envelope{
		From: Identity{Kind: IdentityCore},
		To: Destination{
			Kind:     DestinationLoop,
			Target:   "battery-watch",
			Selector: SelectorName,
		},
		Type: TypeSignal,
	})
	if err == nil || err.Error() == "" {
		t.Fatal("expected missing-route error")
	}
}

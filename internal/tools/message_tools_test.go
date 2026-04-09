package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/messages"
)

type recordingMessageHandler struct {
	env messages.Envelope
}

func (h *recordingMessageHandler) Deliver(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
	h.env = env
	return messages.DeliveryResult{
		Route:  "loop",
		Status: messages.DeliveryDelivered,
	}, nil
}

func TestConfigureMessageToolsRegistersSignalLoop(t *testing.T) {
	t.Parallel()

	bus := messages.NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := &recordingMessageHandler{}
	bus.RegisterRoute(messages.DestinationLoop, handler.Deliver)

	reg := NewEmptyRegistry()
	reg.ConfigureMessageTools(MessageToolDeps{Bus: bus})

	tool := reg.Get("signal_loop")
	if tool == nil {
		t.Fatal("signal_loop tool not registered")
	}
	if _, ok := tool.Parameters["anyOf"]; !ok {
		t.Fatal("signal_loop schema missing anyOf guardrail")
	}

	ctx := WithLoopID(context.Background(), "loop-parent-1")
	ctx = WithHints(ctx, map[string]string{
		"source":    "loop",
		"loop_name": "metacognitive",
	})
	out, err := tool.Handler(ctx, map[string]any{
		"name":             "battery-watch",
		"message":          "Fresh context for the next iteration.",
		"force_supervisor": true,
		"priority":         "urgent",
	})
	if err != nil {
		t.Fatalf("signal_loop: %v", err)
	}

	if handler.env.From.Kind != messages.IdentityLoop || handler.env.From.ID != "loop-parent-1" {
		t.Fatalf("from = %#v, want loop identity", handler.env.From)
	}
	if handler.env.To.Kind != messages.DestinationLoop || handler.env.To.Target != "battery-watch" || handler.env.To.Selector != messages.SelectorName {
		t.Fatalf("to = %#v", handler.env.To)
	}
	payload, err := messagesPayload(handler.env.Payload)
	if err != nil {
		t.Fatalf("messagesPayload: %v", err)
	}
	if payload.Message != "Fresh context for the next iteration." || !payload.ForceSupervisor {
		t.Fatalf("payload = %#v", payload)
	}
	if handler.env.Priority != messages.PriorityUrgent {
		t.Fatalf("priority = %q, want urgent", handler.env.Priority)
	}

	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "ok" {
		t.Fatalf("status = %q, want ok", got.Status)
	}
}

func TestSenderIdentityFromContextDefaultsToConversation(t *testing.T) {
	t.Parallel()

	got := senderIdentityFromContext(context.Background())
	if got.Kind != messages.IdentityCore {
		t.Fatalf("kind = %q, want core", got.Kind)
	}
	if got.Name != "conversation" {
		t.Fatalf("name = %q, want conversation", got.Name)
	}
}

func messagesPayload(raw any) (messages.LoopSignalPayload, error) {
	switch got := raw.(type) {
	case messages.LoopSignalPayload:
		return got, nil
	default:
		return messages.LoopSignalPayload{}, fmt.Errorf("unexpected payload type %T", raw)
	}
}

package tools

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestLoopWakeDeliversMessageAndForceSupervisor(t *testing.T) {
	t.Parallel()

	bus := messages.NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := &recordingMessageHandler{}
	bus.RegisterRoute(messages.DestinationLoop, handler.Deliver)

	loops := looppkg.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		loops.ShutdownAll(shutdownCtx)
	})
	if _, err := loops.SpawnLoop(ctx, looppkg.Config{
		Name:         "tde-msrh-2026-06",
		Task:         "owner-away presence watch",
		SleepDefault: time.Hour,
	}, looppkg.Deps{Runner: &messageToolLoopRunner{}}); err != nil {
		t.Fatalf("SpawnLoop: %v", err)
	}

	reg := NewEmptyRegistry()
	reg.ConfigureLoopRuntimeTools(LoopRuntimeToolDeps{Registry: loops})
	reg.ConfigureMessageTools(MessageToolDeps{Bus: bus})

	tool := reg.Get("loop_wake")
	if tool == nil {
		t.Fatal("loop_wake tool not registered")
	}
	if tool.Core {
		t.Error("loop_wake should be a loops-tagged mechanism tool, not Core")
	}

	out, err := tool.Handler(context.Background(), map[string]any{
		"name":             "tde-msrh-2026-06",
		"message":          "Dan moved a car at 22:00; garage bay 1 cycling is expected, not an anomaly.",
		"force_supervisor": true,
		"priority":         "urgent",
	})
	if err != nil {
		t.Fatalf("loop_wake: %v", err)
	}

	if handler.env.To.Target != "tde-msrh-2026-06" || handler.env.To.Selector != messages.SelectorName {
		t.Fatalf("to = %#v, want name selector for tde-msrh-2026-06", handler.env.To)
	}
	if handler.env.Priority != messages.PriorityUrgent {
		t.Fatalf("priority = %q, want urgent", handler.env.Priority)
	}
	payload, err := messagesPayload(handler.env.Payload)
	if err != nil {
		t.Fatalf("messagesPayload: %v", err)
	}
	if payload.Message == "" || !payload.ForceSupervisor {
		t.Fatalf("payload = %#v, want message + force supervisor", payload)
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

func TestLoopWakeByIDAndRequiresTarget(t *testing.T) {
	t.Parallel()

	bus := messages.NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := &recordingMessageHandler{}
	bus.RegisterRoute(messages.DestinationLoop, handler.Deliver)

	reg := NewEmptyRegistry()
	reg.ConfigureMessageTools(MessageToolDeps{Bus: bus})
	tool := reg.Get("loop_wake")
	if tool == nil {
		t.Fatal("loop_wake not registered")
	}

	// Targeting by loop_id uses the ID selector.
	if _, err := tool.Handler(context.Background(), map[string]any{"loop_id": "lp_abc", "message": "fyi"}); err != nil {
		t.Fatalf("loop_wake by id: %v", err)
	}
	if handler.env.To.Target != "lp_abc" || handler.env.To.Selector != messages.SelectorID {
		t.Fatalf("to = %#v, want id selector for lp_abc", handler.env.To)
	}

	// Neither loop_id nor name is an error, not a silent no-op.
	if _, err := tool.Handler(context.Background(), map[string]any{"message": "hi"}); err == nil {
		t.Fatal("expected error when neither loop_id nor name is provided")
	}
}

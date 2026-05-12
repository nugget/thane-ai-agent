package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
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

type messageToolLoopRunner struct{}

func (r *messageToolLoopRunner) Run(_ context.Context, _ looppkg.Request, _ looppkg.StreamCallback) (*looppkg.Response, error) {
	return &looppkg.Response{Content: "ok", Model: "test-model"}, nil
}

func TestConfigureMessageToolsRegistersNotifyLoop(t *testing.T) {
	t.Parallel()

	bus := messages.NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := &recordingMessageHandler{}
	bus.RegisterRoute(messages.DestinationLoop, handler.Deliver)

	reg := NewEmptyRegistry()
	reg.ConfigureMessageTools(MessageToolDeps{Bus: bus})

	tool := reg.Get("notify_loop")
	if tool == nil {
		t.Fatal("notify_loop tool not registered")
	}
	if _, ok := tool.Parameters["anyOf"]; !ok {
		t.Fatal("notify_loop schema missing anyOf guardrail")
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
		t.Fatalf("notify_loop: %v", err)
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

func TestRequestCoreAttentionTargetsOwnerChannel(t *testing.T) {
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
	loopID, err := loops.SpawnLoop(ctx, looppkg.Config{
		Name:         "signal/owner",
		Task:         "watch the owner channel",
		SleepDefault: time.Hour,
		Metadata: map[string]string{
			"category": "channel",
			"is_owner": "true",
		},
	}, looppkg.Deps{Runner: &messageToolLoopRunner{}})
	if err != nil {
		t.Fatalf("SpawnLoop: %v", err)
	}

	reg := NewEmptyRegistry()
	reg.ConfigureLoopRuntimeTools(LoopRuntimeToolDeps{Registry: loops})
	reg.ConfigureMessageTools(MessageToolDeps{Bus: bus})

	tool := reg.Get("request_core_attention")
	if tool == nil {
		t.Fatal("request_core_attention tool not registered")
	}
	if !tool.AlwaysAvailable {
		t.Fatal("request_core_attention should be always available")
	}

	callCtx := WithHints(context.Background(), map[string]string{"source": "delegate"})
	out, err := tool.Handler(callCtx, map[string]any{
		"concern":          "The watcher saw a safety concern.",
		"suggested_action": "Consider notifying the owner after checking timing.",
		"context":          "Triggered from metacognitive review.",
		"priority":         "urgent",
	})
	if err != nil {
		t.Fatalf("request_core_attention: %v", err)
	}

	if handler.env.From.Kind != messages.IdentityDelegate {
		t.Fatalf("from = %#v, want delegate identity", handler.env.From)
	}
	if handler.env.To.Target != loopID || handler.env.To.Selector != messages.SelectorID {
		t.Fatalf("to = %#v, want loop ID %q", handler.env.To, loopID)
	}
	if handler.env.Priority != messages.PriorityUrgent {
		t.Fatalf("priority = %q, want urgent", handler.env.Priority)
	}
	if len(handler.env.Scope) != 1 || handler.env.Scope[0] != "core_attention" {
		t.Fatalf("scope = %#v, want core_attention", handler.env.Scope)
	}
	payload, err := messagesPayload(handler.env.Payload)
	if err != nil {
		t.Fatalf("messagesPayload: %v", err)
	}
	if payload.Kind != "core_attention_request" || payload.Concern != "The watcher saw a safety concern." {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.SuggestedAction == "" || payload.Context == "" || !payload.ForceSupervisor {
		t.Fatalf("payload = %#v, want suggested_action, context, and force supervisor", payload)
	}

	var got struct {
		Status string `json:"status"`
		Target struct {
			LoopID   string `json:"loop_id"`
			LoopName string `json:"loop_name"`
			Reason   string `json:"reason"`
		} `json:"target"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "ok" || got.Target.LoopID != loopID || got.Target.LoopName != "signal/owner" || got.Target.Reason != "recent_owner_channel" {
		t.Fatalf("result = %#v", got)
	}
}

func TestResolveCoreAttentionTargetPrefersExplicitMetadata(t *testing.T) {
	t.Parallel()

	loops := looppkg.NewRegistry()
	runner := &messageToolLoopRunner{}
	owner, err := looppkg.New(looppkg.Config{
		Name: "signal/owner",
		Task: "owner channel",
		Metadata: map[string]string{
			"category": "channel",
			"is_owner": "true",
		},
	}, looppkg.Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New owner: %v", err)
	}
	core, err := looppkg.New(looppkg.Config{
		Name: "core-attention",
		Task: "core attention",
		Metadata: map[string]string{
			"core_attention_target": "true",
		},
	}, looppkg.Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New core: %v", err)
	}
	if err := loops.Register(owner); err != nil {
		t.Fatalf("Register owner: %v", err)
	}
	if err := loops.Register(core); err != nil {
		t.Fatalf("Register core: %v", err)
	}

	reg := NewEmptyRegistry()
	reg.ConfigureLoopRuntimeTools(LoopRuntimeToolDeps{Registry: loops})
	target, err := reg.resolveCoreAttentionTarget()
	if err != nil {
		t.Fatalf("resolveCoreAttentionTarget: %v", err)
	}
	if target.LoopID != core.ID() || target.LoopName != "core-attention" || target.Reason != "metadata_core_attention_target" {
		t.Fatalf("target = %#v, want explicit core loop", target)
	}
	if target.LastActive != nil {
		t.Fatalf("LastActive = %v, want nil for unstarted loop", target.LastActive)
	}
	blob, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("Marshal target: %v", err)
	}
	if strings.Contains(string(blob), "last_active") {
		t.Fatalf("target JSON = %s, want last_active omitted when unknown", blob)
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

func messagesPayload(raw any) (messages.LoopNotifyPayload, error) {
	switch got := raw.(type) {
	case messages.LoopNotifyPayload:
		return got, nil
	default:
		return messages.LoopNotifyPayload{}, fmt.Errorf("unexpected payload type %T", raw)
	}
}

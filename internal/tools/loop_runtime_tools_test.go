package tools

import (
	"context"
	"encoding/json"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/memory"
)

type noopLoopRunner struct{}

func (noopLoopRunner) Run(context.Context, looppkg.Request, looppkg.StreamCallback) (*looppkg.Response, error) {
	return &looppkg.Response{Content: "ok", Model: "test-model"}, nil
}

type testLoopRuntimeDeps struct {
	reg        *Registry
	live       *looppkg.Registry
	lastLaunch looppkg.Launch
}

func newTestLoopRuntimeDeps(t *testing.T) *testLoopRuntimeDeps {
	t.Helper()

	live := looppkg.NewRegistry(looppkg.WithMaxLoops(3))
	runner := noopLoopRunner{}
	loopA, err := looppkg.New(looppkg.Config{
		Name:       "battery_watch",
		Task:       "Watch batteries.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Metadata:   map[string]string{"category": "observer"},
	}, looppkg.Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New(loopA): %v", err)
	}
	if err := live.Register(loopA); err != nil {
		t.Fatalf("Register(loopA): %v", err)
	}

	loopB, err := looppkg.New(looppkg.Config{
		Name:       "mqtt_bridge",
		Task:       "Bridge MQTT events.",
		Operation:  looppkg.OperationBackgroundTask,
		Completion: looppkg.CompletionChannel,
	}, looppkg.Deps{Runner: runner})
	if err != nil {
		t.Fatalf("New(loopB): %v", err)
	}
	if err := live.Register(loopB); err != nil {
		t.Fatalf("Register(loopB): %v", err)
	}

	reg := NewEmptyRegistry()
	deps := &testLoopRuntimeDeps{reg: reg, live: live}
	reg.ConfigureLoopRuntimeTools(LoopRuntimeToolDeps{
		Registry: live,
		LaunchLoop: func(_ context.Context, launch looppkg.Launch) (looppkg.LaunchResult, error) {
			deps.lastLaunch = launch
			return looppkg.LaunchResult{
				LoopID:    "loop-launch-123",
				Operation: launch.Spec.Operation,
				Detached:  launch.Spec.Operation != looppkg.OperationRequestReply,
			}, nil
		},
	})
	return deps
}

func TestConfigureLoopRuntimeTools_RegistersTools(t *testing.T) {
	deps := newTestLoopRuntimeDeps(t)
	for _, name := range []string{"loop_status", "spawn_loop", "stop_loop"} {
		if deps.reg.Get(name) == nil {
			t.Fatalf("%s tool not registered", name)
		}
	}
}

func TestLoopStatusFiltersAndStopLoop(t *testing.T) {
	deps := newTestLoopRuntimeDeps(t)

	out, err := deps.reg.Get("loop_status").Handler(context.Background(), map[string]any{
		"operation": "service",
		"query":     "battery",
	})
	if err != nil {
		t.Fatalf("loop_status: %v", err)
	}

	var got struct {
		Status            string           `json:"status"`
		ActiveCount       int              `json:"active_count"`
		MaxLoops          int              `json:"max_loops"`
		RemainingCapacity int              `json:"remaining_capacity"`
		Loops             []map[string]any `json:"loops"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal loop_status: %v", err)
	}
	if got.Status != "ok" {
		t.Fatalf("status = %q, want ok", got.Status)
	}
	if got.ActiveCount != 2 || got.MaxLoops != 3 || got.RemainingCapacity != 1 {
		t.Fatalf("counts = %#v, want active=2 max=3 remaining=1", got)
	}
	if len(got.Loops) != 1 || got.Loops[0]["name"] != "battery_watch" {
		t.Fatalf("loops = %#v, want battery_watch only", got.Loops)
	}
	if _, ok := got.Loops[0]["config"]; ok {
		t.Fatalf("loop_status should not return full config in compact view: %#v", got.Loops[0])
	}

	stopOut, err := deps.reg.Get("stop_loop").Handler(context.Background(), map[string]any{
		"name": "battery_watch",
	})
	if err != nil {
		t.Fatalf("stop_loop: %v", err)
	}
	var stopped struct {
		Status string         `json:"status"`
		Loop   looppkg.Status `json:"loop"`
	}
	if err := json.Unmarshal([]byte(stopOut), &stopped); err != nil {
		t.Fatalf("unmarshal stop_loop: %v", err)
	}
	if stopped.Loop.Name != "battery_watch" {
		t.Fatalf("stopped loop = %#v, want battery_watch", stopped.Loop)
	}
	if deps.live.ActiveCount() != 1 {
		t.Fatalf("ActiveCount after stop = %d, want 1", deps.live.ActiveCount())
	}
}

func TestSpawnLoopAppliesConversationDefaults(t *testing.T) {
	deps := newTestLoopRuntimeDeps(t)

	ctx := WithConversationID(context.Background(), "conv-123")
	out, err := deps.reg.Get("spawn_loop").Handler(ctx, map[string]any{
		"launch": map[string]any{
			"spec": map[string]any{
				"name":       "ad-hoc-check",
				"task":       "Check the batteries once.",
				"operation":  "background_task",
				"completion": "conversation",
			},
		},
	})
	if err != nil {
		t.Fatalf("spawn_loop: %v", err)
	}

	var got struct {
		Status     string               `json:"status"`
		Result     looppkg.LaunchResult `json:"result"`
		Completion map[string]any       `json:"completion"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal spawn_loop: %v", err)
	}
	if got.Result.LoopID != "loop-launch-123" {
		t.Fatalf("result = %#v, want loop-launch-123", got.Result)
	}
	if deps.lastLaunch.CompletionConversationID != "conv-123" {
		t.Fatalf("CompletionConversationID = %q, want conv-123", deps.lastLaunch.CompletionConversationID)
	}
	if deps.lastLaunch.ChannelBinding != nil {
		t.Fatalf("ChannelBinding = %#v, want nil without channel context", deps.lastLaunch.ChannelBinding)
	}
	if got.Completion["mode"] != "conversation" {
		t.Fatalf("completion mode = %#v, want conversation", got.Completion)
	}
}

func TestSpawnLoopRequiresLaunch(t *testing.T) {
	deps := newTestLoopRuntimeDeps(t)

	if _, err := deps.reg.Get("spawn_loop").Handler(context.Background(), map[string]any{}); err == nil || err.Error() != "launch is required" {
		t.Fatalf("spawn_loop missing launch err = %v, want launch is required", err)
	}
}

func TestSpawnLoopInfersChannelCompletionFromSignalContext(t *testing.T) {
	deps := newTestLoopRuntimeDeps(t)

	ctx := WithConversationID(context.Background(), "signal-15551234567")
	ctx = WithChannelBinding(ctx, &memory.ChannelBinding{
		Channel: "signal",
		Address: "+15551234567",
	})
	out, err := deps.reg.Get("spawn_loop").Handler(ctx, map[string]any{
		"launch": map[string]any{
			"spec": map[string]any{
				"name":      "signal-detached",
				"task":      "Research and report back later.",
				"operation": "background_task",
			},
		},
	})
	if err != nil {
		t.Fatalf("spawn_loop: %v", err)
	}

	var got struct {
		Status     string               `json:"status"`
		Result     looppkg.LaunchResult `json:"result"`
		Completion struct {
			Mode           looppkg.Completion               `json:"mode"`
			ConversationID string                           `json:"conversation_id"`
			Channel        *looppkg.CompletionChannelTarget `json:"channel"`
			Inferred       bool                             `json:"inferred"`
			Warnings       []string                         `json:"warnings"`
		} `json:"completion"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal spawn_loop: %v", err)
	}
	if deps.lastLaunch.Spec.Completion != looppkg.CompletionChannel {
		t.Fatalf("Completion = %q, want channel", deps.lastLaunch.Spec.Completion)
	}
	if deps.lastLaunch.CompletionChannel == nil || deps.lastLaunch.CompletionChannel.Channel != "signal" || deps.lastLaunch.CompletionChannel.Recipient != "+15551234567" {
		t.Fatalf("CompletionChannel = %#v", deps.lastLaunch.CompletionChannel)
	}
	if !got.Completion.Inferred || got.Completion.Mode != looppkg.CompletionChannel {
		t.Fatalf("completion = %#v, want inferred channel", got.Completion)
	}
}

func TestSpawnLoopWarnsWhenSignalContextUsesConversationCompletion(t *testing.T) {
	deps := newTestLoopRuntimeDeps(t)

	ctx := WithConversationID(context.Background(), "signal-15551234567")
	ctx = WithChannelBinding(ctx, &memory.ChannelBinding{
		Channel: "signal",
		Address: "+15551234567",
	})
	out, err := deps.reg.Get("spawn_loop").Handler(ctx, map[string]any{
		"launch": map[string]any{
			"spec": map[string]any{
				"name":       "signal-conversation",
				"task":       "Research and keep me posted here.",
				"operation":  "background_task",
				"completion": "conversation",
			},
		},
	})
	if err != nil {
		t.Fatalf("spawn_loop: %v", err)
	}

	var got struct {
		Completion struct {
			Mode           looppkg.Completion `json:"mode"`
			ConversationID string             `json:"conversation_id"`
			Inferred       bool               `json:"inferred"`
			Warnings       []string           `json:"warnings"`
		} `json:"completion"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal spawn_loop: %v", err)
	}
	if got.Completion.Mode != looppkg.CompletionConversation || got.Completion.Inferred {
		t.Fatalf("completion = %#v, want explicit conversation", got.Completion)
	}
	if len(got.Completion.Warnings) == 0 {
		t.Fatalf("warnings = %#v, want channel guidance warning", got.Completion.Warnings)
	}
}

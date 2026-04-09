package tools

import (
	"context"
	"encoding/json"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
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
		Outputs: []looppkg.OutputTarget{
			{Kind: looppkg.OutputTargetDocumentJournal, Ref: "generated:batteries/daily.md"},
		},
		Metadata: map[string]string{"category": "observer"},
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
		Outputs: []looppkg.OutputTarget{
			{Kind: looppkg.OutputTargetMQTTTopic, Topic: "thane/test/loops"},
		},
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
		Loops             []looppkg.Status `json:"loops"`
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
	if len(got.Loops) != 1 || got.Loops[0].Name != "battery_watch" {
		t.Fatalf("loops = %#v, want battery_watch only", got.Loops)
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
		Status string               `json:"status"`
		Result looppkg.LaunchResult `json:"result"`
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
}

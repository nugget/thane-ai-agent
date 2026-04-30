package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/scheduler"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

type mockTaskLauncher struct {
	launch *looppkg.Launch
	deps   *looppkg.Deps
	result looppkg.LaunchResult
	err    error
}

func (m *mockTaskLauncher) Launch(_ context.Context, launch looppkg.Launch, deps looppkg.Deps) (looppkg.LaunchResult, error) {
	capturedLaunch := launch
	capturedLaunch.Metadata = cloneTestStringMap(launch.Metadata)
	capturedLaunch.Hints = cloneTestStringMap(launch.Hints)
	capturedLaunch.ExcludeTools = append([]string(nil), launch.ExcludeTools...)
	capturedLaunch.InitialTags = append([]string(nil), launch.InitialTags...)
	capturedLaunch.Spec.Metadata = cloneTestStringMap(launch.Spec.Metadata)
	capturedLaunch.Spec.ExcludeTools = append([]string(nil), launch.Spec.ExcludeTools...)
	capturedLaunch.Spec.Profile.ExcludeTools = append([]string(nil), launch.Spec.Profile.ExcludeTools...)
	capturedLaunch.Spec.Profile.ExtraHints = cloneTestStringMap(launch.Spec.Profile.ExtraHints)
	m.launch = &capturedLaunch
	capturedDeps := deps
	m.deps = &capturedDeps
	return m.result, m.err
}

func cloneTestStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

type stubLoopRunner struct{}

func (stubLoopRunner) Run(context.Context, looppkg.Request, looppkg.StreamCallback) (*looppkg.Response, error) {
	return &looppkg.Response{}, nil
}

func TestRunScheduledTask_WakePayload(t *testing.T) {
	launcher := &mockTaskLauncher{
		result: looppkg.LaunchResult{
			LoopID:   "loop-sched",
			Response: &looppkg.Response{Content: "I checked the sensors."},
		},
	}

	task := &scheduler.Task{
		ID:   "task-1",
		Name: "Heartbeat",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{"message": "Check sensors and report."},
		},
	}
	exec := &scheduler.Execution{ID: "exec-aaa"}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{
		launch: launcher.Launch,
		runner: stubLoopRunner{},
		logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if launcher.launch == nil {
		t.Fatal("launcher.Launch was not called")
	}

	if launcher.launch.Task != "Check sensors and report." {
		t.Errorf("Task = %q, want %q", launcher.launch.Task, "Check sensors and report.")
	}
	if launcher.launch.Spec.Name != "scheduler:Heartbeat" {
		t.Errorf("Spec.Name = %q, want %q", launcher.launch.Spec.Name, "scheduler:Heartbeat")
	}
	if launcher.launch.Spec.Operation != looppkg.OperationRequestReply {
		t.Errorf("Spec.Operation = %q, want %q", launcher.launch.Spec.Operation, looppkg.OperationRequestReply)
	}
	if launcher.launch.Spec.Task == "" {
		t.Error("Spec.Task should provide a non-empty validation baseline")
	}
	if launcher.launch.Spec.Profile.ExtraHints["source"] != "scheduler" {
		t.Errorf("hint source = %q, want %q", launcher.launch.Spec.Profile.ExtraHints["source"], "scheduler")
	}
	if launcher.launch.Spec.Profile.ExtraHints["task"] != "Heartbeat" {
		t.Errorf("hint task = %q, want %q", launcher.launch.Spec.Profile.ExtraHints["task"], "Heartbeat")
	}
	if launcher.launch.Spec.Profile.LocalOnly != "true" {
		t.Errorf("hint local_only = %q, want %q", launcher.launch.Spec.Profile.LocalOnly, "true")
	}
	if launcher.launch.Spec.Profile.QualityFloor != "1" {
		t.Errorf("hint quality_floor = %q, want %q", launcher.launch.Spec.Profile.QualityFloor, "1")
	}
	if launcher.launch.Spec.Profile.Mission != "automation" {
		t.Errorf("hint mission = %q, want %q", launcher.launch.Spec.Profile.Mission, "automation")
	}
	if launcher.launch.Spec.Profile.DelegationGating != "disabled" {
		t.Errorf("hint delegation_gating = %q, want %q", launcher.launch.Spec.Profile.DelegationGating, "disabled")
	}
	if launcher.launch.ConversationID != "sched-task-1-exec-aaa" {
		t.Errorf("ConversationID = %q, want %q", launcher.launch.ConversationID, "sched-task-1-exec-aaa")
	}
	if launcher.launch.Metadata["execution_id"] != "exec-aaa" {
		t.Errorf("execution_id = %q, want %q", launcher.launch.Metadata["execution_id"], "exec-aaa")
	}
	if launcher.launch.Spec.Metadata["task_id"] != "task-1" {
		t.Errorf("task_id = %q, want %q", launcher.launch.Spec.Metadata["task_id"], "task-1")
	}
	if launcher.launch.UsageRole != "scheduler" {
		t.Errorf("UsageRole = %q, want %q", launcher.launch.UsageRole, "scheduler")
	}
	if launcher.deps == nil || launcher.deps.Runner == nil {
		t.Fatal("launch deps should include a loop runner")
	}
	if exec.Result != "I checked the sensors." {
		t.Errorf("exec.Result = %q, want %q", exec.Result, "I checked the sensors.")
	}
}

func TestRunScheduledTask_DefaultMessage(t *testing.T) {
	launcher := &mockTaskLauncher{
		result: looppkg.LaunchResult{Response: &looppkg.Response{Content: "ok"}},
	}

	task := &scheduler.Task{
		ID:   "task-2",
		Name: "Morning Check",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{}, // No message
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{
		launch: launcher.Launch,
		runner: stubLoopRunner{},
		logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if launcher.launch.Task != "Scheduled wake: Morning Check" {
		t.Errorf("default message = %q, want %q", launcher.launch.Task, "Scheduled wake: Morning Check")
	}
}

func TestRunScheduledTask_NilData(t *testing.T) {
	launcher := &mockTaskLauncher{
		result: looppkg.LaunchResult{Response: &looppkg.Response{Content: "ok"}},
	}

	task := &scheduler.Task{
		ID:   "task-3",
		Name: "Nightly",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			// Data is nil
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{
		launch: launcher.Launch,
		runner: stubLoopRunner{},
		logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if launcher.launch.Task != "Scheduled wake: Nightly" {
		t.Errorf("message = %q, want %q", launcher.launch.Task, "Scheduled wake: Nightly")
	}
}

func TestRunScheduledTask_UnsupportedPayload(t *testing.T) {
	launcher := &mockTaskLauncher{}

	task := &scheduler.Task{
		ID:   "task-4",
		Name: "Webhook Task",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWebhook,
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{
		launch: launcher.Launch,
		runner: stubLoopRunner{},
		logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("unsupported payload should return nil, got %v", err)
	}

	if launcher.launch != nil {
		t.Error("launcher should not be called for unsupported payload kinds")
	}
}

func TestRunScheduledTask_LauncherError(t *testing.T) {
	launcher := &mockTaskLauncher{
		err: errors.New("launch unavailable"),
	}

	task := &scheduler.Task{
		ID:   "task-5",
		Name: "Failing Task",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{"message": "test"},
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{
		launch: launcher.Launch,
		runner: stubLoopRunner{},
		logger: slog.Default(),
	})
	if err == nil {
		t.Fatal("expected error when launcher fails")
	}
	if !errors.Is(err, launcher.err) {
		t.Errorf("error = %v, want wrapped %v", err, launcher.err)
	}
}

func TestRunScheduledTask_PayloadModelOverride(t *testing.T) {
	launcher := &mockTaskLauncher{
		result: looppkg.LaunchResult{Response: &looppkg.Response{Content: "reflected"}},
	}

	task := &scheduler.Task{
		ID:   "task-model",
		Name: "ModelOverride",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{
				"message":       "think deeply",
				"model":         "claude-sonnet-4-20250514",
				"local_only":    "false",
				"quality_floor": "7",
			},
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{
		launch: launcher.Launch,
		runner: stubLoopRunner{},
		logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if launcher.launch.Spec.Profile.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", launcher.launch.Spec.Profile.Model, "claude-sonnet-4-20250514")
	}
	if launcher.launch.Spec.Profile.LocalOnly != "false" {
		t.Errorf("hint local_only = %q, want %q", launcher.launch.Spec.Profile.LocalOnly, "false")
	}
	if launcher.launch.Spec.Profile.QualityFloor != "7" {
		t.Errorf("hint quality_floor = %q, want %q", launcher.launch.Spec.Profile.QualityFloor, "7")
	}
}

func TestRunScheduledTask_PayloadPartialOverride(t *testing.T) {
	launcher := &mockTaskLauncher{
		result: looppkg.LaunchResult{Response: &looppkg.Response{Content: "ok"}},
	}

	task := &scheduler.Task{
		ID:   "task-partial",
		Name: "PartialOverride",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{
				"message":       "check something",
				"quality_floor": "5",
				// model and local_only not set — should use defaults
			},
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{
		launch: launcher.Launch,
		runner: stubLoopRunner{},
		logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Model should be empty (no override).
	if launcher.launch.Spec.Profile.Model != "" {
		t.Errorf("model = %q, want empty", launcher.launch.Spec.Profile.Model)
	}
	// local_only should default to "true".
	if launcher.launch.Spec.Profile.LocalOnly != "true" {
		t.Errorf("hint local_only = %q, want %q", launcher.launch.Spec.Profile.LocalOnly, "true")
	}
	// quality_floor should use the override.
	if launcher.launch.Spec.Profile.QualityFloor != "5" {
		t.Errorf("hint quality_floor = %q, want %q", launcher.launch.Spec.Profile.QualityFloor, "5")
	}
}

func TestRunScheduledTask_UsesContextDeadlineAsRunTimeout(t *testing.T) {
	launcher := &mockTaskLauncher{
		result: looppkg.LaunchResult{Response: &looppkg.Response{Content: "ok"}},
	}

	task := &scheduler.Task{
		ID:   "task-timeout",
		Name: "Timed",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{"message": "bounded"},
		},
	}
	exec := &scheduler.Execution{ID: "exec-timeout"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	err := runScheduledTask(ctx, task, exec, taskExecDeps{
		launch: launcher.Launch,
		runner: stubLoopRunner{},
		logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if launcher.launch == nil {
		t.Fatal("launcher.Launch was not called")
	}
	if launcher.launch.RunTimeout <= 0 {
		t.Fatalf("RunTimeout = %v, want > 0", launcher.launch.RunTimeout)
	}
	if launcher.launch.RunTimeout > 2*time.Minute {
		t.Fatalf("RunTimeout = %v, want <= 2m", launcher.launch.RunTimeout)
	}
}

func TestBuildScheduledTaskLoopProfile(t *testing.T) {
	tests := []struct {
		name string
		task *scheduler.Task
		want router.LoopProfile
	}{
		{
			name: "defaults",
			task: &scheduler.Task{
				Name: "Heartbeat",
				Payload: scheduler.Payload{
					Kind: scheduler.PayloadWake,
				},
			},
			want: router.LoopProfile{
				LocalOnly:        "true",
				QualityFloor:     "1",
				Mission:          "automation",
				DelegationGating: "disabled",
				ExtraHints: map[string]string{
					"source": "scheduler",
					"task":   "Heartbeat",
				},
			},
		},
		{
			name: "payload overrides",
			task: &scheduler.Task{
				Name: "Custom",
				Payload: scheduler.Payload{
					Kind: scheduler.PayloadWake,
					Data: map[string]any{
						"model":         "claude-sonnet-4-20250514",
						"local_only":    "false",
						"quality_floor": "7",
					},
				},
			},
			want: router.LoopProfile{
				Model:            "claude-sonnet-4-20250514",
				LocalOnly:        "false",
				QualityFloor:     "7",
				Mission:          "automation",
				DelegationGating: "disabled",
				ExtraHints: map[string]string{
					"source": "scheduler",
					"task":   "Custom",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildScheduledTaskLoopProfile(tt.task)

			if got.Model != tt.want.Model {
				t.Fatalf("Model = %q, want %q", got.Model, tt.want.Model)
			}
			if got.LocalOnly != tt.want.LocalOnly {
				t.Fatalf("LocalOnly = %q, want %q", got.LocalOnly, tt.want.LocalOnly)
			}
			if got.QualityFloor != tt.want.QualityFloor {
				t.Fatalf("QualityFloor = %q, want %q", got.QualityFloor, tt.want.QualityFloor)
			}
			if got.Mission != tt.want.Mission {
				t.Fatalf("Mission = %q, want %q", got.Mission, tt.want.Mission)
			}
			if got.DelegationGating != tt.want.DelegationGating {
				t.Fatalf("DelegationGating = %q, want %q", got.DelegationGating, tt.want.DelegationGating)
			}
			if got.ExtraHints["source"] != "scheduler" {
				t.Fatalf("ExtraHints[source] = %q, want %q", got.ExtraHints["source"], "scheduler")
			}
			if got.ExtraHints["task"] != tt.task.Name {
				t.Fatalf("ExtraHints[task] = %q, want %q", got.ExtraHints["task"], tt.task.Name)
			}
		})
	}
}

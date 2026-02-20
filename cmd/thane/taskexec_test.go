package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
)

// mockRunner records calls to Run and returns a canned response.
type mockRunner struct {
	req  *agent.Request
	resp *agent.Response
	err  error
}

func (m *mockRunner) Run(_ context.Context, req *agent.Request, _ agent.StreamCallback) (*agent.Response, error) {
	m.req = req
	return m.resp, m.err
}

func TestRunScheduledTask_WakePayload(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "I checked the sensors."},
	}

	task := &scheduler.Task{
		ID:   "task-1",
		Name: "Heartbeat",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{"message": "Check sensors and report."},
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the runner was called.
	if runner.req == nil {
		t.Fatal("runner.Run was not called")
	}

	// Verify the message was passed through.
	if len(runner.req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(runner.req.Messages))
	}
	if runner.req.Messages[0].Content != "Check sensors and report." {
		t.Errorf("message = %q, want %q", runner.req.Messages[0].Content, "Check sensors and report.")
	}
	if runner.req.Messages[0].Role != "user" {
		t.Errorf("role = %q, want %q", runner.req.Messages[0].Role, "user")
	}

	// Verify trigger-profile hints.
	if runner.req.Hints["source"] != "scheduler" {
		t.Errorf("hint source = %q, want %q", runner.req.Hints["source"], "scheduler")
	}
	if runner.req.Hints["task"] != "Heartbeat" {
		t.Errorf("hint task = %q, want %q", runner.req.Hints["task"], "Heartbeat")
	}
	if runner.req.Hints[router.HintLocalOnly] != "true" {
		t.Errorf("hint local_only = %q, want %q", runner.req.Hints[router.HintLocalOnly], "true")
	}
	if runner.req.Hints[router.HintQualityFloor] != "1" {
		t.Errorf("hint quality_floor = %q, want %q", runner.req.Hints[router.HintQualityFloor], "1")
	}
	if runner.req.Hints[router.HintMission] != "automation" {
		t.Errorf("hint mission = %q, want %q", runner.req.Hints[router.HintMission], "automation")
	}
	if runner.req.Hints[router.HintDelegationGating] != "disabled" {
		t.Errorf("hint delegation_gating = %q, want %q", runner.req.Hints[router.HintDelegationGating], "disabled")
	}

	// Scheduled tasks should use an isolated conversation ID.
	if runner.req.ConversationID != "sched-task-1" {
		t.Errorf("ConversationID = %q, want %q", runner.req.ConversationID, "sched-task-1")
	}

	// Verify execution result was populated.
	if exec.Result != "I checked the sensors." {
		t.Errorf("exec.Result = %q, want %q", exec.Result, "I checked the sensors.")
	}
}

func TestRunScheduledTask_DefaultMessage(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok"},
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

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if runner.req.Messages[0].Content != "Scheduled wake: Morning Check" {
		t.Errorf("default message = %q, want %q", runner.req.Messages[0].Content, "Scheduled wake: Morning Check")
	}
}

func TestRunScheduledTask_NilData(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok"},
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

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if runner.req.Messages[0].Content != "Scheduled wake: Nightly" {
		t.Errorf("message = %q, want %q", runner.req.Messages[0].Content, "Scheduled wake: Nightly")
	}
}

func TestRunScheduledTask_UnsupportedPayload(t *testing.T) {
	runner := &mockRunner{}

	task := &scheduler.Task{
		ID:   "task-4",
		Name: "Webhook Task",
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWebhook,
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default()})
	if err != nil {
		t.Fatalf("unsupported payload should return nil, got %v", err)
	}

	// Runner should NOT have been called.
	if runner.req != nil {
		t.Error("runner.Run should not be called for unsupported payload kinds")
	}
}

func TestRunScheduledTask_RunnerError(t *testing.T) {
	runner := &mockRunner{
		err: errors.New("LLM unavailable"),
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

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default()})
	if err == nil {
		t.Fatal("expected error when runner fails")
	}
	if !errors.Is(err, runner.err) {
		t.Errorf("error = %v, want wrapped %v", err, runner.err)
	}
}

func TestRunScheduledTask_PeriodicReflection(t *testing.T) {
	// Create a workspace with an ego.md file.
	workspace := t.TempDir()
	egoContent := "# My Reflections\n\nI notice the lights change at sunset."
	if err := os.WriteFile(filepath.Join(workspace, "ego.md"), []byte(egoContent), 0644); err != nil {
		t.Fatalf("write ego.md: %v", err)
	}

	runner := &mockRunner{
		resp: &agent.Response{Content: "Updated ego.md"},
	}

	task := &scheduler.Task{
		ID:   "task-reflect",
		Name: periodicReflectionTaskName,
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{"message": "periodic_reflection"},
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default(), workspacePath: workspace})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The message should contain the reflection prompt with ego.md content.
	msg := runner.req.Messages[0].Content
	if !strings.Contains(msg, "periodic reflection") {
		t.Error("message should contain reflection prompt text")
	}
	if !strings.Contains(msg, egoContent) {
		t.Error("message should contain current ego.md content")
	}
	if strings.Contains(msg, "does not exist yet") {
		t.Error("message should not contain first-run placeholder when ego.md exists")
	}
}

func TestRunScheduledTask_PeriodicReflection_NoEgoFile(t *testing.T) {
	// Workspace exists but ego.md does not.
	workspace := t.TempDir()

	runner := &mockRunner{
		resp: &agent.Response{Content: "Created ego.md"},
	}

	task := &scheduler.Task{
		ID:   "task-reflect-new",
		Name: periodicReflectionTaskName,
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{"message": "periodic_reflection"},
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default(), workspacePath: workspace})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg := runner.req.Messages[0].Content
	if !strings.Contains(msg, "does not exist yet") {
		t.Error("message should contain first-run placeholder when ego.md is missing")
	}
	if !strings.Contains(msg, "periodic reflection") {
		t.Error("message should still contain reflection prompt text")
	}
}

func TestRunScheduledTask_PeriodicReflection_NoWorkspace(t *testing.T) {
	// No workspace path — falls through to raw payload message.
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok"},
	}

	task := &scheduler.Task{
		ID:   "task-reflect-nows",
		Name: periodicReflectionTaskName,
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{"message": "periodic_reflection"},
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Without a workspace, the raw message should be used.
	msg := runner.req.Messages[0].Content
	if msg != "periodic_reflection" {
		t.Errorf("message = %q, want raw payload %q", msg, "periodic_reflection")
	}
}

func TestRunScheduledTask_PayloadModelOverride(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "reflected"},
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

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if runner.req.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", runner.req.Model, "claude-sonnet-4-20250514")
	}
	if runner.req.Hints[router.HintLocalOnly] != "false" {
		t.Errorf("hint local_only = %q, want %q", runner.req.Hints[router.HintLocalOnly], "false")
	}
	if runner.req.Hints[router.HintQualityFloor] != "7" {
		t.Errorf("hint quality_floor = %q, want %q", runner.req.Hints[router.HintQualityFloor], "7")
	}
}

func TestRunScheduledTask_PayloadPartialOverride(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok"},
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

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{runner: runner, logger: slog.Default()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Model should be empty (no override).
	if runner.req.Model != "" {
		t.Errorf("model = %q, want empty", runner.req.Model)
	}
	// local_only should default to "true".
	if runner.req.Hints[router.HintLocalOnly] != "true" {
		t.Errorf("hint local_only = %q, want %q", runner.req.Hints[router.HintLocalOnly], "true")
	}
	// quality_floor should use the override.
	if runner.req.Hints[router.HintQualityFloor] != "5" {
		t.Errorf("hint quality_floor = %q, want %q", runner.req.Hints[router.HintQualityFloor], "5")
	}
}

func TestRunScheduledTask_EmailPoll_NilPoller(t *testing.T) {
	// When emailPoller is nil, the email_poll task should fall through
	// to a normal wake with the payload message.
	runner := &mockRunner{
		resp: &agent.Response{Content: "checked email"},
	}

	task := &scheduler.Task{
		ID:   "task-email",
		Name: emailPollTaskName,
		Payload: scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{
				"message":    "Check for new email",
				"local_only": "false",
			},
		},
	}
	exec := &scheduler.Execution{}

	err := runScheduledTask(context.Background(), task, exec, taskExecDeps{
		runner: runner,
		logger: slog.Default(),
		// emailPoller intentionally nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With nil poller, it should fall through to normal wake.
	if runner.req == nil {
		t.Fatal("runner.Run should have been called")
	}
	if runner.req.Messages[0].Content != "Check for new email" {
		t.Errorf("message = %q, want raw payload", runner.req.Messages[0].Content)
	}
}

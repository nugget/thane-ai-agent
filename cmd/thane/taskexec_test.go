package main

import (
	"context"
	"errors"
	"log/slog"
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

	err := runScheduledTask(context.Background(), task, exec, runner, slog.Default())
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

	err := runScheduledTask(context.Background(), task, exec, runner, slog.Default())
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

	err := runScheduledTask(context.Background(), task, exec, runner, slog.Default())
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

	err := runScheduledTask(context.Background(), task, exec, runner, slog.Default())
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

	err := runScheduledTask(context.Background(), task, exec, runner, slog.Default())
	if err == nil {
		t.Fatal("expected error when runner fails")
	}
	if !errors.Is(err, runner.err) {
		t.Errorf("error = %v, want wrapped %v", err, runner.err)
	}
}

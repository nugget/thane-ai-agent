package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
)

// agentRunner abstracts the agent loop for task execution testing.
type agentRunner interface {
	Run(ctx context.Context, req *agent.Request, stream agent.StreamCallback) (*agent.Response, error)
}

// runScheduledTask handles execution of a scheduled task by dispatching
// PayloadWake tasks to the agent loop. Unsupported payload kinds are
// logged and silently ignored (returning nil, not an error).
func runScheduledTask(ctx context.Context, task *scheduler.Task, exec *scheduler.Execution, runner agentRunner, logger *slog.Logger) error {
	logger.Debug("task executing",
		"task_id", task.ID,
		"task_name", task.Name,
		"payload_kind", task.Payload.Kind,
	)

	if task.Payload.Kind != scheduler.PayloadWake {
		logger.Warn("unsupported task payload kind", "kind", task.Payload.Kind)
		return nil
	}

	msg, _ := task.Payload.Data["message"].(string)
	if msg == "" {
		msg = "Scheduled wake: " + task.Name
	}

	// Use trigger-profile hints: cheapest local model, no persona overhead.
	req := &agent.Request{
		Messages: []agent.Message{{Role: "user", Content: msg}},
		Hints: map[string]string{
			"source":                "scheduler",
			"task":                  task.Name,
			router.HintLocalOnly:    "true",
			router.HintQualityFloor: "1",
			router.HintMission:      "automation",
		},
	}

	resp, err := runner.Run(ctx, req, nil)
	if err != nil {
		return fmt.Errorf("scheduled task %q: %w", task.Name, err)
	}
	exec.Result = resp.Content

	logger.Debug("task completed",
		"task_id", task.ID,
		"task_name", task.Name,
		"result_len", len(resp.Content),
	)
	return nil
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
)

// periodicReflectionTaskName is the well-known name for the self-reflection
// scheduled task. Used for startup registration and context injection.
const periodicReflectionTaskName = "periodic_reflection"

// agentRunner abstracts the agent loop for task execution testing.
type agentRunner interface {
	Run(ctx context.Context, req *agent.Request, stream agent.StreamCallback) (*agent.Response, error)
}

// runScheduledTask handles execution of a scheduled task by dispatching
// PayloadWake tasks to the agent loop. Unsupported payload kinds are
// logged and silently ignored (returning nil, not an error).
//
// workspacePath is the agent's sandboxed file system root. When non-empty
// and the task is periodic_reflection, the current ego.md is read from
// the workspace and injected into the reflection prompt.
func runScheduledTask(ctx context.Context, task *scheduler.Task, exec *scheduler.Execution, runner agentRunner, logger *slog.Logger, workspacePath string) error {
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

	// Context injection for periodic_reflection: read ego.md and build
	// the reflection prompt with its current contents.
	if task.Name == periodicReflectionTaskName && workspacePath != "" {
		egoContent := readEgoMD(workspacePath, logger)
		msg = prompts.PeriodicReflectionPrompt(egoContent)
	}

	// Use trigger-profile hints: cheapest local model, full tool access.
	// Each task gets its own conversation to isolate from interactive chat.
	req := &agent.Request{
		ConversationID: fmt.Sprintf("sched-%s", task.ID),
		Messages:       []agent.Message{{Role: "user", Content: msg}},
		Hints: map[string]string{
			"source":                    "scheduler",
			"task":                      task.Name,
			router.HintLocalOnly:        "true",
			router.HintQualityFloor:     "1",
			router.HintMission:          "automation",
			router.HintDelegationGating: "disabled", // full tool access, no delegation indirection
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

// readEgoMD reads the ego.md file from the workspace. Returns an empty
// string if the file does not exist (first reflection creates it).
func readEgoMD(workspacePath string, logger *slog.Logger) string {
	egoPath := filepath.Join(workspacePath, "ego.md")
	data, err := os.ReadFile(egoPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logger.Warn("failed to read ego.md for reflection",
				"path", egoPath,
				"error", err,
			)
		}
		return ""
	}
	return string(data)
}

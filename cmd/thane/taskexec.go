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

const (
	// periodicReflectionTaskName is the well-known name for the self-reflection
	// scheduled task. Used for startup registration and context injection.
	periodicReflectionTaskName = "periodic_reflection"

	// emailPollTaskName is the well-known name for the email polling
	// scheduled task. When it fires, the poller checks IMAP accounts
	// for new messages and wakes the agent only if something arrived.
	emailPollTaskName = "email_poll"
)

// agentRunner abstracts the agent loop for task execution testing.
type agentRunner interface {
	Run(ctx context.Context, req *agent.Request, stream agent.StreamCallback) (*agent.Response, error)
}

// emailChecker abstracts email polling for task execution testing.
// Implemented by *email.Poller.
type emailChecker interface {
	CheckNewMessages(ctx context.Context) (string, error)
}

// taskExecDeps holds all dependencies needed by the scheduled task
// executor. Using a struct avoids a growing parameter list as more
// task types are added.
type taskExecDeps struct {
	runner        agentRunner
	logger        *slog.Logger
	workspacePath string
	emailPoller   emailChecker // nil when email polling is not configured
}

// runScheduledTask handles execution of a scheduled task by dispatching
// PayloadWake tasks to the agent loop. Unsupported payload kinds are
// logged and silently ignored (returning nil, not an error).
//
// For email_poll tasks, the poller checks IMAP accounts for new messages
// and only wakes the agent if something new arrived — avoiding LLM token
// spend on empty poll cycles.
func runScheduledTask(ctx context.Context, task *scheduler.Task, exec *scheduler.Execution, deps taskExecDeps) error {
	deps.logger.Debug("task executing",
		"task_id", task.ID,
		"task_name", task.Name,
		"payload_kind", task.Payload.Kind,
	)

	if task.Payload.Kind != scheduler.PayloadWake {
		deps.logger.Warn("unsupported task payload kind", "kind", task.Payload.Kind)
		return nil
	}

	msg, _ := task.Payload.Data["message"].(string)
	if msg == "" {
		msg = "Scheduled wake: " + task.Name
	}

	// Email poll: run the poller and only wake the agent if new mail arrived.
	if task.Name == emailPollTaskName && deps.emailPoller != nil {
		wakeMsg, err := deps.emailPoller.CheckNewMessages(ctx)
		if err != nil {
			deps.logger.Warn("email poll failed", "error", err)
			return nil // best-effort — next cycle will catch up
		}
		if wakeMsg == "" {
			exec.Result = "no new messages"
			return nil // nothing new, skip the LLM wake
		}
		msg = wakeMsg
	}

	// Context injection for periodic_reflection: read ego.md and build
	// the reflection prompt with its current contents.
	if task.Name == periodicReflectionTaskName && deps.workspacePath != "" {
		egoContent := readEgoMD(deps.workspacePath, deps.logger)
		msg = prompts.PeriodicReflectionPrompt(egoContent)
	}

	// Extract optional routing overrides from payload data.
	// Tasks can specify model, local_only, and quality_floor to
	// override the defaults (local-only, cheapest model).
	model, _ := task.Payload.Data["model"].(string)

	localOnly := "true"
	if lo, ok := task.Payload.Data["local_only"].(string); ok {
		localOnly = lo
	}

	qualityFloor := "1"
	if qf, ok := task.Payload.Data["quality_floor"].(string); ok {
		qualityFloor = qf
	}

	// Each task gets its own conversation to isolate from interactive chat.
	req := &agent.Request{
		ConversationID: fmt.Sprintf("sched-%s", task.ID),
		Model:          model,
		Messages:       []agent.Message{{Role: "user", Content: msg}},
		Hints: map[string]string{
			"source":                    "scheduler",
			"task":                      task.Name,
			router.HintLocalOnly:        localOnly,
			router.HintQualityFloor:     qualityFloor,
			router.HintMission:          "automation",
			router.HintDelegationGating: "disabled", // full tool access, no delegation indirection
		},
	}

	resp, err := deps.runner.Run(ctx, req, nil)
	if err != nil {
		return fmt.Errorf("scheduled task %q: %w", task.Name, err)
	}
	exec.Result = resp.Content

	deps.logger.Debug("task completed",
		"task_id", task.ID,
		"task_name", task.Name,
		"result_len", len(resp.Content),
	)
	return nil
}

// maxEgoBytes is the maximum ego.md content passed to the reflection
// prompt. Content beyond this limit is truncated with a marker.
const maxEgoBytes = 16 * 1024

// readEgoMD reads the ego.md file from the workspace. Returns an empty
// string if the file does not exist (first reflection creates it).
// Content is capped at maxEgoBytes to bound prompt size.
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
	if len(data) > maxEgoBytes {
		return string(data[:maxEgoBytes]) + "\n\n[ego.md truncated — exceeded 16 KB limit]"
	}
	return string(data)
}

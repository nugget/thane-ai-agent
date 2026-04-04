package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/logging"
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

// taskExecDeps holds all dependencies needed by the scheduled task
// executor. Using a struct avoids a growing parameter list as more
// task types are added.
type taskExecDeps struct {
	runner        agentRunner
	logger        *slog.Logger
	workspacePath string
}

// runScheduledTask handles execution of a scheduled task by dispatching
// PayloadWake tasks to the agent loop. Unsupported payload kinds are
// logged and silently ignored (returning nil, not an error).
func runScheduledTask(ctx context.Context, task *scheduler.Task, exec *scheduler.Execution, deps taskExecDeps) error {
	log := deps.logger.With(
		"subsystem", logging.SubsystemScheduler,
		"task_id", task.ID,
		"task_name", task.Name,
	)
	ctx = logging.WithLogger(ctx, log)

	log.Debug("task executing",
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

	// Context injection for periodic_reflection: read ego.md and build
	// the reflection prompt with its current contents.
	if task.Name == periodicReflectionTaskName && deps.workspacePath != "" {
		egoContent := readEgoMD(deps.workspacePath, deps.logger)
		msg = prompts.PeriodicReflectionPrompt(egoContent)
	}

	// Build the wake routing profile via LoopProfile so scheduled tasks
	// use the same routing/config path as the newer wake subsystems.
	seed := buildScheduledTaskLoopProfile(task)
	reqOpts := seed.RequestOptions()

	// Each execution gets a fresh conversation so prior poll/wake context
	// doesn't accumulate across cycles. The execution ID (UUIDv7) ensures
	// uniqueness while the task ID prefix aids log correlation.
	req := &agent.Request{
		ConversationID: fmt.Sprintf("sched-%s-%s", task.ID, exec.ID),
		Model:          reqOpts.Model,
		Messages:       []agent.Message{{Role: "user", Content: msg}},
		Hints:          reqOpts.Hints,
		ExcludeTools:   reqOpts.ExcludeTools,
		SeedTags:       reqOpts.SeedTags,
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

// buildScheduledTaskLoopProfile converts a wake task payload into the
// shared LoopProfile routing/config shape. Tasks can override the default
// local-only, cheapest-model behavior with model/local_only/
// quality_floor payload fields.
func buildScheduledTaskLoopProfile(task *scheduler.Task) router.LoopProfile {
	model, _ := task.Payload.Data["model"].(string)

	localOnly := "true"
	if lo, ok := task.Payload.Data["local_only"].(string); ok {
		localOnly = lo
	}

	qualityFloor := "1"
	if qf, ok := task.Payload.Data["quality_floor"].(string); ok {
		qualityFloor = qf
	}

	return router.LoopProfile{
		Model:            model,
		LocalOnly:        localOnly,
		QualityFloor:     qualityFloor,
		Mission:          "automation",
		DelegationGating: "disabled",
		ExtraHints: map[string]string{
			"source": "scheduler",
			"task":   task.Name,
		},
	}
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

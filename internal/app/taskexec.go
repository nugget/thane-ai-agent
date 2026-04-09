package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
)

// periodicReflectionTaskName is the well-known name for the self-reflection
// scheduled task. Used for startup registration and context injection.
const periodicReflectionTaskName = "periodic_reflection"

// agentRunner abstracts direct agent dispatch sites that still bypass
// loops-ng launch semantics (for example, handler-triggered wake paths
// that create their own conversations).
type agentRunner interface {
	Run(ctx context.Context, req *agent.Request, stream agent.StreamCallback) (*agent.Response, error)
}

// taskExecDeps holds all dependencies needed by the scheduled task
// executor. Using a struct avoids a growing parameter list as more
// task types are added.
type taskExecDeps struct {
	launch        func(context.Context, looppkg.Launch, looppkg.Deps) (looppkg.LaunchResult, error)
	runner        looppkg.Runner
	eventBus      *events.Bus
	outputSink    looppkg.OutputSink
	logger        *slog.Logger
	workspacePath string
}

// runScheduledTask handles execution of a scheduled task by compiling it
// into a transient loops-ng launch. Unsupported payload kinds are logged
// and silently ignored (returning nil, not an error).
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
	if deps.launch == nil {
		return fmt.Errorf("scheduled task %q: loop launcher is not configured", task.Name)
	}
	if deps.runner == nil {
		return fmt.Errorf("scheduled task %q: loop runner is not configured", task.Name)
	}

	launch := buildScheduledTaskLaunch(ctx, task, exec, deps.workspacePath, deps.logger)
	result, err := deps.launch(ctx, launch, looppkg.Deps{
		Runner:     deps.runner,
		Logger:     deps.logger,
		EventBus:   deps.eventBus,
		OutputSink: deps.outputSink,
	})
	if err != nil {
		return fmt.Errorf("scheduled task %q: %w", task.Name, err)
	}
	if result.Response != nil {
		exec.Result = result.Response.Content
	}

	deps.logger.Debug("task completed",
		"task_id", task.ID,
		"task_name", task.Name,
		"loop_id", result.LoopID,
		"result_len", len(exec.Result),
	)
	return nil
}

// buildScheduledTaskLaunch compiles a persisted scheduler task and one
// execution record into a loops-ng launch with scheduler-specific
// routing, metadata, and timeout inheritance.
func buildScheduledTaskLaunch(ctx context.Context, task *scheduler.Task, exec *scheduler.Execution, workspacePath string, logger *slog.Logger) looppkg.Launch {
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

	profile := buildScheduledTaskLoopProfile(task)

	launch := looppkg.Launch{
		Spec: looppkg.Spec{
			Name:       "scheduler:" + task.Name,
			Task:       "Execute the scheduled task prompt exactly as requested.",
			Operation:  looppkg.OperationRequestReply,
			Completion: looppkg.CompletionNone,
			Profile:    profile,
			Metadata: map[string]string{
				"subsystem": "scheduler",
				"category":  "task",
				"task_id":   task.ID,
				"task_name": task.Name,
			},
		},
		Task:           msg,
		ConversationID: fmt.Sprintf("sched-%s-%s", task.ID, exec.ID),
		Metadata: map[string]string{
			"execution_id": exec.ID,
		},
		UsageRole:     "scheduler",
		UsageTaskName: task.Name,
	}

	if deadline, ok := ctx.Deadline(); ok {
		if timeout := time.Until(deadline); timeout > 0 {
			launch.RunTimeout = timeout
		}
	}

	return launch
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

// readEgoMD reads the ego.md file from the fixed workspace/core root.
// Returns an empty string if the file does not exist (first reflection
// creates it).
// Content is capped at maxEgoBytes to bound prompt size.
func readEgoMD(workspacePath string, logger *slog.Logger) string {
	egoPath := coreFilePath(workspacePath, "ego.md")
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

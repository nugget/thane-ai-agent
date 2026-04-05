package app

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/models"
	"github.com/nugget/thane-ai-agent/internal/paths"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
)

// initAgentLoop builds the core agent loop, registers the periodic
// self-reflection task, resolves path prefixes and context injection
// files, and starts the initial conversation session.
func (a *App) initAgentLoop(s *newState) error {
	cfg := a.cfg
	logger := a.logger
	defaultModel := a.modelCatalog.DefaultModel
	recoveryModel := a.modelCatalog.RecoveryModel

	// --- Periodic reflection ---
	// Register the self-reflection task if it doesn't already exist.
	// Requires a workspace so the agent can write ego.md via file tools.
	// Uses a cloud model (Sonnet) for higher-quality reflection output.
	if cfg.Workspace.Path != "" {
		reflectionInterval := 24 * time.Hour
		reflectionPayload := desiredPeriodicReflectionPayload(a.modelCatalog)

		existing, err := a.schedStore.GetTaskByName(periodicReflectionTaskName)
		if err != nil {
			logger.Error("failed to check for periodic_reflection task", "error", err)
		} else if existing == nil {
			reflectionTask := &scheduler.Task{
				Name: periodicReflectionTaskName,
				Schedule: scheduler.Schedule{
					Kind:  scheduler.ScheduleEvery,
					Every: &scheduler.Duration{Duration: reflectionInterval},
				},
				Payload:   reflectionPayload,
				Enabled:   true,
				CreatedBy: "system",
			}
			if err := a.sched.CreateTask(reflectionTask); err != nil {
				logger.Error("failed to create periodic_reflection task", "error", err)
			} else {
				logger.Info("periodic_reflection task registered", "interval", reflectionInterval)
			}
		} else {
			// Keep this built-in task converged to the current desired
			// schedule and payload so stale persisted model refs self-heal
			// after config/catalog changes.
			needsUpdate := syncPeriodicReflectionTask(existing, reflectionInterval, reflectionPayload)
			if needsUpdate {
				if err := a.sched.UpdateTask(existing); err != nil {
					logger.Error("failed to update periodic_reflection task", "error", err)
				} else {
					logger.Info("periodic_reflection task updated", "interval", reflectionInterval)
				}
			} else {
				logger.Debug("periodic_reflection task already registered", "id", existing.ID)
			}
		}
	}

	// --- Agent loop ---
	// The core conversation engine. Receives messages, manages context,
	// invokes tools, and streams responses. All other components plug
	// into it.
	defaultContextWindow := a.modelCatalog.ContextWindowForModel(defaultModel, 200000)

	loop := agent.NewLoop(logger, a.mem, a.compactor, a.rtr, a.ha, a.sched, a.llmClient, defaultModel, s.parsedTalents, s.personaContent, defaultContextWindow)
	a.loop = loop
	loop.SetTimezone(cfg.Timezone)
	loop.UseModelRegistry(a.modelRegistry)
	loop.UseModelRuntime(a.modelRuntime)
	if a.liveRequestRecorder != nil {
		loop.UseLiveRequestRecorder(a.liveRequestRecorder)
	}
	if a.requestRecorder != nil {
		loop.SetRequestRecorder(a.requestRecorder)
	}
	if recoveryModel != "" {
		loop.SetRecoveryModel(recoveryModel)
		logger.Info("LLM timeout recovery enabled", "recovery_model", recoveryModel)
	}
	loop.SetArchiver(a.archiveAdapter)
	if a.ha != nil {
		loop.SetHAInject(a.ha)
	}

	// --- Shared path prefix resolver ---
	// Build a resolver from the paths: config map. This handles kb:,
	// scratchpad:, and any future directory-based prefixes. The
	// resolver expands ~ in base directories at construction time.
	// Auto-register core: prefix pointing at the workspace root so
	// models can reference core:ego.md without knowing the filesystem
	// path. User-defined core: (with or without trailing colon) in
	// config takes precedence.
	if cfg.Workspace.Path != "" {
		if cfg.Paths == nil {
			cfg.Paths = make(map[string]string)
		}
		hasCore := false
		for k := range cfg.Paths {
			if strings.TrimSuffix(k, ":") == "core" {
				hasCore = true
				break
			}
		}
		if !hasCore {
			cfg.Paths["core"] = cfg.Workspace.Path
		}
	}

	var resolver *paths.Resolver
	if len(cfg.Paths) > 0 {
		resolver = paths.New(cfg.Paths)
		logger.Info("path prefixes registered", "prefixes", resolver.Prefixes())
	}
	s.resolver = resolver

	// --- Context injection ---
	// Resolve inject_file paths at startup (tilde expansion, existence
	// check) but defer reading to each agent turn so external edits
	// (e.g. MEMORY.md updated by another runtime) are visible without
	// restart.
	if len(cfg.Context.InjectFiles) > 0 {
		var resolved []string
		for _, path := range cfg.Context.InjectFiles {
			path = resolvePath(path, resolver)
			if _, err := os.Stat(path); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					logger.Warn("context inject file not found", "path", path)
				} else {
					logger.Warn("context inject file unreadable", "path", path, "error", err)
				}
				// Still include the path — the file may appear later.
			}
			resolved = append(resolved, path)
			logger.Debug("context inject file registered", "path", path)
		}
		loop.SetInjectFiles(resolved)
		logger.Info("context inject files registered", "files", len(resolved))
	}

	// --- OpenClaw profile ---
	if cfg.OpenClaw != nil {
		loop.SetOpenClawConfig(cfg.OpenClaw)
		logger.Info("thane:openclaw profile enabled",
			"workspace", cfg.OpenClaw.WorkspacePath,
			"skills_dirs", cfg.OpenClaw.SkillsDirs,
		)
	}

	// Start initial session
	a.archiveAdapter.EnsureSession("default")

	return nil
}

func desiredPeriodicReflectionPayload(modelCatalog *models.Catalog) scheduler.Payload {
	reflectionModel := "claude-sonnet-4-20250514"
	if modelCatalog != nil {
		if resolved, err := modelCatalog.ResolveModelRef(reflectionModel); err == nil {
			reflectionModel = resolved
		}
	}
	return scheduler.Payload{
		Kind: scheduler.PayloadWake,
		Data: map[string]any{
			"message":       "periodic_reflection",
			"model":         reflectionModel,
			"local_only":    "false",
			"quality_floor": "7",
		},
	}
}

func syncPeriodicReflectionTask(task *scheduler.Task, interval time.Duration, payload scheduler.Payload) bool {
	needsUpdate := false

	if task.Schedule.Kind != scheduler.ScheduleEvery || task.Schedule.Every == nil || task.Schedule.Every.Duration != interval {
		task.Schedule.Kind = scheduler.ScheduleEvery
		task.Schedule.Every = &scheduler.Duration{Duration: interval}
		needsUpdate = true
	}

	if !periodicReflectionPayloadEqual(task.Payload, payload) {
		task.Payload = cloneSchedulerPayload(payload)
		needsUpdate = true
	}

	if !task.Enabled {
		task.Enabled = true
		needsUpdate = true
	}

	return needsUpdate
}

func periodicReflectionPayloadEqual(a, b scheduler.Payload) bool {
	if a.Kind != b.Kind {
		return false
	}
	if len(a.Data) != len(b.Data) {
		return false
	}
	return maps.EqualFunc(a.Data, b.Data, func(left, right any) bool {
		ls, lok := left.(string)
		rs, rok := right.(string)
		return lok && rok && ls == rs
	})
}

func cloneSchedulerPayload(in scheduler.Payload) scheduler.Payload {
	out := scheduler.Payload{Kind: in.Kind}
	if len(in.Data) > 0 {
		out.Data = maps.Clone(in.Data)
	}
	return out
}

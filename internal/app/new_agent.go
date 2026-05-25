package app

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
)

// initAgentLoop builds the core agent loop, resolves path prefixes and
// context injection files, and starts the initial conversation session.
// Self-reflection runs as the ego service loop (see internal/runtime/ego).
func (a *App) initAgentLoop(s *newState) error {
	cfg := a.cfg
	logger := a.logger
	defaultModel := a.modelCatalog.DefaultModel
	recoveryModel := a.modelCatalog.RecoveryModel

	// One-shot migration: remove any legacy periodic_reflection scheduler
	// row left behind by older builds. The ego loop replaces it.
	a.removeLegacyPeriodicReflectionTask(logger)

	// --- Path prefix resolver + core prompt files ---
	// Resolve workspace-derived paths and core context-injection files
	// before constructing the agent loop so they can be passed via
	// LoopOptions instead of post-construction setters.
	if cfg.Workspace.Path != "" {
		if cfg.Paths == nil {
			cfg.Paths = make(map[string]string)
		}
		derivedCore := coreRootPath(cfg.Workspace.Path)
		for k, v := range cfg.Paths {
			if strings.TrimSuffix(k, ":") != "core" {
				continue
			}
			if strings.TrimSpace(v) != derivedCore {
				logger.Info("ignoring configured core path; core root is derived from workspace.path",
					"configured_key", k,
					"configured_path", v,
					"derived_path", derivedCore,
				)
			}
			delete(cfg.Paths, k)
		}
		cfg.Paths["core"] = derivedCore
		if err := os.MkdirAll(derivedCore, 0o755); err != nil {
			return fmt.Errorf("create core document root: %w", err)
		}
	}

	var resolver *paths.Resolver
	if len(cfg.Paths) > 0 {
		resolver = paths.New(cfg.Paths)
		logger.Info("path prefixes registered", "prefixes", resolver.Prefixes())
	}
	s.resolver = resolver

	// Resolve fixed core prompt files if a workspace is configured. The
	// core context provider reads them fresh on every turn, so paths are
	// enough at startup.
	var axiomsFile, personaFile, missionFile, egoFile string
	if cfg.Workspace.Path != "" {
		axiomsFile = resolvePath(coreFilePath(cfg.Workspace.Path, "axioms.md"), nil)
		personaFile = resolvePath(coreFilePath(cfg.Workspace.Path, "persona.md"), nil)
		missionFile = resolvePath(coreFilePath(cfg.Workspace.Path, "mission.md"), nil)
		egoFile = resolvePath(coreFilePath(cfg.Workspace.Path, "ego.md"), nil)
	}

	s.resolvedCorePromptFiles = corePromptFilesForStartupVerification(logger, axiomsFile, personaFile, missionFile, egoFile)

	// --- Agent loop ---
	// The core conversation engine. Receives messages, manages context,
	// invokes tools, and streams responses. All other components plug
	// into it through LoopOptions at construction or grouped Configure*
	// methods invoked by later init phases.
	defaultContextWindow := a.modelCatalog.ContextWindowForModel(defaultModel, 200000)

	var haInject homeassistant.StateFetcher
	if a.ha != nil {
		haInject = a.ha
	}

	loop, err := agent.NewLoop(agent.LoopOptions{
		Logger:              logger,
		Memory:              a.mem,
		Compactor:           a.compactor,
		Router:              a.rtr,
		HomeAssistant:       a.ha,
		Scheduler:           a.sched,
		LLM:                 a.llmClient,
		Model:               defaultModel,
		ContextWindow:       defaultContextWindow,
		AxiomsFile:          axiomsFile,
		PersonaFile:         personaFile,
		MissionFile:         missionFile,
		ParsedTalents:       s.parsedTalents,
		Timezone:            cfg.Timezone,
		RecoveryModel:       recoveryModel,
		Archiver:            a.archiveAdapter,
		EgoFile:             egoFile,
		HAInject:            haInject,
		ModelRegistry:       a.modelRegistry,
		ModelRuntime:        a.modelRuntime,
		LiveRequestRecorder: a.liveRequestRecorder,
		RequestRecorder:     a.requestRecorder,
	})
	if err != nil {
		return fmt.Errorf("build agent loop: %w", err)
	}
	a.loop = loop
	if recoveryModel != "" {
		logger.Info("LLM timeout recovery enabled", "recovery_model", recoveryModel)
	}

	// Start initial session
	a.archiveAdapter.EnsureSession("default")

	return nil
}

func corePromptFilesForStartupVerification(logger *slog.Logger, paths ...string) []string {
	var out []string
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			out = append(out, path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			if logger != nil {
				logger.Warn("core prompt file unreadable", "path", path, "error", err)
			}
			// Keep unreadable paths in startup verification so managed-root
			// policy failures surface instead of being silently skipped.
			out = append(out, path)
		}
	}
	return out
}

// removeLegacyPeriodicReflectionTask deletes the persisted
// periodic_reflection scheduler row from older builds. Self-reflection
// is now handled by the ego service loop. Best-effort: errors are
// logged and ignored so a missing row or transient error does not block
// startup.
func (a *App) removeLegacyPeriodicReflectionTask(logger *slog.Logger) {
	if a.schedStore == nil || a.sched == nil {
		return
	}
	existing, err := a.schedStore.GetTaskByName(legacyPeriodicReflectionTaskName)
	if err != nil {
		logger.Debug("legacy periodic_reflection lookup failed", "error", err)
		return
	}
	if existing == nil {
		return
	}
	if err := a.sched.DeleteTask(existing.ID); err != nil {
		logger.Warn("failed to delete legacy periodic_reflection task",
			"id", existing.ID,
			"error", err,
		)
		return
	}
	logger.Info("removed legacy periodic_reflection scheduler task",
		"id", existing.ID,
		"replacement", "ego service loop",
	)
}

const legacyPeriodicReflectionTaskName = "periodic_reflection"

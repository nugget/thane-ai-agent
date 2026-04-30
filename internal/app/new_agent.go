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
// Self-reflection runs as the ego loops-ng service (see internal/runtime/ego).
func (a *App) initAgentLoop(s *newState) error {
	cfg := a.cfg
	logger := a.logger
	defaultModel := a.modelCatalog.DefaultModel
	recoveryModel := a.modelCatalog.RecoveryModel

	// One-shot migration: remove any legacy periodic_reflection scheduler
	// row left behind by older builds. The ego loop replaces it.
	a.removeLegacyPeriodicReflectionTask(logger)

	// --- Path prefix resolver + inject files ---
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

	// Resolve fixed core context files at startup (tilde expansion,
	// existence check) but defer reading to the core context provider
	// each agent turn so edits under workspace/core are visible without
	// restart.
	var resolvedInjectFiles []string
	if injectFiles := cfg.CoreInjectFiles(); len(injectFiles) > 0 {
		for _, path := range injectFiles {
			path = resolvePath(path, resolver)
			if _, err := os.Stat(path); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					logger.Warn("core context file not found", "path", path)
				} else {
					logger.Warn("core context file unreadable", "path", path, "error", err)
				}
				// Still include the path — the file may appear later.
			}
			resolvedInjectFiles = append(resolvedInjectFiles, path)
			logger.Debug("core context file registered", "path", path)
		}
		logger.Info("core context files registered", "files", len(resolvedInjectFiles))
	}
	s.resolvedInjectFiles = resolvedInjectFiles

	// Resolve ego.md path if a workspace is configured. The core context
	// provider reads the file fresh on every turn, so the path is enough
	// at startup.
	var egoFile string
	if cfg.Workspace.Path != "" {
		egoFile = resolvePath(coreFilePath(cfg.Workspace.Path, "ego.md"), nil)
	}

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
		Persona:             s.personaContent,
		ParsedTalents:       s.parsedTalents,
		Timezone:            cfg.Timezone,
		RecoveryModel:       recoveryModel,
		Archiver:            a.archiveAdapter,
		InjectFiles:         resolvedInjectFiles,
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

// removeLegacyPeriodicReflectionTask deletes the persisted
// periodic_reflection scheduler row from older builds. Self-reflection
// is now handled by the ego loops-ng service. Best-effort: errors are
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
		"replacement", "ego loops-ng service",
	)
}

const legacyPeriodicReflectionTaskName = "periodic_reflection"

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/attachments"
	"github.com/nugget/thane-ai-agent/internal/awareness"
	"github.com/nugget/thane-ai-agent/internal/buildinfo"
	cdav "github.com/nugget/thane-ai-agent/internal/carddav"
	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	sigcli "github.com/nugget/thane-ai-agent/internal/channels/signal"
	"github.com/nugget/thane-ai-agent/internal/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/contacts"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/delegate"
	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/forge"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/knowledge"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/mcp"
	"github.com/nugget/thane-ai-agent/internal/media"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/metacognitive"
	"github.com/nugget/thane-ai-agent/internal/notifications"
	"github.com/nugget/thane-ai-agent/internal/opstate"
	"github.com/nugget/thane-ai-agent/internal/paths"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/provenance"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
	"github.com/nugget/thane-ai-agent/internal/search"
	"github.com/nugget/thane-ai-agent/internal/server/api"
	"github.com/nugget/thane-ai-agent/internal/server/web"
	"github.com/nugget/thane-ai-agent/internal/talents"
	"github.com/nugget/thane-ai-agent/internal/telemetry"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/unifi"
	"github.com/nugget/thane-ai-agent/internal/usage"

	_ "github.com/mattn/go-sqlite3" // SQLite driver for database/sql
)

// New constructs and initializes a fully wired App from the provided
// configuration. The llmClient and ollamaClient are pre-constructed by
// the caller (cmd/thane) so that runAsk and runServe can share the
// createLLMClient function without importing internal/app.
//
// New opens resources, wires dependencies, and registers background
// workers but does not start them. Call [App.StartWorkers] to launch
// all deferred goroutines and persistent loops, then [App.Serve] to
// start external servers and block until shutdown.
//
// All resources that require cleanup are tracked on the returned App;
// cleanup happens in [App.shutdown].
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger, stdout io.Writer, llmClient llm.Client, ollamaClient *llm.OllamaClient) (*App, error) {
	a := &App{
		cfg:          cfg,
		logger:       logger,
		stdout:       stdout,
		llmClient:    llmClient,
		ollamaClient: ollamaClient,
	}

	// Augment PATH before any exec.LookPath calls (tool registration,
	// media client init, etc.) so Homebrew and user-configured binaries
	// are discoverable. Logging is deferred until the final logger is
	// configured (the initial logger is Info-level so Debug would be lost).
	augmentedDirs := augmentPath(cfg.ExtraPath)

	// Reconfigure logger now that we know the desired level, format, and
	// output destination. The initial Info-level text logger above is used
	// only for the startup banner and config load message.
	{
		level, _ := config.ParseLogLevel(cfg.Logging.Level)

		// Open the log rotator for file output. Logs go to both
		// stdout (for launchd/systemd capture) and the rotated file.
		// When Dir is empty, file logging is disabled (stdout only).
		logWriter := stdout
		var rotator *logging.Rotator

		if logDir := cfg.Logging.DirPath(); logDir != "" {
			var err error
			rotator, err = logging.Open(logDir, cfg.Logging.CompressEnabled())
			if err != nil {
				// File logging failed — fall back to stdout only.
				logger.Warn("failed to open log directory, using stdout only",
					"dir", logDir, "error", err)
			} else {
				a.rotator = rotator
				logWriter = io.MultiWriter(stdout, rotator)
			}
		}

		handler := newHandler(logWriter, level, cfg.Logging.Format)

		// Open the SQLite log index alongside the raw log files.
		// If file logging is disabled (no logDir) or the DB fails to
		// open, logging continues without indexing.
		if logDir := cfg.Logging.DirPath(); logDir != "" {
			var err error
			a.indexDB, err = database.Open(filepath.Join(logDir, "logs.db"))
			if err != nil {
				logger.Warn("failed to open log index database, indexing disabled",
					"error", err)
				a.indexDB = nil
			} else if err := logging.Migrate(a.indexDB); err != nil {
				logger.Warn("failed to migrate log index schema, indexing disabled",
					"error", err)
				a.indexDB.Close()
				a.indexDB = nil
			} else {
				indexHandler := logging.NewIndexHandler(handler, a.indexDB, rotator)
				// indexHandler and indexDB are closed in shutdown()
				handler = indexHandler
				a.indexHandler = indexHandler
			}
		}

		logger = slog.New(handler).With(
			"thane_version", buildinfo.Version,
			"thane_commit", buildinfo.GitCommit,
		)
		a.logger = logger
	}

	// Content retention — create after the final logger so warnings
	// go through the configured handler.
	if cfg.Logging.RetainContent && a.indexDB != nil {
		cw, cwErr := logging.NewContentWriter(a.indexDB, cfg.Logging.ContentMaxLength(), logger)
		if cwErr != nil {
			logger.Warn("failed to create content writer, content retention disabled", "error", cwErr)
		} else {
			a.contentWriter = cw
			logger.Info("content retention enabled",
				"max_content_length", cfg.Logging.ContentMaxLength(),
			)
		}
	}

	// Log PATH augmentation now that the final logger is configured.
	if len(augmentedDirs) > 0 {
		logger.Debug("augmented PATH", "prepended", augmentedDirs)
	}

	// Defer background log index pruner if retention is configured and
	// the index database is available.
	if a.indexDB != nil {
		if retention := cfg.Logging.RetentionDaysDuration(); retention > 0 {
			a.deferWorker("log-index-pruner", func(ctx context.Context) error {
				go func() {
					ticker := time.NewTicker(24 * time.Hour)
					defer ticker.Stop()
					for {
						if deleted, err := logging.Prune(a.indexDB, retention, slog.LevelInfo); err != nil {
							logger.Warn("log index prune failed", "error", err, "retention", retention)
						} else if deleted > 0 {
							logger.Info("pruned log index", "deleted", deleted, "retention", retention)
						} else {
							logger.Debug("log index prune ran; nothing to delete", "retention", retention)
						}
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
						}
					}
				}()
				return nil
			})
		}

		// Defer background content archiver: exports retained request/tool
		// content older than ContentArchiveDays to monthly JSONL files, then
		// removes it from logs.db. Runs daily; disabled when archive duration
		// is 0 or content retention is off.
		if logDir := cfg.Logging.DirPath(); logDir != "" {
			if archiveDur := cfg.Logging.ContentArchiveDuration(); archiveDur > 0 && a.contentWriter != nil {
				archiveDir := cfg.Logging.ContentArchiveDirPath(logDir)
				archiver := logging.NewArchiver(a.indexDB, archiveDir, logger)
				a.deferWorker("content-archiver", func(ctx context.Context) error {
					go func() {
						ticker := time.NewTicker(24 * time.Hour)
						defer ticker.Stop()
						for {
							before := time.Now().Add(-archiveDur)
							runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
							n, err := archiver.Archive(runCtx, before)
							cancel()
							if err != nil {
								logger.Warn("content archive failed", "error", err, "before", before)
							} else if n > 0 {
								logger.Info("content archived", "requests", n, "before", before)
							} else {
								logger.Debug("content archive ran; nothing to archive", "before", before)
							}
							select {
							case <-ctx.Done():
								return
							case <-ticker.C:
							}
						}
					}()
					return nil
				})
				logger.Info("content archival enabled", "archive_after", archiveDur)
			}
		}
	}

	// Warn about deprecated config fields.
	if depLevel, depFormat := cfg.DeprecatedFieldsUsed(); depLevel || depFormat {
		logger.Warn("log_level/log_format are deprecated; use logging.level/logging.format instead")
	}

	// --- Data directory ---
	// All persistent state (SQLite databases for memory, facts, scheduler,
	// checkpoints, and anticipations) lives under this directory.
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", cfg.DataDir, err)
	}

	// --- Event bus ---
	// Process-wide publish/subscribe for operational observability.
	// Components publish structured events; the dashboard WebSocket
	// handler (and future metrics/alerting consumers) subscribe.
	// Zero cost when nobody subscribes.
	eventBus := events.New()
	a.eventBus = eventBus

	// --- Loop registry ---
	// Tracks all persistent background loops (metacognitive, pollers,
	// watchers). Created early so component init blocks can register
	// loops before the web dashboard is wired up.
	loopRegistry := looppkg.NewRegistry(looppkg.WithRegistryLogger(logger))
	a.loopRegistry = loopRegistry

	// --- Demo loops (debug) ---
	if cfg.Debug.DemoLoops {
		a.deferWorker("demo-loops", func(ctx context.Context) error {
			if err := looppkg.SpawnDemoLoops(ctx, loopRegistry, eventBus, logger); err != nil {
				return fmt.Errorf("spawn demo loops: %w", err)
			}
			logger.Warn("demo loops enabled — dashboard shows simulated activity")
			return nil
		})
	}

	// --- Memory store ---
	// SQLite-backed conversation memory. Persists across restarts so the
	// agent can resume in-progress conversations.
	dbPath := cfg.DataDir + "/thane.db"
	mem, err := memory.NewSQLiteStore(dbPath, 100)
	if err != nil {
		return nil, fmt.Errorf("open memory database %s: %w", dbPath, err)
	}
	a.mem = mem
	logger.Info("memory database opened", "path", dbPath)

	// --- Home Assistant client ---
	// Optional but central. Without it, HA-related tools are unavailable
	// and Thane operates as a general-purpose agent.
	if cfg.HomeAssistant.Configured() {
		a.ha = homeassistant.NewClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, logger)
		a.haWS = homeassistant.NewWSClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, logger)
		logger.Debug("Home Assistant configured", "url", cfg.HomeAssistant.URL)
	} else {
		logger.Warn("Home Assistant not configured - tools will be limited")
	}

	// --- Connection resilience ---
	// Background health monitoring with exponential backoff for external
	// dependencies (Home Assistant, Ollama). Replaces the former single-shot
	// Ping() check with retries on startup and automatic reconnection at
	// runtime — no restart required. See issue #96.
	connMgr := connwatch.NewManager(logger)
	a.connMgr = connMgr

	// Forward-declare personTracker so the connwatch OnReady callback
	// can reference it. The closure captures by pointer; the tracker
	// is constructed later and also calls Initialize immediately after
	// construction to cover the case where HA connected first.
	var personTracker *contacts.PresenceTracker

	var subscribeOnce sync.Once
	if a.ha != nil {
		haWatcher := connMgr.Watch(ctx, connwatch.WatcherConfig{
			Name:    "homeassistant",
			Probe:   func(pCtx context.Context) error { return a.ha.Ping(pCtx) },
			Backoff: connwatch.DefaultBackoffConfig(),
			OnReady: func() {
				// Log HA details on first successful connection.
				infoCtx, infoCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer infoCancel()
				if haCfg, err := a.ha.GetConfig(infoCtx); err == nil {
					logger.Info("connected to Home Assistant",
						"url", cfg.HomeAssistant.URL,
						"version", haCfg.Version,
						"location", haCfg.LocationName,
					)
				}

				// Reconnect WebSocket when HA comes back.
				if a.haWS != nil {
					wsCtx, wsCancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer wsCancel()
					if err := a.haWS.Reconnect(wsCtx); err != nil {
						logger.Error("WebSocket reconnect failed", "error", err)
					}

					// Subscribe to state_changed events on first connection.
					// Subsequent reconnects restore subscriptions automatically
					// via WSClient.restoreSubscriptions.
					subscribeOnce.Do(func() {
						subCtx, subCancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer subCancel()
						if err := a.haWS.Subscribe(subCtx, "state_changed"); err != nil {
							logger.Error("subscribe to state_changed failed", "error", err)
						} else {
							logger.Info("subscribed to state_changed events")
						}
					})
				}

				// Initialize (or refresh) person tracker from current HA state.
				if personTracker != nil {
					initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer initCancel()
					if err := personTracker.Initialize(initCtx, a.ha); err != nil {
						logger.Warn("person tracker initialization incomplete", "error", err)
					} else {
						logger.Info("person tracker initialized")
					}
				}
			},
			Logger: logger,
		})
		a.ha.SetWatcher(haWatcher)
	}

	ollamaWatcher := connMgr.Watch(ctx, connwatch.WatcherConfig{
		Name:    "ollama",
		Probe:   func(pCtx context.Context) error { return ollamaClient.Ping(pCtx) },
		Backoff: connwatch.DefaultBackoffConfig(),
		Logger:  logger,
	})
	ollamaClient.SetWatcher(ollamaWatcher)

	// --- Session archive ---
	// All data (sessions, iterations, messages, tool calls) lives in
	// thane.db. The archive store borrows the working DB connection.
	archiveStore, err := memory.NewArchiveStoreFromDB(mem.DB(), nil, logger)
	if err != nil {
		return nil, fmt.Errorf("open archive store: %w", err)
	}
	a.archiveStore = archiveStore

	// --- Working memory ---
	// Persists free-form experiential context per conversation.
	wmStore, err := memory.NewWorkingMemoryStore(mem.DB())
	if err != nil {
		return nil, fmt.Errorf("create working memory store: %w", err)
	}
	a.wmStore = wmStore
	logger.Info("working memory store initialized")

	archiveAdapter := memory.NewArchiveAdapter(archiveStore, mem, mem, logger)
	a.archiveAdapter = archiveAdapter

	// --- Talents ---
	// Talents are markdown files that extend the system prompt with
	// domain-specific knowledge and instructions.
	talentLoader := talents.NewLoader(cfg.TalentsDir)
	parsedTalents, err := talentLoader.Talents()
	if err != nil {
		return nil, fmt.Errorf("load talents: %w", err)
	}
	if len(parsedTalents) > 0 {
		talentNames := make([]string, len(parsedTalents))
		for i, t := range parsedTalents {
			talentNames[i] = t.Name
		}
		logger.Info("talents loaded", "count", len(parsedTalents), "talents", talentNames)
	}

	// --- Persona ---
	// An optional markdown file that replaces the default system prompt,
	// giving the agent a custom identity and behavioral guidelines.
	var personaContent string
	if cfg.PersonaFile != "" {
		data, err := os.ReadFile(cfg.PersonaFile)
		if err != nil {
			return nil, fmt.Errorf("load persona %s: %w", cfg.PersonaFile, err)
		}
		personaContent = string(data)
		logger.Info("persona loaded", "path", cfg.PersonaFile, "size", len(personaContent))
	}

	// --- Model router ---
	// Selects the best model for each request based on complexity, cost,
	// and capability requirements. Falls back to the default model.
	routerCfg := router.Config{
		DefaultModel: cfg.Models.Default,
		LocalFirst:   cfg.Models.LocalFirst,
		MaxAuditLog:  1000,
	}

	for _, m := range cfg.Models.Available {
		minComp := router.ComplexitySimple
		switch m.MinComplexity {
		case "moderate":
			minComp = router.ComplexityModerate
		case "complex":
			minComp = router.ComplexityComplex
		}

		routerCfg.Models = append(routerCfg.Models, router.Model{
			Name:          m.Name,
			Provider:      m.Provider,
			SupportsTools: m.SupportsTools,
			ContextWindow: m.ContextWindow,
			Speed:         m.Speed,
			Quality:       m.Quality,
			CostTier:      m.CostTier,
			MinComplexity: minComp,
		})
	}

	rtr := router.NewRouter(logger, routerCfg)
	a.rtr = rtr
	logger.Info("model router initialized",
		"models", len(routerCfg.Models),
		"default", routerCfg.DefaultModel,
		"local_first", routerCfg.LocalFirst,
	)

	// --- Conversation compactor ---
	// When a conversation grows too long, the compactor summarizes older
	// messages to stay within the model's context window. Routes through
	// the model router for quality-aware model selection.
	compactionConfig := memory.CompactionConfig{
		MaxTokens:            8000,
		TriggerRatio:         0.7, // Compact at 70% of MaxTokens
		KeepRecent:           10,  // Preserve the last 10 messages verbatim
		MinMessagesToCompact: 15,  // Don't bother compacting tiny conversations
	}

	summarizeFunc := func(ctx context.Context, prompt string) (string, error) {
		model, _ := rtr.Route(ctx, router.Request{
			Query:    "conversation compaction",
			Priority: router.PriorityBackground,
			Hints: map[string]string{
				router.HintMission:      "background",
				router.HintLocalOnly:    "true",
				router.HintQualityFloor: "7",
			},
		})
		msgs := []llm.Message{{Role: "user", Content: prompt}}
		resp, err := llmClient.Chat(ctx, model, msgs, nil)
		if err != nil {
			return "", err
		}
		return resp.Message.Content, nil
	}

	compactSummarizer := memory.NewLLMSummarizer(summarizeFunc)
	compactor := memory.NewCompactor(mem, compactionConfig, compactSummarizer, logger)
	compactor.SetWorkingMemoryStore(wmStore)
	a.compactor = compactor

	// --- Session metadata summarizer ---
	// Background worker that generates titles, tags, and summaries for
	// sessions that ended without metadata (e.g., during shutdown).
	// Runs immediately on startup to catch up, then periodically.
	var idleTimeoutMinutes int
	if cfg.Archive.SessionIdleMinutes != nil {
		idleTimeoutMinutes = *cfg.Archive.SessionIdleMinutes
	}
	summarizerCfg := memory.SummarizerConfig{
		Interval:        time.Duration(cfg.Archive.SummarizeInterval) * time.Second,
		Timeout:         time.Duration(cfg.Archive.SummarizeTimeout) * time.Second,
		PauseBetween:    5 * time.Second,
		BatchSize:       10,
		ModelPreference: cfg.Archive.MetadataModel,
		IdleTimeout:     time.Duration(idleTimeoutMinutes) * time.Minute,
	}
	summaryWorker := memory.NewSummarizerWorker(archiveStore, llmClient, rtr, logger, summarizerCfg)
	a.summaryWorker = summaryWorker

	// --- Scheduler ---
	// Persistent task scheduler for deferred and recurring work (e.g.,
	// wake events, periodic checks). Tasks survive restarts.
	schedStore, err := scheduler.NewStore(cfg.DataDir + "/scheduler.db")
	if err != nil {
		return nil, fmt.Errorf("open scheduler database: %w", err)
	}
	a.schedStore = schedStore

	// --- Operational state ---
	// Generic KV store for persistent operational state (poller
	// high-water marks, feature toggles, session preferences).
	// Shares the main thane.db connection.
	opStore, err := opstate.NewStore(mem.DB())
	if err != nil {
		return nil, fmt.Errorf("initialize operational state store: %w", err)
	}
	a.opStore = opStore

	// --- Usage tracking ---
	// Persistent token usage and cost recording for attribution and
	// analysis. Append-only SQLite store, queried via the cost_summary tool.
	// Shares the main thane.db connection.
	usageStore, err := usage.NewStore(mem.DB())
	if err != nil {
		return nil, fmt.Errorf("initialize usage store: %w", err)
	}
	a.usageStore = usageStore

	// Forward-declare task execution dependencies so the executeTask
	// closure can reference them. All fields are populated before the
	// scheduler fires its first task.
	var loop *agent.Loop
	var deps taskExecDeps
	deps.logger = logger
	deps.workspacePath = cfg.Workspace.Path

	executeTask := func(ctx context.Context, task *scheduler.Task, exec *scheduler.Execution) error {
		deps.runner = loop // captured by reference, set before first fire
		return runScheduledTask(ctx, task, exec, deps)
	}

	sched := scheduler.New(logger, schedStore, executeTask)
	a.sched = sched
	a.deferWorker("scheduler", func(ctx context.Context) error {
		if err := sched.Start(ctx); err != nil {
			return fmt.Errorf("start scheduler: %w", err)
		}
		return nil
	})

	// --- Periodic reflection ---
	// Register the self-reflection task if it doesn't already exist.
	// Requires a workspace so the agent can write ego.md via file tools.
	// Uses a cloud model (Sonnet) for higher-quality reflection output.
	if cfg.Workspace.Path != "" {
		reflectionInterval := 24 * time.Hour
		reflectionPayload := scheduler.Payload{
			Kind: scheduler.PayloadWake,
			Data: map[string]any{
				"message":       "periodic_reflection",
				"model":         "claude-sonnet-4-20250514",
				"local_only":    "false",
				"quality_floor": "7",
			},
		}

		existing, err := schedStore.GetTaskByName(periodicReflectionTaskName)
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
			if err := sched.CreateTask(reflectionTask); err != nil {
				logger.Error("failed to create periodic_reflection task", "error", err)
			} else {
				logger.Info("periodic_reflection task registered", "interval", reflectionInterval)
			}
		} else {
			// Migrate existing tasks from 15min/local-only to daily/Sonnet.
			needsUpdate := false
			if existing.Schedule.Every != nil && existing.Schedule.Every.Duration < reflectionInterval {
				existing.Schedule.Every.Duration = reflectionInterval
				needsUpdate = true
			}
			if existing.Payload.Data["model"] == nil {
				existing.Payload = reflectionPayload
				needsUpdate = true
			}
			if !existing.Enabled {
				existing.Enabled = true
				needsUpdate = true
			}
			if needsUpdate {
				if err := sched.UpdateTask(existing); err != nil {
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
	defaultContextWindow := cfg.ContextWindowForModel(cfg.Models.Default, 200000)

	loop = agent.NewLoop(logger, mem, compactor, rtr, a.ha, sched, llmClient, cfg.Models.Default, parsedTalents, personaContent, defaultContextWindow)
	a.loop = loop
	loop.SetTimezone(cfg.Timezone)
	if a.contentWriter != nil {
		loop.SetContentWriter(a.contentWriter)
	}
	if cfg.Models.RecoveryModel != "" {
		loop.SetRecoveryModel(cfg.Models.RecoveryModel)
		logger.Info("LLM timeout recovery enabled", "recovery_model", cfg.Models.RecoveryModel)
	}
	loop.SetArchiver(archiveAdapter)
	if a.ha != nil {
		loop.SetHAInject(a.ha)
	}

	// --- Context injection ---
	// Resolve inject_file paths at startup (tilde expansion, existence
	// check) but defer reading to each agent turn so external edits
	// (e.g. MEMORY.md updated by another runtime) are visible without
	// restart.
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
	archiveAdapter.EnsureSession("default")

	// --- Fact store ---
	// Long-term memory backed by SQLite. Facts are discrete pieces of
	// knowledge that persist across conversations and restarts.
	factStore, err := knowledge.NewStore(cfg.DataDir+"/knowledge.db", logger)
	if err != nil {
		return nil, fmt.Errorf("open fact store: %w", err)
	}
	a.factStore = factStore

	factTools := knowledge.NewTools(factStore)
	loop.Tools().SetFactTools(factTools)
	logger.Info("fact store initialized", "path", cfg.DataDir+"/knowledge.db")

	// --- Contact directory ---
	// Structured storage for people and organizations. Separate database
	// from facts to keep concerns isolated.
	contactStore, err := contacts.NewStore(cfg.DataDir+"/contacts.db", logger)
	if err != nil {
		return nil, fmt.Errorf("open contact store: %w", err)
	}
	a.contactStore = contactStore

	// Wire summarizer → contact interaction tracking now that the
	// contact store is available. Register the callback before Start()
	// to avoid a race where the startup scan reads the field concurrently.
	summaryWorker.SetInteractionCallback(func(conversationID, sessionID string, endedAt time.Time, topics []string) {
		updateContactInteraction(contactStore, logger, conversationID, sessionID, endedAt, topics)
	})
	a.deferWorker("summary-worker", func(ctx context.Context) error {
		summaryWorker.Start(ctx)
		return nil
	})

	contactTools := contacts.NewTools(contactStore)
	if cfg.Identity.ContactName != "" {
		contactTools.SetSelfContactName(cfg.Identity.ContactName)
	}
	loop.Tools().SetContactTools(contactTools)
	logger.Info("contact store initialized", "path", cfg.DataDir+"/contacts.db")

	// --- Notifications ---
	// Push notifications via HA companion app. Requires both the HA client
	// and the contact store for recipient → device resolution.
	if a.ha != nil {
		a.notifSender = notifications.NewSender(a.ha, contactStore, opStore, cfg.MQTT.DeviceName, logger)
		loop.Tools().SetHANotifier(a.notifSender)
		logger.Info("HA notification sender initialized")

		var nrErr error
		a.notifRecords, nrErr = notifications.NewRecordStore(mem.DB(), logger)
		if nrErr != nil {
			return nil, fmt.Errorf("initialize notification record store: %w", nrErr)
		}
		loop.Tools().SetNotificationRecords(a.notifRecords)
		logger.Info("notification record store initialized")

		// Provider-agnostic notification router — wraps the HA push sender
		// behind a routing layer that selects delivery channel per recipient.
		a.notifRouter = notifications.NewNotificationRouter(contactStore, a.notifRecords, logger)
		a.notifRouter.RegisterProvider(notifications.NewHAPushProvider(a.notifSender))
		a.notifRouter.SetActivitySource(&channelActivityAdapter{
			loops: &channelLoopAdapter{registry: loopRegistry},
			store: contactStore,
		})
		loop.Tools().SetNotificationRouter(a.notifRouter)
		logger.Info("notification router initialized", "providers", "ha_push")
	}

	// --- Email ---
	// Native IMAP/SMTP email. Replaces the MCP email server approach
	// with direct IMAP connections for reading and SMTP for sending,
	// supporting multiple accounts with trust zone gating.
	if cfg.Email.Configured() {
		emailMgr := email.NewManager(cfg.Email, logger)
		a.emailMgr = emailMgr

		emailTools := email.NewTools(emailMgr, &emailContactResolver{store: contactStore})
		loop.Tools().SetEmailTools(emailTools)

		// Register each account with connwatch for health monitoring.
		for _, name := range emailMgr.AccountNames() {
			acctName := name // capture for closure
			acct, _ := emailMgr.Account(acctName)
			connMgr.Watch(ctx, connwatch.WatcherConfig{
				Name:    "email-" + acctName,
				Probe:   func(pCtx context.Context) error { return acct.Ping(pCtx) },
				Backoff: connwatch.DefaultBackoffConfig(),
				Logger:  logger,
			})
		}

		// --- Email polling ---
		// Periodic IMAP check for new messages via the loop infrastructure.
		// The handler checks UIDs against a high-water mark and dispatches
		// an agent conversation only when new mail is detected.
		if cfg.Email.PollIntervalSec > 0 {
			poller := email.NewPoller(emailMgr, opStore, logger)
			pollInterval := time.Duration(cfg.Email.PollIntervalSec) * time.Second
			loopCfg := looppkg.Config{
				Name:         "email-poller",
				SleepMin:     pollInterval,
				SleepMax:     pollInterval,
				SleepDefault: pollInterval,
				Jitter:       looppkg.Float64Ptr(0),
				Handler:      emailPollHandler(poller, loop, logger),
				Metadata: map[string]string{
					"subsystem": "email",
				},
			}
			loopDeps := looppkg.Deps{
				Logger:   logger,
				EventBus: eventBus,
			}
			a.deferWorker("email-poller", func(ctx context.Context) error {
				if _, err := loopRegistry.SpawnLoop(ctx, loopCfg, loopDeps); err != nil {
					return fmt.Errorf("spawn email poller loop: %w", err)
				}
				return nil
			})
		}

		logger.Info("email enabled", "accounts", emailMgr.AccountNames(), "poll_interval", cfg.Email.PollIntervalSec)
	} else {
		logger.Info("email disabled (not configured)")
	}

	// --- Forge integration ---
	// Native GitHub (and future Gitea/GitLab) integration. Replaces the
	// MCP github server with direct API calls via go-github.
	var forgeOpLog *forge.OperationLog
	if cfg.Forge.Configured() {
		var err error
		a.forgeMgr, err = forge.NewManager(cfg.Forge, logger)
		if err != nil {
			return nil, fmt.Errorf("create forge manager: %w", err)
		}

		forgeOpLog = forge.NewOperationLog()
		forgeTools := forge.NewTools(a.forgeMgr, forgeOpLog, logger)
		loop.Tools().SetForgeTools(forgeTools)

		logger.Info("forge enabled", "accounts", len(cfg.Forge.Accounts))
	} else {
		logger.Info("forge disabled (not configured)")
	}

	// --- Working memory tool ---
	// Gives the agent a read/write scratchpad for experiential context
	// that survives compaction. Auto-injected via context provider below.
	loop.Tools().SetWorkingMemoryStore(wmStore)

	// --- Fact extraction ---
	// Automatic extraction of facts from conversations. Runs async after
	// each interaction using a local model. Opt-in via config.
	if cfg.Extraction.Enabled {
		extractionModel := cfg.Extraction.Model
		logger.Info("fact extraction enabled", "model", extractionModel)

		// FactSetter adapter with confidence reinforcement: if a fact already
		// exists, bump its confidence rather than overwriting.
		factSetterAdapter := &factSetterFunc{store: factStore, logger: logger}

		extractor := memory.NewExtractor(factSetterAdapter, logger, cfg.Extraction.MinMessages)
		extractor.SetTimeout(time.Duration(cfg.Extraction.TimeoutSeconds) * time.Second)
		extractor.SetExtractFunc(func(ctx context.Context, userMsg, assistantResp string, history []memory.Message) (*memory.ExtractionResult, error) {
			// Build transcript from recent history (only complete messages).
			var transcript strings.Builder
			for _, m := range history {
				line := fmt.Sprintf("[%s] %s\n", m.Role, m.Content)
				if transcript.Len()+len(line) > 4000 {
					break
				}
				transcript.WriteString(line)
			}

			prompt := prompts.FactExtractionPrompt(userMsg, assistantResp, transcript.String())
			msgs := []llm.Message{{Role: "user", Content: prompt}}

			start := time.Now()
			resp, err := llmClient.Chat(ctx, extractionModel, msgs, nil)
			if err != nil {
				logger.Warn("fact extraction LLM call failed",
					"model", extractionModel,
					"elapsed_ms", time.Since(start).Milliseconds(),
					"error", err)
				return nil, err
			}
			logger.Debug("fact extraction LLM call complete",
				"model", extractionModel,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"response_len", len(resp.Message.Content))

			// Parse JSON (strip code fences, same pattern as metadata gen)
			content := resp.Message.Content
			content = strings.TrimPrefix(content, "```json\n")
			content = strings.TrimPrefix(content, "```\n")
			content = strings.TrimSuffix(content, "\n```")
			content = strings.TrimSpace(content)

			var result memory.ExtractionResult
			if err := json.Unmarshal([]byte(content), &result); err != nil {
				preview := content
				if len(preview) > 500 {
					preview = preview[:500]
				}
				logger.Debug("extraction JSON parse failed",
					"raw_response", preview)
				return nil, fmt.Errorf("parse extraction result: %w", err)
			}
			return &result, nil
		})

		loop.SetExtractor(extractor)
	}

	// --- Anticipation store ---
	// Bridges intent to action. The agent can set anticipations ("I expect
	// X to happen") that trigger context injection when they're fulfilled.
	// Shares the main thane.db connection.
	anticipationStore, err := scheduler.NewAnticipationStore(mem.DB())
	if err != nil {
		return nil, fmt.Errorf("create anticipation store: %w", err)
	}

	anticipationTools := scheduler.NewAnticipationTools(anticipationStore)
	loop.Tools().SetAnticipationTools(anticipationTools)
	logger.Info("anticipation store initialized")

	// --- Provenance store ---
	// Git-backed file storage with SSH signature enforcement. When
	// configured, identity files (ego.md, metacognitive.md) are
	// auto-committed with cryptographic signatures on every write.
	if cfg.Provenance.Configured() {
		keyPath := paths.ExpandHome(cfg.Provenance.SigningKey)
		signer, err := provenance.NewSSHFileSigner(keyPath)
		if err != nil {
			return nil, fmt.Errorf("load provenance signing key %s: %w", keyPath, err)
		}
		storePath := paths.ExpandHome(cfg.Provenance.Path)
		a.provenanceStore, err = provenance.New(storePath, signer, logger)
		if err != nil {
			return nil, fmt.Errorf("init provenance store at %s: %w", storePath, err)
		}
		logger.Info("provenance store initialized",
			"path", storePath,
			"public_key", signer.PublicKey(),
		)
	}

	// --- Attachment store ---
	// Content-addressed file storage with SHA-256 deduplication.
	// When configured, channels (Signal, email) store attachments
	// by content hash with a SQLite metadata index.
	if cfg.Attachments.StoreDir != "" {
		storeDir := paths.ExpandHome(cfg.Attachments.StoreDir)
		attachDbPath := filepath.Join(cfg.DataDir, "attachments.db")
		var err error
		a.attachmentStore, err = attachments.NewStore(attachDbPath, storeDir, logger)
		if err != nil {
			return nil, fmt.Errorf("init attachment store: %w", err)
		}
		logger.Info("attachment store initialized",
			"db", attachDbPath,
			"store_dir", storeDir,
		)
	}

	// --- Vision analyzer ---
	// When both the attachment store and vision config are enabled,
	// images are automatically analyzed on ingest using a vision-capable
	// LLM. Results are cached in the attachment metadata index.
	if a.attachmentStore != nil && cfg.Attachments.Vision.Enabled {
		a.visionAnalyzer = attachments.NewAnalyzer(a.attachmentStore, attachments.AnalyzerConfig{
			Client:  llmClient,
			Model:   cfg.Attachments.Vision.Model,
			Prompt:  cfg.Attachments.Vision.Prompt,
			Timeout: cfg.Attachments.Vision.ParsedTimeout(),
			Logger:  logger,
		})
		logger.Info("vision analyzer enabled",
			"model", cfg.Attachments.Vision.Model,
			"timeout", cfg.Attachments.Vision.ParsedTimeout(),
		)
	}

	// --- Attachment tools ---
	// When the attachment store is configured, the agent can list,
	// search, and describe attachments. Vision analysis is available
	// when the analyzer is also configured.
	if a.attachmentStore != nil {
		attachmentTools := attachments.NewTools(a.attachmentStore, a.visionAnalyzer)
		loop.Tools().SetAttachmentTools(attachmentTools)
		logger.Info("attachment tools registered")
	}

	// --- File tools ---
	// When a workspace path is configured, the agent can read and write
	// files within that directory. All paths are sandboxed.
	if cfg.Workspace.Path != "" {
		fileTools := tools.NewFileTools(cfg.Workspace.Path, cfg.Workspace.ReadOnlyDirs)
		if resolver != nil {
			fileTools.SetResolver(resolver)
		}
		loop.Tools().SetFileTools(fileTools)

		// Ego file: prefer provenance store path, fall back to workspace.
		if a.provenanceStore != nil {
			loop.SetEgoFile(a.provenanceStore.FilePath("ego.md"))
			loop.SetProvenanceStore(a.provenanceStore)
			logger.Info("ego.md backed by provenance store")
		} else {
			egoPath := filepath.Join(cfg.Workspace.Path, "ego.md")
			if resolver != nil {
				if resolved, err := resolver.Resolve("core:ego.md"); err != nil {
					logger.Warn("failed to resolve core:ego.md, using default",
						"error", err,
						"default_path", egoPath,
					)
				} else {
					egoPath = resolved
				}
			}
			loop.SetEgoFile(egoPath)
		}
		logger.Info("file tools enabled", "workspace", cfg.Workspace.Path)
	} else {
		logger.Info("file tools disabled (no workspace path configured)")
	}

	// --- Temp file store ---
	// Provides create_temp_file tool for orchestrator-delegate data passing.
	// Files are stored in the workspace's .tmp subdirectory and cleaned up
	// when conversations end. Requires both workspace and opstate.
	if cfg.Workspace.Path != "" {
		tempFileStore := tools.NewTempFileStore(
			filepath.Join(cfg.Workspace.Path, ".tmp"),
			opStore,
			logger,
		)
		loop.Tools().SetTempFileStore(tempFileStore)
		logger.Info("temp file store enabled",
			"base_dir", filepath.Join(cfg.Workspace.Path, ".tmp"),
		)
	}

	// --- Universal content resolution ---
	// Wire prefix-to-content resolution into the tool registry so that bare
	// prefix references (temp:LABEL, kb:file.md, etc.) in any tool's string
	// arguments are automatically replaced with file content before the
	// handler runs. File tools opt out via SkipContentResolve (they need
	// the path, not the content).
	cr := tools.NewContentResolver(resolver, loop.Tools().TempFileStore(), logger)
	if cr != nil {
		loop.Tools().SetContentResolver(cr)
		logger.Info("content resolver enabled for tool arguments")
	}

	// --- Usage recording ---
	// Wire persistent token usage recording into the agent loop and
	// register the cost_summary tool so the agent can query its own spend.
	loop.SetUsageRecorder(usageStore, cfg.Pricing)
	loop.Tools().SetUsageStore(usageStore)

	// --- Log index query ---
	// Expose the structured log index so the agent can query its own
	// logs for self-diagnostics and forensics.
	if a.indexDB != nil {
		loop.Tools().SetLogIndexDB(a.indexDB)
	}

	// --- Shell exec ---
	// Optional and disabled by default. When enabled, the agent can
	// execute shell commands on the host, subject to allow/deny lists.
	if cfg.ShellExec.Enabled {
		shellCfg := tools.ShellExecConfig{
			Enabled:        true,
			WorkingDir:     cfg.ShellExec.WorkingDir,
			AllowedCmds:    cfg.ShellExec.AllowedPrefixes,
			DeniedCmds:     cfg.ShellExec.DeniedPatterns,
			DefaultTimeout: time.Duration(cfg.ShellExec.DefaultTimeoutSec) * time.Second,
		}
		if len(shellCfg.DeniedCmds) == 0 {
			shellCfg.DeniedCmds = tools.DefaultShellExecConfig().DeniedCmds
		}
		shellExec := tools.NewShellExec(shellCfg)
		loop.Tools().SetShellExec(shellExec)
		logger.Info("shell exec enabled", "working_dir", cfg.ShellExec.WorkingDir)
	} else {
		logger.Info("shell exec disabled")
	}

	// --- Web Search ---
	// Optional web search tool. Supports multiple providers; the first
	// configured provider becomes the default if none is specified.
	if cfg.Search.Configured() {
		primary := cfg.Search.Default
		mgr := search.NewManager(primary)

		if cfg.Search.SearXNG.Configured() {
			mgr.Register(search.NewSearXNG(cfg.Search.SearXNG.URL))
			if primary == "" {
				primary = "searxng"
			}
		}
		if cfg.Search.Brave.Configured() {
			mgr.Register(search.NewBrave(cfg.Search.Brave.APIKey))
			if primary == "" {
				primary = "brave"
			}
		}

		// Re-create manager with resolved primary if it was empty.
		if cfg.Search.Default == "" && primary != "" {
			mgr = search.NewManager(primary)
			if cfg.Search.SearXNG.Configured() {
				mgr.Register(search.NewSearXNG(cfg.Search.SearXNG.URL))
			}
			if cfg.Search.Brave.Configured() {
				mgr.Register(search.NewBrave(cfg.Search.Brave.APIKey))
			}
		}

		loop.Tools().SetSearchManager(mgr)
		logger.Info("web search enabled", "primary", primary, "providers", mgr.Providers())
	} else {
		logger.Warn("web search disabled (no providers configured)")
	}

	// --- Web Fetch ---
	// Always available — no configuration needed. Fetches web pages and
	// extracts readable text content.
	loop.Tools().SetFetcher(search.NewFetcher())

	// --- Media transcript ---
	// Wraps yt-dlp for on-demand transcript retrieval from YouTube,
	// Vimeo, podcasts, and other supported sources.
	ytdlpPath := cfg.Media.YtDlpPath
	if ytdlpPath == "" {
		ytdlpPath, _ = exec.LookPath("yt-dlp")
	}
	if ytdlpPath != "" {
		mc := media.New(media.Config{
			YtDlpPath:          ytdlpPath,
			CookiesFile:        cfg.Media.CookiesFile,
			CookiesFromBrowser: cfg.Media.CookiesFromBrowser,
			SubtitleLanguage:   cfg.Media.SubtitleLanguage,
			MaxTranscriptChars: cfg.Media.MaxTranscriptChars,
			WhisperModel:       cfg.Media.WhisperModel,
			TranscriptDir:      cfg.Media.TranscriptDir,
			OllamaURL:          cfg.Models.OllamaURL,
		}, logger)

		// Wire up LLM summarization for map-reduce transcript processing.
		// Uses a local model via router for chunk summarization.
		mc.SetSummarizer(func(ctx context.Context, prompt string) (string, error) {
			hints := map[string]string{
				router.HintMission:      "background",
				router.HintLocalOnly:    "true",
				router.HintQualityFloor: "3",
				router.HintPreferSpeed:  "true",
			}
			if cfg.Media.SummarizeModel != "" {
				hints[router.HintModelPreference] = cfg.Media.SummarizeModel
			}
			model, _ := rtr.Route(ctx, router.Request{
				Query:    "transcript summarization",
				Priority: router.PriorityBackground,
				Hints:    hints,
			})
			msgs := []llm.Message{{Role: "user", Content: prompt}}
			resp, err := llmClient.Chat(ctx, model, msgs, nil)
			if err != nil {
				return "", err
			}
			return resp.Message.Content, nil
		})

		loop.Tools().SetMediaClient(mc)
		logger.Info("media_transcript enabled", "yt_dlp", ytdlpPath)
	} else {
		logger.Warn("media_transcript disabled (yt-dlp not found)")
	}

	// --- Media feed tools ---
	// Feed management tools (media_follow, media_unfollow, media_feeds)
	// are always registered so the agent can manage feeds. Feed polling
	// is a separate concern controlled by FeedCheckInterval.
	feedTools := media.NewFeedTools(opStore, logger, cfg.Media.MaxFeeds)
	loop.Tools().SetMediaFeedTools(feedTools)

	// --- Media analysis tools ---
	// The media_save_analysis tool lets the agent persist structured
	// analysis to an Obsidian-compatible vault and track engagement.
	// It requires either a per-feed output_path or the global default.
	// If the engagement store fails to open, the tool is still registered
	// without engagement tracking (vault writes still work).
	a.mediaStore, err = media.NewMediaStore(cfg.Media.Analysis.DatabasePath, logger)
	if err != nil {
		logger.Warn("media engagement store unavailable; analysis will persist to vault only", "error", err)
	}
	vaultWriter := media.NewVaultWriter(logger)
	analysisTools := media.NewAnalysisTools(
		opStore, a.mediaStore, vaultWriter,
		cfg.Media.Analysis.DefaultOutputPath, logger,
	)
	loop.Tools().SetMediaAnalysisTools(analysisTools)

	// --- Media feed polling ---
	// Periodic RSS/Atom check for new entries via the loop infrastructure.
	// The handler checks feeds against high-water marks and dispatches an
	// agent conversation only when new content is detected.
	if cfg.Media.FeedCheckInterval > 0 {
		feedPoller := media.NewFeedPoller(opStore, logger)
		pollInterval := time.Duration(cfg.Media.FeedCheckInterval) * time.Second
		loopCfg := looppkg.Config{
			Name:         "media-feed-poller",
			SleepMin:     pollInterval,
			SleepMax:     pollInterval,
			SleepDefault: pollInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Handler:      mediaFeedHandler(feedPoller, loop, logger),
			Metadata: map[string]string{
				"subsystem": "media",
			},
		}
		loopDeps := looppkg.Deps{
			Logger:   logger,
			EventBus: eventBus,
		}
		a.deferWorker("media-feed-poller", func(ctx context.Context) error {
			if _, err := loopRegistry.SpawnLoop(ctx, loopCfg, loopDeps); err != nil {
				return fmt.Errorf("spawn media feed poller loop: %w", err)
			}
			return nil
		})

		logger.Info("media feed polling enabled",
			"interval", pollInterval,
			"max_feeds", cfg.Media.MaxFeeds,
		)
	}

	// --- Archive tools ---
	// Gives the agent the ability to search and recall past conversations.
	loop.Tools().SetArchiveStore(archiveStore)
	loop.Tools().SetConversationResetter(loop)
	loop.Tools().SetSessionManager(loop)

	// --- Embeddings ---
	// Optional semantic search over fact and contact stores. When enabled,
	// records are indexed with vector embeddings generated by a local model.
	// The client is declared here so it's available to context providers below.
	var embClient *knowledge.Client
	if cfg.Embeddings.Enabled {
		embClient = knowledge.New(knowledge.Config{
			BaseURL: cfg.Embeddings.BaseURL,
			Model:   cfg.Embeddings.Model,
		})
		factTools.SetEmbeddingClient(embClient)
		contactTools.SetEmbeddingClient(embClient)
		logger.Info("embeddings enabled", "model", cfg.Embeddings.Model, "url", cfg.Embeddings.BaseURL)
	}

	// --- MCP servers ---
	// Connect to configured MCP servers and bridge their tools into the
	// registry. This must happen before delegate executor creation so
	// delegates have access to MCP tools.
	for _, serverCfg := range cfg.MCP.Servers {
		var transport mcp.Transport
		switch serverCfg.Transport {
		case "stdio":
			transport = mcp.NewStdioTransport(mcp.StdioConfig{
				Command: serverCfg.Command,
				Args:    serverCfg.Args,
				Env:     serverCfg.Env,
				Logger:  logger,
			})
		case "http":
			transport = mcp.NewHTTPTransport(mcp.HTTPConfig{
				URL:     serverCfg.URL,
				Headers: serverCfg.Headers,
				Logger:  logger,
			})
		}

		client := mcp.NewClient(serverCfg.Name, transport, logger)

		initCtx, initCancel := context.WithTimeout(ctx, 30*time.Second)
		err := client.Initialize(initCtx)
		initCancel()
		if err != nil {
			logger.Error("MCP server initialization failed",
				"server", serverCfg.Name,
				"error", err,
			)
			client.Close()
			continue
		}

		bridgeCtx, bridgeCancel := context.WithTimeout(ctx, 30*time.Second)
		count, err := mcp.BridgeTools(
			bridgeCtx,
			client, serverCfg.Name, loop.Tools(),
			serverCfg.IncludeTools, serverCfg.ExcludeTools,
			logger,
		)
		bridgeCancel()
		if err != nil {
			logger.Error("MCP tool bridge failed",
				"server", serverCfg.Name,
				"error", err,
			)
			client.Close()
			continue
		}

		a.mcpClients = append(a.mcpClients, client)

		connMgr.Watch(ctx, connwatch.WatcherConfig{
			Name:    "mcp-" + serverCfg.Name,
			Probe:   func(pCtx context.Context) error { return client.Ping(pCtx) },
			Backoff: connwatch.DefaultBackoffConfig(),
			Logger:  logger,
		})

		logger.Info("MCP server connected",
			"server", serverCfg.Name,
			"tools", count,
		)
	}

	// --- Signal message bridge ---
	// Launches a native signal-cli jsonRpc subprocess and receives
	// messages event-driven, routing them through the agent loop.
	if cfg.Signal.Configured() {
		signalArgs := append([]string{"-a", cfg.Signal.Account, "jsonRpc"}, cfg.Signal.Args...)
		signalClient := sigcli.NewClient(cfg.Signal.Command, signalArgs, logger)
		if err := signalClient.Start(ctx); err != nil {
			logger.Error("signal-cli start failed", "error", err)
		} else {
			a.signalClient = signalClient

			// Register signal_send_message tool so the agent can
			// send messages during its tool loop.
			loop.Tools().Register(&tools.Tool{
				Name:        "signal_send_message",
				Description: "Send a Signal message to a phone number. Use this to reply to the user's Signal message or initiate a new Signal conversation.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"recipient": map[string]any{
							"type":        "string",
							"description": "Phone number including country code (e.g., +15551234567)",
						},
						"message": map[string]any{
							"type":        "string",
							"description": "Message text to send",
						},
					},
					"required": []string{"recipient", "message"},
				},
				Handler: func(toolCtx context.Context, args map[string]any) (string, error) {
					recipient, _ := args["recipient"].(string)
					message, _ := args["message"].(string)
					if recipient == "" || message == "" {
						return "", fmt.Errorf("recipient and message are required")
					}
					_, err := signalClient.Send(toolCtx, recipient, message)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("Message sent to %s", recipient), nil
				},
			})

			idleTimeout := time.Duration(cfg.Signal.SessionIdleMinutes) * time.Minute
			var signalRotator sigcli.SessionRotator
			if idleTimeout > 0 {
				signalRotator = &signalSessionRotator{
					loop:      loop,
					llmClient: llmClient,
					router:    rtr,
					sender:    &signalChannelSender{client: signalClient},
					archiver:  archiveAdapter,
					logger:    logger,
				}
			}

			bridge := sigcli.NewBridge(sigcli.BridgeConfig{
				Client:        signalClient,
				Runner:        loop,
				Logger:        logger,
				RateLimit:     cfg.Signal.RateLimitPerMinute,
				HandleTimeout: cfg.Signal.HandleTimeout,
				Routing:       cfg.Signal.Routing,
				Rotator:       signalRotator,
				IdleTimeout:   idleTimeout,
				Resolver:      &contactPhoneResolver{store: contactStore},
				Attachments: sigcli.AttachmentConfig{
					SourceDir: cfg.Signal.AttachmentSourceDir,
					DestDir:   cfg.Signal.AttachmentDir,
					MaxSize:   cfg.Signal.MaxAttachmentSize,
				},
				AttachmentStore: a.attachmentStore,
				VisionAnalyzer:  a.visionAnalyzer,
				Registry:        loopRegistry,
				EventBus:        eventBus,
			})
			if err := bridge.Register(ctx); err != nil {
				logger.Error("signal bridge registration failed", "error", err)
			}
			a.signalBridge = bridge

			// Register signal_send_reaction tool so the agent can
			// react to Signal messages with emoji.
			loop.Tools().Register(&tools.Tool{
				Name:        "signal_send_reaction",
				Description: "React to a Signal message with an emoji. Use this to acknowledge messages or express reactions. The target_timestamp identifies which message to react to — use the [ts:...] value from the message, or \"latest\" to react to the most recent message from the recipient.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"recipient": map[string]any{
							"type":        "string",
							"description": "Phone number including country code (e.g., +15551234567)",
						},
						"emoji": map[string]any{
							"type":        "string",
							"description": "Reaction emoji (e.g., 👍, ❤️, 😂)",
						},
						"target_author": map[string]any{
							"type":        "string",
							"description": "Phone number of the message author to react to",
						},
						"target_timestamp": map[string]any{
							"type":        "string",
							"description": "Timestamp of the message to react to (from [ts:...] tag) as a numeric string, or \"latest\" for the most recent inbound message from the recipient",
						},
					},
					"required": []string{"recipient", "emoji", "target_author", "target_timestamp"},
				},
				Handler: func(toolCtx context.Context, args map[string]any) (string, error) {
					recipient, _ := args["recipient"].(string)
					emoji, _ := args["emoji"].(string)
					targetAuthor, _ := args["target_author"].(string)

					if recipient == "" || emoji == "" || targetAuthor == "" {
						return "", fmt.Errorf("recipient, emoji, and target_author are required")
					}

					var targetTS int64
					switch v := args["target_timestamp"].(type) {
					case string:
						if v == "latest" {
							ts, ok := bridge.LastInboundTimestamp(recipient)
							if !ok {
								return "", fmt.Errorf("no recent inbound message from %s to react to", recipient)
							}
							targetTS = ts
						} else {
							// Accept numeric strings (LLMs often serialize large ints as strings).
							n, err := strconv.ParseInt(v, 10, 64)
							if err != nil {
								return "", fmt.Errorf("target_timestamp must be a numeric string or \"latest\", got %q", v)
							}
							targetTS = n
						}
					case float64:
						targetTS = int64(v)
					default:
						return "", fmt.Errorf("target_timestamp must be a string (numeric or \"latest\")")
					}

					if err := signalClient.SendReaction(toolCtx, recipient, emoji, targetAuthor, targetTS, false); err != nil {
						return "", err
					}
					return fmt.Sprintf("Reacted with %s to message from %s", emoji, targetAuthor), nil
				},
			})

			connMgr.Watch(ctx, connwatch.WatcherConfig{
				Name:    "signal",
				Probe:   func(pCtx context.Context) error { return signalClient.Ping(pCtx) },
				Backoff: connwatch.DefaultBackoffConfig(),
				Logger:  logger,
			})

			// Register Signal as a notification delivery channel so the
			// notification router can route to Signal when the contact
			// has an active Signal session.
			if a.notifRouter != nil {
				sp := notifications.NewSignalProvider(
					signalClient, contactStore, logger,
				)
				sp.SetRecorder(&signalMemoryRecorder{mem: mem})
				a.notifRouter.RegisterProvider(sp)
				logger.Info("signal notification provider registered")
			}

			logger.Info("signal bridge started",
				"command", cfg.Signal.Command,
				"account", cfg.Signal.Account,
				"rate_limit", cfg.Signal.RateLimitPerMinute,
				"session_idle_timeout", idleTimeout,
			)
		}
	}

	// --- Delegation ---
	// Register thane_delegate tool AFTER all other tools so the delegate
	// executor's parent registry snapshot includes the full tool set.
	delegateExec := delegate.NewExecutor(logger, llmClient, rtr, loop.Tools(), cfg.Models.Default)
	if len(cfg.Delegate.Profiles) > 0 {
		overrides := make(map[string]delegate.ProfileOverride, len(cfg.Delegate.Profiles))
		for name, pc := range cfg.Delegate.Profiles {
			overrides[name] = delegate.ProfileOverride{
				ToolTimeout: pc.ToolTimeout,
				MaxDuration: pc.MaxDuration,
				MaxIter:     pc.MaxIter,
				MaxTokens:   pc.MaxTokens,
			}
		}
		delegateExec.ApplyProfileOverrides(overrides)
	}
	delegateExec.SetTimezone(cfg.Timezone)
	if a.contentWriter != nil {
		delegateExec.SetContentWriter(a.contentWriter)
	}
	delegateExec.SetArchiver(archiveStore)
	delegateExec.SetUsageRecorder(usageStore, cfg.Pricing)
	delegateExec.SetEventBus(eventBus)
	var alwaysActiveTags []string
	for tag, tagCfg := range cfg.CapabilityTags {
		if tagCfg.AlwaysActive {
			alwaysActiveTags = append(alwaysActiveTags, tag)
		}
	}
	if len(alwaysActiveTags) > 0 {
		delegateExec.SetAlwaysActiveTags(alwaysActiveTags)
	}
	if a.forgeMgr != nil {
		delegateExec.SetForgeContext(a.forgeMgr.Context())
	}
	if tfs := loop.Tools().TempFileStore(); tfs != nil {
		delegateExec.SetTempFileStore(tfs)
	}
	loop.Tools().Register(&tools.Tool{
		Name:        "thane_delegate",
		Description: delegate.ToolDescription,
		Parameters:  delegate.ToolDefinition(),
		Handler:     delegate.ToolHandler(delegateExec),
	})
	a.delegateExec = delegateExec
	logger.Info("delegation enabled", "profiles", delegateExec.ProfileNames())

	// --- Notification callback routing ---
	// Wire up the callback dispatcher and timeout watcher for actionable
	// notifications. Requires both the notification record store and the
	// delegate executor (for spawning responses when the session is gone).
	if a.notifRecords != nil {
		sessionInj := &notifSessionInjector{mem: mem, archiver: archiveAdapter}
		delegateSpn := &notifDelegateSpawner{exec: delegateExec}
		a.notifCallbackDispatcher = notifications.NewCallbackDispatcher(
			a.notifRecords, sessionInj, delegateSpn, cfg.MQTT.DeviceName, logger,
		)

		// Use the router for escalation so timeout_action: "escalate"
		// respects per-recipient routing preferences. Falls back to the
		// raw HA sender when the router is unavailable.
		var escalationSender notifications.EscalationSender
		if a.notifRouter != nil {
			escalationSender = a.notifRouter
		} else if a.notifSender != nil {
			escalationSender = a.notifSender
		}
		timeoutWatcher := notifications.NewTimeoutWatcher(
			a.notifRecords, a.notifCallbackDispatcher, escalationSender,
			30*time.Second, logger,
		)
		a.deferWorker("notification-timeout-watcher", func(ctx context.Context) error {
			go timeoutWatcher.Start(ctx)
			return nil
		})
		loop.Tools().SetCallbackDispatcher(a.notifCallbackDispatcher)

		// Synchronous escalation support — allows tools to block
		// waiting for human responses via any notification channel.
		escalationWaiter := notifications.NewResponseWaiter()
		a.notifCallbackDispatcher.SetResponseWaiter(escalationWaiter)
		loop.Tools().SetEscalationTools(tools.EscalationDeps{
			Router:     a.notifRouter,
			Records:    a.notifRecords,
			Dispatcher: a.notifCallbackDispatcher,
			Waiter:     escalationWaiter,
		})

		logger.Info("notification callback dispatcher and timeout watcher initialized")
	}

	// --- Orchestrator tool gating ---
	// When delegation_required is true, the agent loop only sees
	// lightweight tools (delegate + memory), steering the primary model
	// toward delegation instead of direct tool use.
	if cfg.Agent.DelegationRequired {
		loop.SetOrchestratorTools(cfg.Agent.OrchestratorTools)
		logger.Info("orchestrator tool gating enabled", "tools", cfg.Agent.OrchestratorTools)
	}

	// --- Capability tags ---
	// Tag-driven tool and talent filtering. When configured, tools and
	// talents are grouped into named capabilities that can be activated
	// per-conversation via activate_capability/deactivate_capability tools.
	if len(cfg.CapabilityTags) > 0 {
		// parsedTalents was loaded above; copy the slice header so the
		// manifest prepend below doesn't modify the outer variable.
		capTalents := append([]talents.Talent(nil), parsedTalents...)

		// Warn about tools referenced in config but not registered.
		// This catches typos, missing MCP servers, and tools gated by config
		// (e.g., shell_exec disabled). Non-fatal: skip the missing tool.
		for tag, tagCfg := range cfg.CapabilityTags {
			for _, toolName := range tagCfg.Tools {
				if loop.Tools().Get(toolName) == nil {
					logger.Warn("capability tag references unregistered tool",
						"tag", tag, "tool", toolName)
				}
			}
		}

		// Build the shared tag context assembler early so KB article
		// counts are available for the manifest. It merges two sources
		// per active tag: tagged KB articles (frontmatter tags: [forge])
		// and live providers.
		var kbDir string
		if resolver != nil {
			resolved, err := resolver.Resolve("kb:")
			if err == nil {
				kbDir = resolved
			}
		}

		tagCtxAssembler := agent.NewTagContextAssembler(agent.TagContextAssemblerConfig{
			CapTags:  cfg.CapabilityTags,
			KBDir:    kbDir,
			HAInject: loop.HAInject(),
			Logger:   logger.With("component", "tag_context"),
		})

		// Register forge as a tag context provider so its account
		// config and recent operations appear/disappear with the
		// forge capability tag.
		if a.forgeMgr != nil {
			loop.RegisterTagContextProvider("forge", forge.NewContextProvider(a.forgeMgr, forgeOpLog))
		}

		// Build manifest entries with enriched context info.
		kbCounts := tagCtxAssembler.KBArticleTags()
		liveProviders := loop.TagContextProviders()

		tagIndex := make(map[string][]string, len(cfg.CapabilityTags))
		descriptions := make(map[string]string, len(cfg.CapabilityTags))
		alwaysActive := make(map[string]bool, len(cfg.CapabilityTags))
		for tag, tagCfg := range cfg.CapabilityTags {
			tagIndex[tag] = tagCfg.Tools
			descriptions[tag] = tagCfg.Description
			alwaysActive[tag] = tagCfg.AlwaysActive
		}
		manifest := tools.BuildCapabilityManifest(tagIndex, descriptions, alwaysActive)

		manifestEntries := make([]talents.ManifestEntry, len(manifest))
		for i, m := range manifest {
			manifestEntries[i] = talents.ManifestEntry{
				Tag:          m.Tag,
				Description:  m.Description,
				Tools:        m.Tools,
				AlwaysActive: m.AlwaysActive,
				KBArticles:   kbCounts[m.Tag],
				LiveContext:  liveProviders[m.Tag] != nil,
			}
		}

		// Discover ad-hoc tags from KB articles and talents that aren't
		// in the config. These can be activated at runtime to load their
		// tagged content without requiring config changes.
		configuredTags := make(map[string]bool, len(cfg.CapabilityTags))
		for tag := range cfg.CapabilityTags {
			configuredTags[tag] = true
		}
		adHocTags := make(map[string]bool)
		for tag := range kbCounts {
			if !configuredTags[tag] {
				adHocTags[tag] = true
			}
		}
		for _, t := range capTalents {
			for _, tag := range t.Tags {
				if !configuredTags[tag] {
					adHocTags[tag] = true
				}
			}
		}
		for tag := range adHocTags {
			manifestEntries = append(manifestEntries, talents.ManifestEntry{
				Tag:        tag,
				AdHoc:      true,
				KBArticles: kbCounts[tag],
			})
		}

		if manifestTalent := talents.GenerateManifest(manifestEntries); manifestTalent != nil {
			capTalents = append([]talents.Talent{*manifestTalent}, capTalents...)
		}

		loop.SetCapabilityTags(cfg.CapabilityTags, capTalents)
		loop.Tools().SetCapabilityTools(loop, manifest)
		loop.SetTagContextAssembler(tagCtxAssembler)
		loop.SetCapabilityTagStore(agent.NewOpstateCapabilityTagStore(opStore))

		// Behavioral lenses are wired below (outside this block)
		// so they work even without capability_tags configured.

		// Expose the agent loop's active tags to every process loop
		// spawned through the registry so the dashboard can display
		// dynamically activated capabilities.
		loopRegistry.SetDefaultActiveTagsFunc(func() []string {
			tags := loop.LastRunTags()
			if tags == nil {
				return nil
			}
			result := make([]string, 0, len(tags))
			for t := range tags {
				result = append(result, t)
			}
			sort.Strings(result)
			return result
		})

		// Wire tag context into delegates. The closure captures both the
		// assembler and the loop so delegates always see the latest
		// registered providers at call time (not a stale snapshot from
		// construction time).
		delegateExec.SetTagContextFunc(func(ctx context.Context, activeTags map[string]bool) string {
			return tagCtxAssembler.Build(ctx, activeTags, loop.TagContextProviders())
		})

		var activeTags []string
		for tag := range loop.LastRunTags() {
			activeTags = append(activeTags, tag)
		}
		logger.Info("capability tags enabled",
			"tags", len(cfg.CapabilityTags),
			"always_active", activeTags,
			"talents", len(parsedTalents),
			"kb_tagged_articles", kbCounts,
		)
	}

	if len(cfg.ChannelTags) > 0 {
		loop.SetChannelTags(cfg.ChannelTags)
		logger.Info("channel tags configured", "channels", len(cfg.ChannelTags))
	}

	// --- Behavioral lenses ---
	// Persistent global context modes backed by opstate. Active lenses
	// are merged into every Run's capability scope (and every delegate
	// execution) so their KB articles and talents load globally.
	// Wired unconditionally — lenses work even without capability_tags.
	lensStore := tools.NewLensStore(opStore)
	loop.Tools().SetLensTools(lensStore)
	lensProviderFn := func() []string {
		lenses, err := lensStore.ActiveLenses()
		if err != nil {
			logger.Warn("failed to load active lenses", "error", err)
			return nil
		}
		return lenses
	}
	loop.SetLensProvider(lensProviderFn)
	delegateExec.SetLensProvider(lensProviderFn)
	if lenses, _ := lensStore.ActiveLenses(); len(lenses) > 0 {
		logger.Info("active lenses loaded from opstate", "lenses", lenses)
	}

	// --- Context providers ---
	// Dynamic system prompt injection. Providers add context based on
	// current state (e.g., pending anticipations) before each LLM call.
	anticipationProvider := scheduler.NewAnticipationProvider(anticipationStore)
	contextProvider := agent.NewCompositeContextProvider(anticipationProvider)
	contextProvider.Add(agent.NewChannelProvider(&contactNameLookup{store: contactStore, logger: logger}))
	contextProvider.Add(awareness.NewChannelOverviewProvider(awareness.ChannelOverviewConfig{
		Loops:  &channelLoopAdapter{registry: loopRegistry},
		Phones: &contactPhoneResolver{store: contactStore},
		Hints:  tools.HintsFromContext,
		Logger: logger,
	}))

	episodicProvider := memory.NewEpisodicProvider(archiveStore, logger, memory.EpisodicConfig{
		Timezone:          cfg.Timezone,
		DailyDir:          cfg.Episodic.DailyDir,
		LookbackDays:      cfg.Episodic.LookbackDays,
		HistoryTokens:     cfg.Episodic.HistoryTokens,
		SessionGapMinutes: cfg.Episodic.SessionGapMinutes,
	})
	contextProvider.Add(episodicProvider)

	wmProvider := memory.NewWorkingMemoryProvider(wmStore, tools.ConversationIDFromContext)
	contextProvider.Add(wmProvider)

	// --- Entity watchlist ---
	// Allows the agent to dynamically add HA entities to a watched list
	// whose live state is injected into context each turn. Persisted in
	// SQLite so the watchlist survives restarts. Shares thane.db.
	watchlistStore, err := awareness.NewWatchlistStore(mem.DB())
	if err != nil {
		return nil, fmt.Errorf("watchlist store: %w", err)
	}

	if a.ha != nil {
		watchlistProvider := awareness.NewWatchlistProvider(watchlistStore, a.ha, logger)
		contextProvider.Add(watchlistProvider)

		// Register tag-scoped watchlist providers for entities added
		// with tags. Each distinct tag in the store gets a provider that
		// emits those entities only when the tag is active.
		if taggedTags, err := watchlistStore.DistinctTags(); err == nil && len(taggedTags) > 0 {
			for _, tag := range taggedTags {
				loop.RegisterTagContextProvider(tag,
					awareness.NewWatchlistTagProvider(tag, watchlistStore, a.ha, logger))
			}
			logger.Info("tagged watchlist entities registered",
				"tags", taggedTags)
		}

		logger.Info("entity watchlist context enabled")
	}

	loop.Tools().SetWatchlistStore(watchlistStore)

	// --- State change window ---
	// Maintains a rolling buffer of recent HA state changes, injected
	// into the system prompt on every agent run for ambient awareness.
	stateWindowLoc := time.Local
	if cfg.Timezone != "" {
		if parsed, err := time.LoadLocation(cfg.Timezone); err == nil {
			stateWindowLoc = parsed
		}
	}
	stateWindowProvider := awareness.NewStateWindowProvider(
		cfg.StateWindow.MaxEntries,
		time.Duration(cfg.StateWindow.MaxAgeMinutes)*time.Minute,
		stateWindowLoc,
		logger,
	)
	contextProvider.Add(stateWindowProvider)

	// --- Person tracker ---
	// Tracks configured household members' presence state and injects
	// it into the system prompt, eliminating tool calls for "who is
	// home?" queries. State updates arrive via the state watcher.
	//
	// Initialization from HA state happens in the connwatch OnReady
	// callback on each reconnect. However, if HA was already connected
	// before this tracker was constructed, OnReady would have skipped
	// initialization (personTracker was nil). Catch up immediately
	// after construction when HA is available. Initialize is idempotent,
	// so a redundant call from OnReady is harmless.
	if len(cfg.Person.Track) > 0 {
		personTracker = contacts.NewPresenceTracker(cfg.Person.Track, cfg.Timezone, logger)
		contextProvider.Add(personTracker)

		// Configure device MAC addresses from config.
		for entityID, devices := range cfg.Person.Devices {
			macs := make([]string, len(devices))
			for i, d := range devices {
				macs[i] = strings.ToLower(d.MAC)
			}
			personTracker.SetDeviceMACs(entityID, macs)
		}

		logger.Info("person tracking enabled", "entities", cfg.Person.Track)

		if a.ha != nil {
			initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
			if err := personTracker.Initialize(initCtx, a.ha); err != nil {
				logger.Warn("person tracker initial sync incomplete", "error", err)
			}
			initCancel()
		}
	}

	// --- UniFi room presence ---
	// Optional: polls UniFi controller for wireless client associations
	// and pushes room-level presence into the person tracker. Requires
	// both person.track and unifi config to be set.
	if cfg.Unifi.Configured() && personTracker != nil {
		unifiClient := unifi.NewClient(cfg.Unifi.URL, cfg.Unifi.APIKey, logger)

		// Build MAC → entity_id mapping from config.
		deviceOwners := make(map[string]string)
		for entityID, devices := range cfg.Person.Devices {
			for _, d := range devices {
				deviceOwners[strings.ToLower(d.MAC)] = entityID
			}
		}

		pollInterval := time.Duration(cfg.Unifi.PollIntervalSec) * time.Second
		poller := unifi.NewPoller(unifi.PollerConfig{
			Locator:      unifiClient,
			Updater:      personTracker,
			PollInterval: pollInterval,
			DeviceOwners: deviceOwners,
			APRooms:      cfg.Person.APRooms,
			Logger:       logger,
		})

		unifiLoopCfg := looppkg.Config{
			Name:         "unifi-poller",
			SleepMin:     pollInterval,
			SleepMax:     pollInterval,
			SleepDefault: pollInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Handler: func(ctx context.Context, _ any) error {
				return poller.Poll(ctx)
			},
			Metadata: map[string]string{
				"subsystem": "unifi",
			},
		}
		unifiLoopDeps := looppkg.Deps{
			Logger:   logger,
			EventBus: eventBus,
		}
		a.deferWorker("unifi-poller", func(ctx context.Context) error {
			if _, err := loopRegistry.SpawnLoop(ctx, unifiLoopCfg, unifiLoopDeps); err != nil {
				return fmt.Errorf("spawn unifi poller loop: %w", err)
			}
			return nil
		})

		// Register UniFi with connwatch for health endpoint visibility.
		connMgr.Watch(ctx, connwatch.WatcherConfig{
			Name:    "unifi",
			Probe:   func(pCtx context.Context) error { return unifiClient.Ping(pCtx) },
			Backoff: connwatch.DefaultBackoffConfig(),
			Logger:  logger,
		})

		logger.Info("unifi room presence enabled",
			"url", cfg.Unifi.URL,
			"poll_interval", pollInterval,
			"tracked_macs", len(deviceOwners),
			"ap_rooms", len(cfg.Person.APRooms),
		)
	} else if cfg.Unifi.Configured() && personTracker == nil {
		logger.Warn("unifi configured but person tracking disabled (no person.track entries)")
	}

	// Forge account context is now injected via tag context provider
	// (registered above in capability tag setup). It appears/disappears
	// with the forge capability tag instead of being always present.

	// Contact directory context — injects relevant contacts when the
	// user message mentions people or organizations. Uses semantic
	// search when embeddings are available; no-ops gracefully otherwise.
	var contactEmbedder contacts.EmbeddingClient
	if embClient != nil {
		contactEmbedder = embClient
	}
	contextProvider.Add(contacts.NewContextProvider(contactStore, contactEmbedder))

	// Subject-keyed fact injection — pre-warm cold-start loops with
	// facts keyed to specific entities, contacts, zones, etc.
	if cfg.Prewarm.Enabled {
		subjectProvider := knowledge.NewSubjectContextProvider(factStore, logger)
		if cfg.Prewarm.MaxFacts > 0 {
			subjectProvider.SetMaxFacts(cfg.Prewarm.MaxFacts)
		}
		contextProvider.Add(subjectProvider)
		logger.Info("context pre-warming enabled", "max_facts", cfg.Prewarm.MaxFacts)
	}

	// Archive retrieval injection — pre-warm cold-start loops with
	// relevant past conversation excerpts so the model has experiential
	// judgment alongside Layer 1 knowledge. See issue #404.
	if cfg.Prewarm.Enabled && cfg.Prewarm.Archive.Enabled {
		archiveProvider := memory.NewArchiveContextProvider(
			archiveStore,
			cfg.Prewarm.Archive.MaxResults,
			cfg.Prewarm.Archive.MaxBytes,
			logger,
		)
		contextProvider.Add(archiveProvider)
		logger.Info("archive pre-warming enabled",
			"max_results", cfg.Prewarm.Archive.MaxResults,
			"max_bytes", cfg.Prewarm.Archive.MaxBytes,
		)
	}

	loop.SetContextProvider(contextProvider)
	logger.Info("context providers initialized",
		"episodic_daily_dir", cfg.Episodic.DailyDir,
		"episodic_history_tokens", cfg.Episodic.HistoryTokens,
	)

	// --- State watcher ---
	// Consumes state_changed events from the HA WebSocket, bridges them
	// to the anticipation system via WakeContext, and triggers agent wakes
	// when an active anticipation matches the state change. Person entity
	// IDs are auto-merged into entity globs so the person tracker
	// receives state changes regardless of the user's subscribe config.
	if a.haWS != nil {
		globs := append([]string(nil), cfg.HomeAssistant.Subscribe.EntityGlobs...)
		if personTracker != nil {
			globs = append(globs, personTracker.EntityIDs()...)
		}
		filter := homeassistant.NewEntityFilter(globs, logger)
		limiter := homeassistant.NewEntityRateLimiter(cfg.HomeAssistant.Subscribe.RateLimitPerMinute)
		cooldown := time.Duration(cfg.HomeAssistant.Subscribe.CooldownMinutes) * time.Minute

		wakeCfg := WakeBridgeConfig{
			Store:    anticipationStore,
			Resolver: anticipationStore,
			Runner:   loop,
			Provider: anticipationProvider,
			Logger:   logger,
			Ctx:      ctx,
			Cooldown: cooldown,
		}
		if a.ha != nil {
			wakeCfg.HA = a.ha
		}
		bridge := NewWakeBridge(wakeCfg)

		// Compose handler: state window, person tracker, and wake bridge
		// all see every state change that passes the filter and rate limiter.
		var handler homeassistant.StateWatchHandler = bridge.HandleStateChange
		if personTracker != nil {
			bridgeHandler := handler
			handler = func(entityID, oldState, newState string) {
				personTracker.HandleStateChange(entityID, oldState, newState)
				bridgeHandler(entityID, oldState, newState)
			}
		}
		{
			prevHandler := handler
			handler = func(entityID, oldState, newState string) {
				stateWindowProvider.HandleStateChange(entityID, oldState, newState)
				prevHandler(entityID, oldState, newState)
			}
		}

		watcher := homeassistant.NewStateWatcher(a.haWS.Events(), filter, limiter, handler, logger)
		haEvents := watcher.Events()

		a.deferWorker("ha-state-watcher", func(ctx context.Context) error {
			// Derive a cancellable context so the loop exits cleanly
			// when the HA event channel closes.
			haLoopCtx, haLoopCancel := context.WithCancel(ctx)

			// Track last cleanup time for periodic rate-limiter maintenance.
			lastCleanup := time.Now()
			const haCleanupInterval = 5 * time.Minute
			const haBatchWindow = 1 * time.Second
			const haBatchMax = 100

			if _, err := loopRegistry.SpawnLoop(haLoopCtx, looppkg.Config{
				Name: "ha-state-watcher",
				WaitFunc: func(wCtx context.Context) (any, error) {
					// Block until at least one event arrives, then drain
					// up to haBatchMax events within haBatchWindow. This
					// debounces high-frequency HA state changes into one
					// loop iteration per batch.
					cleanupTimer := time.NewTimer(haCleanupInterval)
					defer cleanupTimer.Stop()

					var batch []homeassistant.Event

					// Wait for the first event (or cleanup/cancel).
					select {
					case <-wCtx.Done():
						return nil, wCtx.Err()
					case ev, ok := <-haEvents:
						if !ok {
							haLoopCancel()
							return nil, context.Canceled
						}
						batch = append(batch, ev)
					case <-cleanupTimer.C:
						watcher.CleanupRateLimiter()
						lastCleanup = time.Now()
						return nil, nil
					}

					// Drain additional events within the batch window.
					drainTimer := time.NewTimer(haBatchWindow)
					defer drainTimer.Stop()
				drain:
					for len(batch) < haBatchMax {
						select {
						case <-wCtx.Done():
							break drain
						case ev, ok := <-haEvents:
							if !ok {
								break drain
							}
							batch = append(batch, ev)
						case <-drainTimer.C:
							break drain
						}
					}

					return batch, nil
				},
				Handler: func(ctx context.Context, payload any) error {
					var processed int
					if batch, ok := payload.([]homeassistant.Event); ok {
						for _, ev := range batch {
							if watcher.HandleEvent(ev) {
								processed++
							}
						}
					}
					if time.Since(lastCleanup) > haCleanupInterval {
						watcher.CleanupRateLimiter()
						lastCleanup = time.Now()
					}
					if processed == 0 {
						return looppkg.ErrNoOp
					}
					if summary := looppkg.IterationSummary(ctx); summary != nil {
						summary["events_processed"] = processed
					}
					return nil
				},
				Metadata: map[string]string{
					"subsystem": "homeassistant",
					"category":  "listener",
				},
			}, looppkg.Deps{
				Logger:   logger,
				EventBus: eventBus,
			}); err != nil {
				return fmt.Errorf("spawn ha-state-watcher loop: %w", err)
			}
			logger.Info("state watcher started",
				"entity_globs", globs,
				"rate_limit_per_minute", cfg.HomeAssistant.Subscribe.RateLimitPerMinute,
				"cooldown", cooldown,
			)
			return nil
		})
	}

	// --- API server ---
	// The primary HTTP server exposing the OpenAI-compatible chat API,
	// health endpoint, router introspection, and the web UI.
	server := api.NewServer(cfg.Listen.Address, cfg.Listen.Port, loop, rtr, cfg.Pricing, logger)
	server.SetMemoryStore(mem)
	server.SetArchiveStore(archiveStore)
	server.SetEventBus(eventBus)
	server.SetConnManager(func() map[string]api.DependencyStatus {
		status := connMgr.Status()
		result := make(map[string]api.DependencyStatus, len(status))
		for name, s := range status {
			ds := api.DependencyStatus{
				Name:      s.Name,
				Ready:     s.Ready,
				LastError: s.LastError,
			}
			if !s.LastCheck.IsZero() {
				ds.LastCheck = s.LastCheck.Format(time.RFC3339)
			}
			result[name] = ds
		}
		return result
	})
	a.server = server

	// --- Checkpointer ---
	// Periodically snapshots application state (conversations, facts,
	// scheduled tasks) to enable crash recovery. Also creates a snapshot
	// on clean shutdown and before model failover. Shares thane.db.
	checkpointCfg := checkpoint.Config{
		PeriodicMessages: 50, // Snapshot every 50 messages
	}
	checkpointer, err := checkpoint.NewCheckpointer(mem.DB(), checkpointCfg, logger)
	if err != nil {
		return nil, fmt.Errorf("create checkpointer: %w", err)
	}
	a.checkpointer = checkpointer

	// Wire up the data providers that the checkpointer snapshots.
	checkpointer.SetProviders(
		func() ([]checkpoint.Conversation, error) {
			convs := mem.GetAllConversations()
			result := make([]checkpoint.Conversation, len(convs))
			for i, c := range convs {
				msgs := make([]checkpoint.SourceMessage, len(c.Messages))
				for j, m := range c.Messages {
					msgs[j] = checkpoint.SourceMessage{
						Role:      m.Role,
						Content:   m.Content,
						Timestamp: m.Timestamp,
					}
				}
				conv, err := checkpoint.ConvertConversation(c.ID, c.CreatedAt, c.UpdatedAt, msgs)
				if err != nil {
					return nil, fmt.Errorf("convert conversation %s: %w", c.ID, err)
				}
				result[i] = conv
			}
			return result, nil
		},
		func() ([]checkpoint.Fact, error) {
			allFacts, err := factStore.GetAll()
			if err != nil {
				return nil, err
			}
			result := make([]checkpoint.Fact, len(allFacts))
			for i, f := range allFacts {
				result[i] = checkpoint.Fact{
					ID:         f.ID,
					Category:   string(f.Category),
					Key:        f.Key,
					Value:      f.Value,
					Source:     f.Source,
					CreatedAt:  f.CreatedAt,
					UpdatedAt:  f.UpdatedAt,
					Confidence: f.Confidence,
				}
			}
			return result, nil
		},
		func() ([]checkpoint.Task, error) {
			tasks, err := sched.GetAllTasks()
			if err != nil {
				return nil, err
			}
			result := make([]checkpoint.Task, len(tasks))
			for i, t := range tasks {
				result[i] = checkpoint.Task{
					ID:          checkpoint.ParseUUID(t.ID),
					Name:        t.Name,
					Description: "",
					Schedule:    t.Schedule.Cron,
					Action:      string(t.Payload.Kind),
					Enabled:     t.Enabled,
					CreatedAt:   t.CreatedAt,
				}
			}
			return result, nil
		},
	)
	server.SetCheckpointer(checkpointer)
	loop.SetFailoverHandler(checkpointer)
	logger.Info("checkpointing enabled", "periodic_messages", checkpointCfg.PeriodicMessages)

	checkpointer.LogStartupStatus()

	// --- Ollama-compatible API server ---
	// --- OWU tracker ---
	// Registers a parent "owu" loop and lazily spawns per-conversation
	// children so that Open WebUI sessions appear on the dashboard.
	owuTracker, err := api.NewOWUTracker(ctx, loopRegistry, eventBus, loop, logger)
	if err != nil {
		return nil, fmt.Errorf("create owu tracker: %w", err)
	}
	server.SetOWUTracker(owuTracker)

	// Optional second HTTP server that speaks the Ollama wire protocol.
	// Home Assistant's Ollama integration connects here, allowing Thane
	// to serve as a drop-in replacement for a standalone Ollama instance.
	if cfg.OllamaAPI.Enabled {
		a.ollamaServer = api.NewOllamaServer(cfg.OllamaAPI.Address, cfg.OllamaAPI.Port, loop, logger)
		a.ollamaServer.SetOWUTracker(owuTracker)
	}

	// --- CardDAV server ---
	// Optional: exposes the contacts store as a CardDAV address book so
	// native contact apps (macOS Contacts.app, iOS, Thunderbird) can sync.
	if cfg.CardDAV.Configured() {
		carddavBackend := cdav.NewBackend(contactStore, logger)
		a.carddavServer = cdav.NewServer(
			cfg.CardDAV.Listen,
			cfg.CardDAV.Username,
			cfg.CardDAV.Password,
			carddavBackend,
			logger,
		)
	}

	// --- MQTT publisher ---
	// Optional: publishes HA MQTT discovery messages and periodic sensor
	// state updates so Thane appears as a native HA device.
	if cfg.MQTT.Configured() {
		var err error
		a.mqttInstanceID, err = mqtt.LoadOrCreateInstanceID(cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("load mqtt instance id: %w", err)
		}
		logger.Info("mqtt instance ID loaded", "instance_id", a.mqttInstanceID)

		// Timezone for midnight token counter reset.
		var tokenLoc *time.Location
		if cfg.Timezone != "" {
			tokenLoc, _ = time.LoadLocation(cfg.Timezone) // already validated
		}
		dailyTokens := mqtt.NewDailyTokens(tokenLoc)
		server.SetTokenObserver(dailyTokens)

		statsAdapter := &mqttStatsAdapter{
			model:  cfg.Models.Default,
			server: server,
		}

		// Auto-subscribe to the instance-specific callback topic when
		// actionable notifications are enabled. The topic follows the
		// existing baseTopic convention: thane/{device_name}/callbacks.
		// The subscription is appended to the user-configured list so
		// both ambient awareness topics and the callback topic are active.
		var callbackTopic string
		if a.notifCallbackDispatcher != nil {
			callbackTopic = "thane/" + cfg.MQTT.DeviceName + "/callbacks"
			found := false
			for _, sub := range cfg.MQTT.Subscriptions {
				if sub.Topic == callbackTopic {
					found = true
					break
				}
			}
			if !found {
				cfg.MQTT.Subscriptions = append(cfg.MQTT.Subscriptions, config.SubscriptionConfig{
					Topic: callbackTopic,
				})
			}
			logger.Info("notification callback topic configured", "topic", callbackTopic)
		}

		mqttPub := mqtt.New(cfg.MQTT, a.mqttInstanceID, dailyTokens, statsAdapter, logger)
		a.mqttPub = mqttPub

		// Composite MQTT message handler: routes the instance callback
		// topic to the notification dispatcher, everything else gets
		// default debug logging.
		if a.notifCallbackDispatcher != nil {
			dispatcher := a.notifCallbackDispatcher // capture for closure
			cbTopic := callbackTopic                // capture for closure
			mqttPub.SetMessageHandler(func(topic string, payload []byte) {
				if topic == cbTopic {
					dispatcher.Handle(topic, payload)
					return
				}
				logger.Debug("mqtt message received", "topic", topic, "size", len(payload))
			})
		}

		// Pass the long-lived server context as the lifecycle context
		// for the MQTT ConnectionManager. A short-lived context here
		// would kill the connection as soon as it expired (#572).
		// The initial connection await has its own internal timeout.
		err = mqttPub.Connect(ctx)
		if err != nil {
			logger.Error("mqtt publisher connection failed", "error", err)
		} else {
			// Publish immediately on connect, then let the loop handle the schedule.
			mqttPub.PublishStates(ctx)

			mqttInterval := mqttPub.PublishInterval()
			mqttLoopCfg := looppkg.Config{
				Name:         "mqtt-publisher",
				SleepMin:     mqttInterval,
				SleepMax:     mqttInterval,
				SleepDefault: mqttInterval,
				Jitter:       looppkg.Float64Ptr(0),
				Handler: func(ctx context.Context, _ any) error {
					mqttPub.PublishStates(ctx)
					return nil
				},
				Metadata: map[string]string{
					"subsystem": "mqtt",
					"category":  "publisher",
				},
			}
			mqttLoopDeps := looppkg.Deps{
				Logger:   logger,
				EventBus: eventBus,
			}
			a.deferWorker("mqtt-publisher", func(ctx context.Context) error {
				if _, err := loopRegistry.SpawnLoop(ctx, mqttLoopCfg, mqttLoopDeps); err != nil {
					return fmt.Errorf("spawn mqtt-publisher loop: %w", err)
				}
				return nil
			})
		}

		// Register with connwatch for health endpoint visibility.
		connMgr.Watch(ctx, connwatch.WatcherConfig{
			Name: "mqtt",
			Probe: func(pCtx context.Context) error {
				awaitCtx, awaitCancel := context.WithTimeout(pCtx, 2*time.Second)
				defer awaitCancel()
				return mqttPub.AwaitConnection(awaitCtx)
			},
			Backoff: connwatch.DefaultBackoffConfig(),
			Logger:  logger,
		})

		logger.Info("mqtt publishing enabled",
			"broker", cfg.MQTT.Broker,
			"device_name", cfg.MQTT.DeviceName,
			"interval", cfg.MQTT.PublishIntervalSec,
		)
	} else {
		logger.Info("mqtt publishing disabled (not configured)")
	}

	// --- MQTT AP presence sensors ---
	// When both MQTT and UniFi room presence are active, register a
	// per-person AP sensor with the MQTT publisher and observe room
	// changes so state is published only when the AP actually changes.
	if a.mqttPub != nil && personTracker != nil && cfg.Unifi.Configured() {
		var apSensors []mqtt.DynamicSensor
		mqttInstanceID := a.mqttInstanceID
		for _, entityID := range cfg.Person.Track {
			shortName := entityID
			if idx := strings.IndexByte(entityID, '.'); idx >= 0 {
				shortName = entityID[idx+1:]
			}
			suffix := shortName + "_ap"

			apSensors = append(apSensors, mqtt.DynamicSensor{
				EntitySuffix: suffix,
				Config: mqtt.SensorConfig{
					Name:                contacts.TitleCase(shortName) + " AP",
					ObjectID:            a.mqttPub.ObjectIDPrefix() + suffix,
					HasEntityName:       true,
					UniqueID:            mqttInstanceID + "_" + suffix,
					StateTopic:          a.mqttPub.StateTopic(suffix),
					JsonAttributesTopic: a.mqttPub.AttributesTopic(suffix),
					AvailabilityTopic:   a.mqttPub.AvailabilityTopic(),
					Device:              a.mqttPub.Device(),
					Icon:                "mdi:access-point",
				},
			})
		}

		a.mqttPub.RegisterSensors(apSensors)

		// Route room changes from person tracker to MQTT publishes.
		personTracker.OnRoomChange(func(entityID, room, source string) {
			shortName := entityID
			if idx := strings.IndexByte(entityID, '.'); idx >= 0 {
				shortName = entityID[idx+1:]
			}
			suffix := shortName + "_ap"

			attrs, err := json.Marshal(map[string]string{
				"ap_name":      source,
				"last_changed": time.Now().Format(time.RFC3339),
			})
			if err != nil {
				logger.Warn("mqtt AP attributes marshal failed",
					"entity_id", entityID, "error", err)
				return
			}

			pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
			defer pubCancel()

			if err := a.mqttPub.PublishDynamicState(pubCtx, suffix, room, attrs); err != nil {
				logger.Warn("mqtt AP presence publish failed",
					"entity_id", entityID, "room", room, "error", err)
			} else {
				logger.Debug("mqtt AP presence published",
					"entity_id", entityID, "room", room, "source", source)
			}
		})

		logger.Info("mqtt AP presence sensors registered", "count", len(apSensors))
	}

	// --- MQTT telemetry ---
	// When enabled, a dedicated loop collects operational metrics
	// (DB sizes, token usage, loop states, sessions, request perf,
	// attachments) and publishes them as native HA sensors.
	if a.mqttPub != nil && cfg.MQTT.Telemetry.Enabled {
		mqttInstanceID := a.mqttInstanceID
		telBuilder := &telemetry.SensorBuilder{
			InstanceID:        mqttInstanceID,
			Prefix:            a.mqttPub.ObjectIDPrefix(),
			StateTopicFn:      a.mqttPub.StateTopic,
			AttributesTopicFn: a.mqttPub.AttributesTopic,
			AvailabilityTopic: a.mqttPub.AvailabilityTopic(),
			Device:            a.mqttPub.Device(),
		}

		a.mqttPub.RegisterSensors(telBuilder.StaticSensors())

		dbPaths := map[string]string{
			"main":  filepath.Join(cfg.DataDir, "thane.db"),
			"usage": filepath.Join(cfg.DataDir, "usage.db"),
		}
		if logDir := cfg.Logging.DirPath(); logDir != "" {
			dbPaths["logs"] = filepath.Join(logDir, "logs.db")
		}
		if cfg.Attachments.StoreDir != "" {
			dbPaths["attachments"] = filepath.Join(cfg.DataDir, "attachments.db")
		}

		telSources := telemetry.Sources{
			LoopRegistry: loopRegistry,
			UsageStore:   usageStore,
			ArchiveStore: archiveStore,
			LogsDB:       a.indexDB,
			DBPaths:      dbPaths,
			Logger:       logger,
		}
		if a.attachmentStore != nil {
			telSources.AttachmentSource = a.attachmentStore
		}

		telCollector := telemetry.NewCollector(telSources)
		telPub := telemetry.NewPublisher(telCollector, a.mqttPub, telBuilder, logger)

		telInterval := time.Duration(cfg.MQTT.Telemetry.Interval) * time.Second
		telLoopCfg := looppkg.Config{
			Name:         "mqtt-telemetry",
			SleepMin:     telInterval,
			SleepMax:     telInterval,
			SleepDefault: telInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Handler: func(ctx context.Context, _ any) error {
				return telPub.Publish(ctx)
			},
			Metadata: map[string]string{
				"subsystem": "mqtt",
				"category":  "telemetry",
			},
		}
		telLoopDeps := looppkg.Deps{
			Logger:   logger,
			EventBus: eventBus,
		}
		a.deferWorker("mqtt-telemetry", func(ctx context.Context) error {
			if _, err := loopRegistry.SpawnLoop(ctx, telLoopCfg, telLoopDeps); err != nil {
				return fmt.Errorf("spawn mqtt-telemetry loop: %w", err)
			}
			return nil
		})

		logger.Info("mqtt telemetry enabled",
			"interval", cfg.MQTT.Telemetry.Interval,
			"db_paths", len(dbPaths),
		)
	}

	// --- Loop visualizer ---
	// Wire the web dashboard now that the loop registry exists.
	{
		webCfg := web.Config{
			LoopRegistry: loopRegistry,
			EventBus:     eventBus,
			SystemStatus: &systemStatusAdapter{connMgr: connMgr},
			Logger:       logger,
		}
		if a.indexDB != nil {
			webCfg.LogQuerier = &logQueryAdapter{db: a.indexDB}
			// Content querier is only useful when content retention is
			// enabled — without it every request detail lookup returns
			// empty, making the inspectable chips misleading.
			if cfg.Logging.RetainContent {
				webCfg.ContentQuerier = &contentQueryAdapter{db: a.indexDB}
			}
		}
		server.SetWebServer(web.NewWebServer(webCfg))
		logger.Info("cognition engine dashboard enabled", "url", fmt.Sprintf("http://localhost:%d/", cfg.Listen.Port))
	}

	if cfg.Metacognitive.Enabled {
		metacogCfg, err := metacognitive.ParseConfig(cfg.Metacognitive)
		if err != nil {
			return nil, fmt.Errorf("metacognitive config: %w", err)
		}
		a.metacogCfg = &metacogCfg

		// Resolve state file path: provenance store when configured,
		// workspace-relative otherwise. Uses filepath.Base to normalize
		// config values like "Thane/metacognitive.md" to flat layout.
		stateFileName := filepath.Base(metacogCfg.StateFile)
		var metacogStatePath string
		if a.provenanceStore != nil {
			metacogStatePath = a.provenanceStore.FilePath(stateFileName)
		} else {
			metacogStatePath = filepath.Join(cfg.Workspace.Path, metacogCfg.StateFile)
		}

		adapter := &loopAdapter{agentLoop: loop, router: rtr}
		loopCfg := metacognitive.BuildLoopConfig(metacogCfg, metacognitive.Opts{
			WorkspacePath:   cfg.Workspace.Path,
			StateFilePath:   metacogStatePath,
			ProvenanceStore: a.provenanceStore,
			StateFileName:   stateFileName,
		})
		loopCfg.Setup = func(l *looppkg.Loop) {
			metacognitive.RegisterTools(loop.Tools(), l, metacogCfg, metacogStatePath, a.provenanceStore)
		}

		metacogDeps := looppkg.Deps{
			Runner:   adapter,
			Logger:   logger,
			EventBus: eventBus,
		}
		a.deferWorker("metacognitive", func(ctx context.Context) error {
			if _, err := loopRegistry.SpawnLoop(ctx, loopCfg, metacogDeps); err != nil {
				return fmt.Errorf("spawn metacognitive loop: %w", err)
			}
			return nil
		})

		logger.Info("metacognitive loop enabled",
			"state_file", cfg.Metacognitive.StateFile,
			"min_sleep", cfg.Metacognitive.MinSleep,
			"max_sleep", cfg.Metacognitive.MaxSleep,
			"supervisor_probability", cfg.Metacognitive.SupervisorProbability,
		)
	}

	return a, nil
}

// newHandler creates a structured [slog.Handler] that writes to w at
// the given level and format. This is the shared handler construction
// used by newLogger and (with optional wrapping) by the serve command.
func newHandler(w io.Writer, level slog.Level, format string) slog.Handler {
	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
		ReplaceAttr: logging.ChainReplaceAttr(
			config.ReplaceLogLevelNames,
			logging.ShortenSource,
		),
	}
	if format == "json" {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// resolvePath expands a configuration path using prefix resolution (kb: etc.)
// and home-directory tilde expansion. It returns the resolved absolute path.
// The resolver may be nil if no prefixes are configured.
func resolvePath(p string, resolver *paths.Resolver) string {
	if resolver != nil {
		if r, err := resolver.Resolve(p); err == nil {
			p = r
		}
	}
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			switch {
			case p == "~":
				p = home
			case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(filepath.Separator)):
				p = filepath.Join(home, p[2:])
			}
		}
	}
	return p
}

// augmentPath prepends directories to the process PATH so that
// exec.LookPath (used during tool registration) can find binaries
// installed outside the default system PATH. On macOS, Homebrew
// directories are added automatically if they exist on disk.
// Returns the list of directories that were prepended (for deferred
// logging after the final logger is configured).
func augmentPath(extra []string) []string {
	var dirs []string

	// Config entries first (highest priority).
	for _, d := range extra {
		if expanded := os.ExpandEnv(d); expanded != "" {
			dirs = append(dirs, expanded)
		}
	}

	// Platform defaults: macOS Homebrew (Apple Silicon).
	if runtime.GOOS == "darwin" {
		for _, d := range []string{"/opt/homebrew/bin", "/opt/homebrew/sbin"} {
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				dirs = append(dirs, d)
			}
		}
	}

	if len(dirs) == 0 {
		return nil
	}

	// Deduplicate against current PATH and within dirs itself.
	current := os.Getenv("PATH")
	seen := make(map[string]bool)
	for _, d := range filepath.SplitList(current) {
		seen[d] = true
	}

	var prepend []string
	for _, d := range dirs {
		if !seen[d] {
			prepend = append(prepend, d)
			seen[d] = true
		}
	}
	if len(prepend) == 0 {
		return nil
	}

	sep := string(os.PathListSeparator)
	var newPath string
	if current == "" {
		newPath = strings.Join(prepend, sep)
	} else {
		newPath = strings.Join(prepend, sep) + sep + current
	}
	if err := os.Setenv("PATH", newPath); err != nil {
		// Can't log yet (logger not configured), but this should
		// essentially never fail.
		return nil
	}
	return prepend
}

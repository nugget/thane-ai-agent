package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/llm"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/models"
	"github.com/nugget/thane-ai-agent/internal/opstate"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
	"github.com/nugget/thane-ai-agent/internal/talents"
	"github.com/nugget/thane-ai-agent/internal/usage"
)

const modelInventoryRefreshInterval = 5 * time.Minute

// initStores creates data stores, background infrastructure, and the
// model router. Most components are passive — their goroutines are
// started later via deferred workers — but connwatch watchers start
// background health-check goroutines immediately.
func (a *App) initStores(s *newState) error {
	cfg := a.cfg
	logger := a.logger

	// --- Data directory ---
	// All persistent state (SQLite databases for memory, facts, scheduler,
	// checkpoints) lives under this directory.
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("create data directory %s: %w", cfg.DataDir, err)
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
		return fmt.Errorf("open memory database %s: %w", dbPath, err)
	}
	a.mem = mem
	a.onCloseErr("memory", mem.Close)
	logger.Info("memory database opened", "path", dbPath)

	// --- Home Assistant client ---
	// Optional but central. Without it, HA-related tools are unavailable
	// and Thane operates as a general-purpose agent.
	if cfg.HomeAssistant.Configured() {
		a.ha = homeassistant.NewClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, logger)
		a.haWS = homeassistant.NewWSClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, logger)
		a.ha.UseWSClient(a.haWS)
		a.onCloseErr("ha-websocket", a.haWS.Close)
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

	countDiscovered := func(snapshot *models.RegistrySnapshot) int {
		if snapshot == nil {
			return 0
		}
		discovered := 0
		for _, dep := range snapshot.Deployments {
			if dep.Source == models.DeploymentSourceDiscovered {
				discovered++
			}
		}
		return discovered
	}

	refreshModelRuntime := func(ctx context.Context, reason string) {
		if a.modelRuntime == nil {
			return
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		result, err := a.modelRuntime.Refresh(refreshCtx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				logger.Debug("model registry refresh canceled", "reason", reason, "error", err)
				return
			}
			logger.Warn("model registry refresh failed", "reason", reason, "error", err)
			return
		}
		if result == nil || result.Snapshot == nil {
			return
		}
		if result.Changed {
			logger.Info("model registry refreshed",
				"reason", reason,
				"generation", result.Snapshot.Generation,
				"resources", len(result.Snapshot.Resources),
				"deployments", len(result.Snapshot.Deployments),
				"discovered_deployments", countDiscovered(result.Snapshot),
			)
		} else {
			logger.Debug("model registry refresh completed with no changes",
				"reason", reason,
				"generation", result.Snapshot.Generation,
			)
		}
	}

	// The OnReady callback captures s (pointer) so it sees the
	// personTracker assigned later in initAwareness.
	var subscribeOnce sync.Once
	if a.ha != nil {
		haWatcher := connMgr.Watch(s.ctx, connwatch.WatcherConfig{
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
				if s.personTracker != nil {
					initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer initCancel()
					if err := s.personTracker.Initialize(initCtx, a.ha); err != nil {
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

	for _, res := range a.modelCatalog.Resources {
		res := res
		client, ok := a.resourceHealthClients[res.ID]
		if !ok {
			continue
		}
		c := client
		watchName := res.Provider
		if len(a.resourceHealthClients) > 1 || res.ID != "default" {
			watchName = res.Provider + ":" + res.ID
		}
		resourceWatcher := connMgr.Watch(s.ctx, connwatch.WatcherConfig{
			Name:    watchName,
			Probe:   func(pCtx context.Context) error { return c.Ping(pCtx) },
			Backoff: connwatch.DefaultBackoffConfig(),
			OnReady: func() {
				refreshModelRuntime(s.ctx, "resource_ready:"+res.ID)
			},
			Logger: logger.With("resource", res.ID, "provider", res.Provider),
		})
		c.SetWatcher(resourceWatcher)
	}

	if a.modelRuntime != nil && a.modelRuntime.InventoryClientCount() > 0 {
		a.deferWorker("model-inventory-refresh", func(ctx context.Context) error {
			go func() {
				ticker := time.NewTicker(modelInventoryRefreshInterval)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						refreshModelRuntime(ctx, "periodic")
					}
				}
			}()
			return nil
		})
		logger.Info("model inventory refresh enabled", "interval", modelInventoryRefreshInterval)
	}

	// --- Session archive ---
	// All data (sessions, iterations, messages, tool calls) lives in
	// thane.db. The archive store borrows the working DB connection.
	archiveStore, err := memory.NewArchiveStoreFromDB(mem.DB(), nil, logger)
	if err != nil {
		return fmt.Errorf("open archive store: %w", err)
	}
	a.archiveStore = archiveStore
	a.onCloseErr("archive", archiveStore.Close)

	// --- Working memory ---
	// Persists free-form experiential context per conversation.
	wmStore, err := memory.NewWorkingMemoryStore(mem.DB())
	if err != nil {
		return fmt.Errorf("create working memory store: %w", err)
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
		return fmt.Errorf("load talents: %w", err)
	}
	if len(parsedTalents) > 0 {
		talentNames := make([]string, len(parsedTalents))
		for i, t := range parsedTalents {
			talentNames[i] = t.Name
		}
		logger.Info("talents loaded", "count", len(parsedTalents), "talents", talentNames)
	}
	s.parsedTalents = parsedTalents

	// --- Persona ---
	// An optional markdown file that replaces the default system prompt,
	// giving the agent a custom identity and behavioral guidelines.
	var personaContent string
	if cfg.PersonaFile != "" {
		data, err := os.ReadFile(cfg.PersonaFile)
		if err != nil {
			return fmt.Errorf("load persona %s: %w", cfg.PersonaFile, err)
		}
		personaContent = string(data)
		logger.Info("persona loaded", "path", cfg.PersonaFile, "size", len(personaContent))
	}
	s.personaContent = personaContent

	// --- Model router ---
	// Selects the best model for each request based on complexity, cost,
	// and capability requirements. Falls back to the default model.
	routerCfg := a.modelCatalog.RouterConfig(1000)
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
		resp, err := a.llmClient.Chat(ctx, model, msgs, nil)
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
	summaryWorker := memory.NewSummarizerWorker(archiveStore, a.llmClient, rtr, logger, summarizerCfg)
	a.summaryWorker = summaryWorker

	// --- Scheduler ---
	// Persistent task scheduler for deferred and recurring work (e.g.,
	// wake events, periodic checks). Tasks survive restarts.
	schedStore, err := scheduler.NewStore(cfg.DataDir + "/scheduler.db")
	if err != nil {
		return fmt.Errorf("open scheduler database: %w", err)
	}
	a.schedStore = schedStore
	a.onCloseErr("scheduler-db", schedStore.Close)

	// --- Operational state ---
	// Generic KV store for persistent operational state (poller
	// high-water marks, feature toggles, session preferences).
	// Shares the main thane.db connection.
	opStore, err := opstate.NewStore(mem.DB())
	if err != nil {
		return fmt.Errorf("initialize operational state store: %w", err)
	}
	a.opStore = opStore

	// --- Usage tracking ---
	// Persistent token usage and cost recording for attribution and
	// analysis. Append-only SQLite store, queried via the cost_summary tool.
	// Shares the main thane.db connection.
	usageStore, err := usage.NewStore(mem.DB())
	if err != nil {
		return fmt.Errorf("initialize usage store: %w", err)
	}
	a.usageStore = usageStore

	// Task execution dependencies. The runner reads a.loop at call time
	// (not capture time) so it sees the loop constructed by initAgentLoop.
	var deps taskExecDeps
	deps.logger = logger
	deps.workspacePath = cfg.Workspace.Path

	executeTask := func(ctx context.Context, task *scheduler.Task, exec *scheduler.Execution) error {
		deps.runner = a.loop // read at execution time, set by initAgentLoop
		return runScheduledTask(ctx, task, exec, deps)
	}

	sched := scheduler.New(logger, schedStore, executeTask)
	a.sched = sched
	a.deferWorker("scheduler", func(ctx context.Context) error {
		if err := sched.Start(ctx); err != nil {
			return fmt.Errorf("start scheduler: %w", err)
		}
		a.onClose("scheduler", sched.Stop)
		return nil
	})

	return nil
}

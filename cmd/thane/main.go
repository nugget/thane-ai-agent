// Thane is an autonomous Home Assistant agent.
//
// It exposes an OpenAI-compatible API, an optional Ollama-compatible API
// (for Home Assistant integration), and a CLI for one-shot queries and
// document ingestion. Configuration is loaded from a single YAML file
// discovered automatically (see [config.DefaultSearchPaths]).
//
// Usage:
//
//	thane serve              Start the API server
//	thane init [dir]         Initialize a working directory with defaults
//	thane ask <question>     Ask a single question (for testing)
//	thane ingest <file.md>   Import a markdown document into the fact store
//	thane version            Print version and build information
//	thane -o json version    Output version information as JSON
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
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

// main is intentionally minimal. It constructs the OS-level environment
// (context, stdio, argv) and delegates immediately to [run]. This keeps
// os.Exit, os.Stdout, and os.Args out of the application logic so that
// the full startup-to-shutdown lifecycle can be driven from tests.
func main() {
	ctx := context.Background()

	if err := run(ctx, os.Stdout, os.Stderr, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

// run is the real entry point for the thane command. All OS-level
// dependencies are injected as parameters:
//
//   - ctx controls the lifetime of the process. Cancelling it triggers
//     graceful shutdown of all servers and background goroutines.
//   - stdout and stderr receive all program output. Structured logs go
//     to stdout; fatal error messages go to stderr.
//   - args is os.Args[1:] — the command-line arguments after the program
//     name. We parse these manually rather than using the flag package
//     to avoid global state that interferes with parallel tests.
//
// run returns nil on clean shutdown and a non-nil error for any failure.
// The caller (main) is responsible for printing the error and exiting.
func run(ctx context.Context, stdout io.Writer, stderr io.Writer, args []string) error {
	// Parse arguments by hand. The flag package relies on package-level
	// globals (flag.CommandLine), which makes it impossible to call run()
	// concurrently from tests. Our argument surface is small enough that
	// manual parsing is clearer than bringing in a CLI framework.
	var configPath string
	var outputFmt string // "text" (default) or "json"
	var command string
	var cmdArgs []string

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-config" && i+1 < len(args):
			configPath = args[i+1]
			i++ // skip the value
		case strings.HasPrefix(args[i], "-config="):
			configPath = strings.TrimPrefix(args[i], "-config=")
		case (args[i] == "-o" || args[i] == "--output") && i+1 < len(args):
			outputFmt = args[i+1]
			i++
		case strings.HasPrefix(args[i], "-o="):
			outputFmt = strings.TrimPrefix(args[i], "-o=")
		case strings.HasPrefix(args[i], "--output="):
			outputFmt = strings.TrimPrefix(args[i], "--output=")
		case args[i] == "-h" || args[i] == "-help" || args[i] == "--help":
			return printUsage(stdout)
		case !strings.HasPrefix(args[i], "-") && command == "":
			command = args[i]
		default:
			if command != "" {
				// Collect remaining args as subcommand arguments.
				cmdArgs = append(cmdArgs, args[i])
			} else {
				return fmt.Errorf("unknown flag: %s", args[i])
			}
		}
	}

	// Default to human-readable text output.
	if outputFmt == "" {
		outputFmt = "text"
	}
	if outputFmt != "text" && outputFmt != "json" {
		return fmt.Errorf("unknown output format: %q (expected text or json)", outputFmt)
	}

	switch command {
	case "serve":
		return runServe(ctx, stdout, stderr, configPath)
	case "init":
		dir := "."
		if len(cmdArgs) > 0 {
			dir = cmdArgs[0]
		}
		return runInit(stdout, dir)
	case "ask":
		if len(cmdArgs) == 0 {
			return fmt.Errorf("usage: thane ask <question>")
		}
		return runAsk(ctx, stdout, stderr, configPath, cmdArgs)
	case "ingest":
		if len(cmdArgs) == 0 {
			return fmt.Errorf("usage: thane ingest <file.md>")
		}
		return runIngest(ctx, stdout, stderr, configPath, cmdArgs[0])
	case "version":
		return runVersion(stdout, outputFmt)
	case "":
		return printUsage(stdout)
	default:
		return fmt.Errorf("unknown command: %s", command)
	}
}

// runVersion prints build metadata in the requested output format.
func runVersion(w io.Writer, outputFmt string) error {
	info := buildinfo.BuildInfo()
	if outputFmt == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	fmt.Fprintln(w, buildinfo.String())
	// Print fields in a stable order for human readability.
	for _, k := range []string{"version", "git_commit", "git_branch", "build_time", "go_version", "os", "arch"} {
		if v, ok := info[k]; ok {
			fmt.Fprintf(w, "  %-12s %s\n", k+":", v)
		}
	}
	return nil
}

// printUsage writes the top-level help text to w. It is called when
// thane is invoked with no arguments, or with -h / --help.
func printUsage(w io.Writer) error {
	fmt.Fprintln(w, "Thane - Autonomous Home Assistant Agent")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: thane [flags] <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  serve        Start the API server")
	fmt.Fprintln(w, "  init [dir]   Initialize working directory with defaults (default: .)")
	fmt.Fprintln(w, "  ask          Ask a single question (for testing)")
	fmt.Fprintln(w, "  ingest       Import markdown docs into fact store")
	fmt.Fprintln(w, "  version      Show version information")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -config <path>    Path to config file (default: auto-discover)")
	fmt.Fprintln(w, "  -o, --output fmt  Output format: text (default) or json")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Config search order:")
	fmt.Fprintln(w, "  ./config.yaml, ~/Thane/config.yaml, ~/.config/thane/config.yaml,")
	fmt.Fprintln(w, "  /config/config.yaml, /usr/local/etc/thane/config.yaml, /etc/thane/config.yaml")
	return nil
}

// runAsk handles the "thane ask <question>" subcommand. It boots a
// minimal agent (in-memory conversation store, no router, no scheduler)
// and processes a single question, printing the response to stdout.
// Useful for quick smoke tests and debugging without starting the server.
func runAsk(ctx context.Context, stdout io.Writer, stderr io.Writer, configPath string, args []string) error {
	logger := newLogger(stdout, slog.LevelInfo, "text")

	question := strings.Join(args, " ")

	cfg, cfgPath, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	logger.Info("config loaded", "path", cfgPath)

	// Home Assistant client (optional — ask works without it)
	var ha *homeassistant.Client
	if cfg.HomeAssistant.Configured() {
		ha = homeassistant.NewClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, logger)
	}

	ollamaClient := llm.NewOllamaClient(cfg.Models.OllamaURL, logger)
	llmClient := createLLMClient(cfg, logger, ollamaClient)

	talentLoader := talents.NewLoader(cfg.TalentsDir)
	talentContent, _ := talentLoader.Load()

	// In-memory store is fine for a single question — nothing to persist.
	mem := memory.NewStore(100)

	// Minimal loop: no router, no scheduler, no compactor. The default
	// model handles everything for CLI one-shots.
	loop := agent.NewLoop(logger, mem, nil, nil, ha, nil, llmClient, cfg.Models.Default, talentContent, "", 0)
	if ha != nil {
		loop.SetHAInject(ha)
	}

	response, err := loop.Process(ctx, "cli-test", question)
	if err != nil {
		return fmt.Errorf("ask: %w", err)
	}

	fmt.Fprintln(stdout, response)
	return nil
}

// runIngest handles the "thane ingest <file.md>" subcommand. It parses
// a markdown document into discrete facts and stores them in the fact
// database, optionally generating embeddings for semantic search.
func runIngest(ctx context.Context, stdout io.Writer, stderr io.Writer, configPath string, filePath string) error {
	logger := newLogger(stdout, slog.LevelInfo, "text")
	logger.Info("ingesting markdown document", "file", filePath)

	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	factStore, err := knowledge.NewStore(cfg.DataDir+"/knowledge.db", logger)
	if err != nil {
		return fmt.Errorf("open fact store: %w", err)
	}
	defer factStore.Close()

	// Embeddings are optional. When enabled, each ingested fact gets a
	// vector embedding for later semantic search.
	var embClient knowledge.EmbeddingClient
	if cfg.Embeddings.Enabled {
		embClient = knowledge.New(knowledge.Config{
			BaseURL: cfg.Embeddings.BaseURL,
			Model:   cfg.Embeddings.Model,
		})
		logger.Info("embeddings enabled", "model", cfg.Embeddings.Model)
	}

	source := "file:" + filePath
	ingester := knowledge.NewMarkdownIngester(factStore, embClient, source, knowledge.CategoryArchitecture)

	count, err := ingester.IngestFile(ctx, filePath)
	if err != nil {
		return fmt.Errorf("ingestion failed: %w", err)
	}

	logger.Info("ingestion complete", "facts_created", count, "source", source)
	fmt.Fprintf(stdout, "Successfully ingested %d facts from %s\n", count, filePath)
	return nil
}

// runServe handles the "thane serve" subcommand. It is the primary
// operating mode: loads config, opens databases, connects to Home
// Assistant, initializes the agent loop with all tools and providers,
// starts the API server(s), and blocks until a shutdown signal arrives.
//
// The shutdown sequence is:
//  1. SIGINT or SIGTERM cancels the context
//  2. A shutdown checkpoint is persisted (conversations, facts, tasks)
//  3. HTTP servers drain in-flight requests
//  4. Database connections and the scheduler are closed via defers
func runServe(ctx context.Context, stdout io.Writer, stderr io.Writer, configPath string) error {
	logger := newLogger(stdout, slog.LevelInfo, "text")
	logger.Info("starting Thane", "version", buildinfo.Version, "commit", buildinfo.GitCommit, "branch", buildinfo.GitBranch, "built", buildinfo.BuildTime)

	cfg, cfgPath, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	// Augment PATH before any exec.LookPath calls (tool registration,
	// media client init, etc.) so Homebrew and user-configured binaries
	// are discoverable. Logging is deferred until the final logger is
	// configured (the initial logger is Info-level so Debug would be lost).
	augmentedDirs := augmentPath(cfg.ExtraPath)

	// Reconfigure logger now that we know the desired level, format, and
	// output destination. The initial Info-level text logger above is used
	// only for the startup banner and config load message.
	var indexDB *sql.DB
	var contentWriter *logging.ContentWriter
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
				defer rotator.Close()
				logWriter = io.MultiWriter(stdout, rotator)
			}
		}

		handler := newHandler(logWriter, level, cfg.Logging.Format)

		// Open the SQLite log index alongside the raw log files.
		// If file logging is disabled (no logDir) or the DB fails to
		// open, logging continues without indexing.
		if logDir := cfg.Logging.DirPath(); logDir != "" {
			var err error
			indexDB, err = database.Open(filepath.Join(logDir, "logs.db"))
			if err != nil {
				logger.Warn("failed to open log index database, indexing disabled",
					"error", err)
				indexDB = nil
			} else if err := logging.Migrate(indexDB); err != nil {
				logger.Warn("failed to migrate log index schema, indexing disabled",
					"error", err)
				indexDB.Close()
				indexDB = nil
			} else {
				indexHandler := logging.NewIndexHandler(handler, indexDB, rotator)
				defer indexDB.Close()
				defer indexHandler.Close() // LIFO: flush pending entries before closing DB
				handler = indexHandler
			}
		}

		logger = slog.New(handler).With(
			"thane_version", buildinfo.Version,
			"thane_commit", buildinfo.GitCommit,
		)
	}

	// Content retention — create after the final logger so warnings
	// go through the configured handler.
	if cfg.Logging.RetainContent && indexDB != nil {
		cw, cwErr := logging.NewContentWriter(indexDB, cfg.Logging.ContentMaxLength(), logger)
		if cwErr != nil {
			logger.Warn("failed to create content writer, content retention disabled", "error", cwErr)
		} else {
			contentWriter = cw
			defer contentWriter.Close()
			logger.Info("content retention enabled",
				"max_content_length", cfg.Logging.ContentMaxLength(),
			)
		}
	}

	// Log PATH augmentation now that the final logger is configured.
	if len(augmentedDirs) > 0 {
		logger.Debug("augmented PATH", "prepended", augmentedDirs)
	}

	// Start background log index pruner if retention is configured and
	// the index database is available.
	if indexDB != nil {
		if retention := cfg.Logging.RetentionDaysDuration(); retention > 0 {
			go func() {
				ticker := time.NewTicker(24 * time.Hour)
				defer ticker.Stop()
				for {
					if deleted, err := logging.Prune(indexDB, retention, slog.LevelInfo); err != nil {
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
		}
	}

	// Warn about deprecated config fields.
	if depLevel, depFormat := cfg.DeprecatedFieldsUsed(); depLevel || depFormat {
		logger.Warn("log_level/log_format are deprecated; use logging.level/logging.format instead")
	}

	logger.Info("config loaded",
		"path", cfgPath,
		"port", cfg.Listen.Port,
		"model", cfg.Models.Default,
		"ollama_url", cfg.Models.OllamaURL,
	)

	// --- Data directory ---
	// All persistent state (SQLite databases for memory, facts, scheduler,
	// checkpoints, and anticipations) lives under this directory.
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("create data directory %s: %w", cfg.DataDir, err)
	}

	// --- Event bus ---
	// Process-wide publish/subscribe for operational observability.
	// Components publish structured events; the dashboard WebSocket
	// handler (and future metrics/alerting consumers) subscribe.
	// Zero cost when nobody subscribes.
	eventBus := events.New()

	// --- Loop registry ---
	// Tracks all persistent background loops (metacognitive, pollers,
	// watchers). Created early so component init blocks can register
	// loops before the web dashboard is wired up.
	loopRegistry := looppkg.NewRegistry(looppkg.WithRegistryLogger(logger))
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutCancel()
		loopRegistry.ShutdownAll(shutCtx)
	}()

	// --- Demo loops (debug) ---
	if cfg.Debug.DemoLoops {
		if err := looppkg.SpawnDemoLoops(ctx, loopRegistry, eventBus, logger); err != nil {
			return fmt.Errorf("spawn demo loops: %w", err)
		}
		logger.Warn("demo loops enabled — dashboard shows simulated activity")
	}

	// --- Memory store ---
	// SQLite-backed conversation memory. Persists across restarts so the
	// agent can resume in-progress conversations.
	dbPath := cfg.DataDir + "/thane.db"
	mem, err := memory.NewSQLiteStore(dbPath, 100)
	if err != nil {
		return fmt.Errorf("open memory database %s: %w", dbPath, err)
	}
	defer mem.Close()
	logger.Info("memory database opened", "path", dbPath)

	// --- Home Assistant client ---
	// Optional but central. Without it, HA-related tools are unavailable
	// and Thane operates as a general-purpose agent.
	var ha *homeassistant.Client
	var haWS *homeassistant.WSClient
	if cfg.HomeAssistant.Configured() {
		ha = homeassistant.NewClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, logger)
		haWS = homeassistant.NewWSClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, logger)
		logger.Debug("Home Assistant configured", "url", cfg.HomeAssistant.URL)
	} else {
		logger.Warn("Home Assistant not configured - tools will be limited")
	}

	// --- LLM client ---
	// Multi-provider client that routes each model name to its configured
	// provider (Ollama, Anthropic, etc.). Unknown models fall back to Ollama.
	ollamaClient := llm.NewOllamaClient(cfg.Models.OllamaURL, logger)
	llmClient := createLLMClient(cfg, logger, ollamaClient)

	// --- Connection resilience ---
	// Background health monitoring with exponential backoff for external
	// dependencies (Home Assistant, Ollama). Replaces the former single-shot
	// Ping() check with retries on startup and automatic reconnection at
	// runtime — no restart required. See issue #96.
	connMgr := connwatch.NewManager(logger)
	defer connMgr.Stop()

	// Forward-declare personTracker so the connwatch OnReady callback
	// can reference it. The closure captures by pointer; the tracker
	// is constructed later and also calls Initialize immediately after
	// construction to cover the case where HA connected first.
	var personTracker *contacts.PresenceTracker

	var subscribeOnce sync.Once
	if ha != nil {
		haWatcher := connMgr.Watch(ctx, connwatch.WatcherConfig{
			Name:    "homeassistant",
			Probe:   func(pCtx context.Context) error { return ha.Ping(pCtx) },
			Backoff: connwatch.DefaultBackoffConfig(),
			OnReady: func() {
				// Log HA details on first successful connection.
				infoCtx, infoCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer infoCancel()
				if haCfg, err := ha.GetConfig(infoCtx); err == nil {
					logger.Info("connected to Home Assistant",
						"url", cfg.HomeAssistant.URL,
						"version", haCfg.Version,
						"location", haCfg.LocationName,
					)
				}

				// Reconnect WebSocket when HA comes back.
				if haWS != nil {
					wsCtx, wsCancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer wsCancel()
					if err := haWS.Reconnect(wsCtx); err != nil {
						logger.Error("WebSocket reconnect failed", "error", err)
					}

					// Subscribe to state_changed events on first connection.
					// Subsequent reconnects restore subscriptions automatically
					// via WSClient.restoreSubscriptions.
					subscribeOnce.Do(func() {
						subCtx, subCancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer subCancel()
						if err := haWS.Subscribe(subCtx, "state_changed"); err != nil {
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
					if err := personTracker.Initialize(initCtx, ha); err != nil {
						logger.Warn("person tracker initialization incomplete", "error", err)
					} else {
						logger.Info("person tracker initialized")
					}
				}
			},
			Logger: logger,
		})
		ha.SetWatcher(haWatcher)
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
		return fmt.Errorf("open archive store: %w", err)
	}
	defer archiveStore.Close() // no-op — connection owned by mem

	// --- Working memory ---
	// Persists free-form experiential context per conversation.
	wmStore, err := memory.NewWorkingMemoryStore(mem.DB())
	if err != nil {
		return fmt.Errorf("create working memory store: %w", err)
	}
	logger.Info("working memory store initialized")

	archiveAdapter := memory.NewArchiveAdapter(archiveStore, mem, mem, logger)

	// --- Talents ---
	// Talents are markdown files that extend the system prompt with
	// domain-specific knowledge and instructions.
	talentLoader := talents.NewLoader(cfg.TalentsDir)
	talentContent, err := talentLoader.Load()
	if err != nil {
		return fmt.Errorf("load talents: %w", err)
	}
	if talentContent != "" {
		talentList, _ := talentLoader.List()
		logger.Info("talents loaded", "count", len(talentList), "talents", talentList)
	}

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
	defer summaryWorker.Stop()

	// --- Scheduler ---
	// Persistent task scheduler for deferred and recurring work (e.g.,
	// wake events, periodic checks). Tasks survive restarts.
	schedStore, err := scheduler.NewStore(cfg.DataDir + "/scheduler.db")
	if err != nil {
		return fmt.Errorf("open scheduler database: %w", err)
	}
	defer schedStore.Close()

	// --- Operational state ---
	// Generic KV store for persistent operational state (poller
	// high-water marks, feature toggles, session preferences).
	opStore, err := opstate.NewStore(cfg.DataDir + "/opstate.db")
	if err != nil {
		return fmt.Errorf("open operational state database: %w", err)
	}
	defer opStore.Close()

	// --- Usage tracking ---
	// Persistent token usage and cost recording for attribution and
	// analysis. Append-only SQLite store, queried via the cost_summary tool.
	usageStore, err := usage.NewStore(filepath.Join(cfg.DataDir, "usage.db"))
	if err != nil {
		return fmt.Errorf("open usage database: %w", err)
	}
	defer usageStore.Close()
	logger.Info("usage store initialized", "path", filepath.Join(cfg.DataDir, "usage.db"))

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
	if err := sched.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}
	defer sched.Stop()

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

	loop = agent.NewLoop(logger, mem, compactor, rtr, ha, sched, llmClient, cfg.Models.Default, talentContent, personaContent, defaultContextWindow)
	loop.SetTimezone(cfg.Timezone)
	if contentWriter != nil {
		loop.SetContentWriter(contentWriter)
	}
	if cfg.Models.RecoveryModel != "" {
		loop.SetRecoveryModel(cfg.Models.RecoveryModel)
		logger.Info("LLM timeout recovery enabled", "recovery_model", cfg.Models.RecoveryModel)
	}
	loop.SetArchiver(archiveAdapter)
	if ha != nil {
		loop.SetHAInject(ha)
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
		return fmt.Errorf("open fact store: %w", err)
	}
	defer factStore.Close()

	factTools := knowledge.NewTools(factStore)
	loop.Tools().SetFactTools(factTools)
	logger.Info("fact store initialized", "path", cfg.DataDir+"/knowledge.db")

	// --- Contact directory ---
	// Structured storage for people and organizations. Separate database
	// from facts to keep concerns isolated.
	contactStore, err := contacts.NewStore(cfg.DataDir+"/contacts.db", logger)
	if err != nil {
		return fmt.Errorf("open contact store: %w", err)
	}
	defer contactStore.Close()

	// Wire summarizer → contact interaction tracking now that the
	// contact store is available. Register the callback before Start()
	// to avoid a race where the startup scan reads the field concurrently.
	summaryWorker.SetInteractionCallback(func(conversationID, sessionID string, endedAt time.Time, topics []string) {
		updateContactInteraction(contactStore, logger, conversationID, sessionID, endedAt, topics)
	})
	summaryWorker.Start(ctx)

	contactTools := contacts.NewTools(contactStore)
	if cfg.Identity.ContactName != "" {
		contactTools.SetSelfContactName(cfg.Identity.ContactName)
	}
	loop.Tools().SetContactTools(contactTools)
	logger.Info("contact store initialized", "path", cfg.DataDir+"/contacts.db")

	// --- Notifications ---
	// Push notifications via HA companion app. Requires both the HA client
	// and the contact store for recipient → device resolution.
	var notifSender *notifications.Sender
	var notifRecords *notifications.RecordStore
	var notifRouter *notifications.NotificationRouter
	if ha != nil {
		notifSender = notifications.NewSender(ha, contactStore, opStore, cfg.MQTT.DeviceName, logger)
		loop.Tools().SetHANotifier(notifSender)
		logger.Info("HA notification sender initialized")

		var nrErr error
		notifRecords, nrErr = notifications.NewRecordStore(cfg.DataDir+"/notifications.db", logger)
		if nrErr != nil {
			return fmt.Errorf("open notification record store: %w", nrErr)
		}
		defer notifRecords.Close()
		loop.Tools().SetNotificationRecords(notifRecords)
		logger.Info("notification record store initialized", "path", cfg.DataDir+"/notifications.db")

		// Provider-agnostic notification router — wraps the HA push sender
		// behind a routing layer that selects delivery channel per recipient.
		notifRouter = notifications.NewNotificationRouter(contactStore, notifRecords, logger)
		notifRouter.RegisterProvider(notifications.NewHAPushProvider(notifSender))
		notifRouter.SetActivitySource(&channelActivityAdapter{
			loops: &channelLoopAdapter{registry: loopRegistry},
			store: contactStore,
		})
		loop.Tools().SetNotificationRouter(notifRouter)
		logger.Info("notification router initialized", "providers", "ha_push")
	}

	// --- Email ---
	// Native IMAP/SMTP email. Replaces the MCP email server approach
	// with direct IMAP connections for reading and SMTP for sending,
	// supporting multiple accounts with trust zone gating.
	if cfg.Email.Configured() {
		emailMgr := email.NewManager(cfg.Email, logger)
		defer emailMgr.Close()

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

			if _, err := loopRegistry.SpawnLoop(ctx, looppkg.Config{
				Name:         "email-poller",
				SleepMin:     pollInterval,
				SleepMax:     pollInterval,
				SleepDefault: pollInterval,
				Jitter:       looppkg.Float64Ptr(0),
				Handler:      emailPollHandler(poller, loop, logger),
				Metadata: map[string]string{
					"subsystem": "email",
				},
			}, looppkg.Deps{
				Logger:   logger,
				EventBus: eventBus,
			}); err != nil {
				return fmt.Errorf("spawn email poller loop: %w", err)
			}
		}

		logger.Info("email enabled", "accounts", emailMgr.AccountNames(), "poll_interval", cfg.Email.PollIntervalSec)
	} else {
		logger.Info("email disabled (not configured)")
	}

	// --- Forge integration ---
	// Native GitHub (and future Gitea/GitLab) integration. Replaces the
	// MCP github server with direct API calls via go-github.
	var forgeMgr *forge.Manager
	var forgeOpLog *forge.OperationLog
	if cfg.Forge.Configured() {
		var err error
		forgeMgr, err = forge.NewManager(cfg.Forge, logger)
		if err != nil {
			return fmt.Errorf("create forge manager: %w", err)
		}

		forgeOpLog = forge.NewOperationLog()
		forgeTools := forge.NewTools(forgeMgr, forgeOpLog, logger)
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
	anticipationDB, err := database.Open(cfg.DataDir + "/anticipations.db")
	if err != nil {
		return fmt.Errorf("open anticipation db: %w", err)
	}
	defer anticipationDB.Close()

	anticipationStore, err := scheduler.NewAnticipationStore(anticipationDB)
	if err != nil {
		return fmt.Errorf("create anticipation store: %w", err)
	}

	anticipationTools := scheduler.NewAnticipationTools(anticipationStore)
	loop.Tools().SetAnticipationTools(anticipationTools)
	logger.Info("anticipation store initialized", "path", cfg.DataDir+"/anticipations.db")

	// --- Provenance store ---
	// Git-backed file storage with SSH signature enforcement. When
	// configured, identity files (ego.md, metacognitive.md) are
	// auto-committed with cryptographic signatures on every write.
	var provenanceStore *provenance.Store
	if cfg.Provenance.Configured() {
		keyPath := paths.ExpandHome(cfg.Provenance.SigningKey)
		signer, err := provenance.NewSSHFileSigner(keyPath)
		if err != nil {
			return fmt.Errorf("load provenance signing key %s: %w", keyPath, err)
		}
		storePath := paths.ExpandHome(cfg.Provenance.Path)
		provenanceStore, err = provenance.New(storePath, signer, logger)
		if err != nil {
			return fmt.Errorf("init provenance store at %s: %w", storePath, err)
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
	var attachmentStore *attachments.Store
	if cfg.Attachments.StoreDir != "" {
		storeDir := paths.ExpandHome(cfg.Attachments.StoreDir)
		dbPath := filepath.Join(cfg.DataDir, "attachments.db")
		var err error
		attachmentStore, err = attachments.NewStore(dbPath, storeDir, logger)
		if err != nil {
			return fmt.Errorf("init attachment store: %w", err)
		}
		defer attachmentStore.Close()
		logger.Info("attachment store initialized",
			"db", dbPath,
			"store_dir", storeDir,
		)
	}

	// --- Vision analyzer ---
	// When both the attachment store and vision config are enabled,
	// images are automatically analyzed on ingest using a vision-capable
	// LLM. Results are cached in the attachment metadata index.
	var visionAnalyzer *attachments.Analyzer
	if attachmentStore != nil && cfg.Attachments.Vision.Enabled {
		visionAnalyzer = attachments.NewAnalyzer(attachmentStore, attachments.AnalyzerConfig{
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
	if attachmentStore != nil {
		attachmentTools := attachments.NewTools(attachmentStore, visionAnalyzer)
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
		if provenanceStore != nil {
			loop.SetEgoFile(provenanceStore.FilePath("ego.md"))
			loop.SetProvenanceStore(provenanceStore)
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
	if indexDB != nil {
		loop.Tools().SetLogIndexDB(indexDB)
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
	var mediaStore *media.MediaStore
	mediaStore, err = media.NewMediaStore(cfg.Media.Analysis.DatabasePath, logger)
	if err != nil {
		logger.Warn("media engagement store unavailable; analysis will persist to vault only", "error", err)
	} else {
		defer mediaStore.Close()
	}
	vaultWriter := media.NewVaultWriter(logger)
	analysisTools := media.NewAnalysisTools(
		opStore, mediaStore, vaultWriter,
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

		if _, err := loopRegistry.SpawnLoop(ctx, looppkg.Config{
			Name:         "media-feed-poller",
			SleepMin:     pollInterval,
			SleepMax:     pollInterval,
			SleepDefault: pollInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Handler:      mediaFeedHandler(feedPoller, loop, logger),
			Metadata: map[string]string{
				"subsystem": "media",
			},
		}, looppkg.Deps{
			Logger:   logger,
			EventBus: eventBus,
		}); err != nil {
			return fmt.Errorf("spawn media feed poller loop: %w", err)
		}

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
	logger.Info("web fetch enabled")

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
	var mcpClients []*mcp.Client
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

		mcpClients = append(mcpClients, client)

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
	defer func() {
		for _, c := range mcpClients {
			c.Close()
		}
	}()

	// --- Signal message bridge ---
	// Launches a native signal-cli jsonRpc subprocess and receives
	// messages event-driven, routing them through the agent loop.
	if cfg.Signal.Configured() {
		signalArgs := append([]string{"-a", cfg.Signal.Account, "jsonRpc"}, cfg.Signal.Args...)
		signalClient := sigcli.NewClient(cfg.Signal.Command, signalArgs, logger)
		if err := signalClient.Start(ctx); err != nil {
			logger.Error("signal-cli start failed", "error", err)
		} else {
			defer signalClient.Close()

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
				AttachmentStore: attachmentStore,
				VisionAnalyzer:  visionAnalyzer,
				Registry:        loopRegistry,
				EventBus:        eventBus,
			})
			if err := bridge.Register(ctx); err != nil {
				logger.Error("signal bridge registration failed", "error", err)
			}

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
			if notifRouter != nil {
				sp := notifications.NewSignalProvider(
					signalClient, contactStore, logger,
				)
				sp.SetRecorder(&signalMemoryRecorder{mem: mem})
				notifRouter.RegisterProvider(sp)
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
	if contentWriter != nil {
		delegateExec.SetContentWriter(contentWriter)
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
	if forgeMgr != nil {
		delegateExec.SetForgeContext(forgeMgr.Context())
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
	logger.Info("delegation enabled", "profiles", delegateExec.ProfileNames())

	// --- Notification callback routing ---
	// Wire up the callback dispatcher and timeout watcher for actionable
	// notifications. Requires both the notification record store and the
	// delegate executor (for spawning responses when the session is gone).
	var notifCallbackDispatcher *notifications.CallbackDispatcher
	if notifRecords != nil {
		sessionInj := &notifSessionInjector{mem: mem, archiver: archiveAdapter}
		delegateSpn := &notifDelegateSpawner{exec: delegateExec}
		notifCallbackDispatcher = notifications.NewCallbackDispatcher(
			notifRecords, sessionInj, delegateSpn, cfg.MQTT.DeviceName, logger,
		)

		// Use the router for escalation so timeout_action: "escalate"
		// respects per-recipient routing preferences. Falls back to the
		// raw HA sender when the router is unavailable.
		var escalationSender notifications.EscalationSender
		if notifRouter != nil {
			escalationSender = notifRouter
		} else if notifSender != nil {
			escalationSender = notifSender
		}
		timeoutWatcher := notifications.NewTimeoutWatcher(
			notifRecords, notifCallbackDispatcher, escalationSender,
			30*time.Second, logger,
		)
		go timeoutWatcher.Start(ctx)
		loop.Tools().SetCallbackDispatcher(notifCallbackDispatcher)
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
	// per-session via request_capability/drop_capability tools.
	if len(cfg.CapabilityTags) > 0 {
		// Load parsed talents for tag-aware filtering.
		parsedTalents, err := talentLoader.LoadAll()
		if err != nil {
			return fmt.Errorf("load talents for capability tags: %w", err)
		}

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

		// Resolve context file paths in capability tags.
		// Same pattern as inject_files: resolve kb: prefixes and ~ at
		// startup, re-read per turn. Missing files are warned but kept
		// (may appear later).
		for tag, tagCfg := range cfg.CapabilityTags {
			if len(tagCfg.Context) == 0 {
				continue
			}
			resolved := make([]string, 0, len(tagCfg.Context))
			for _, ctxPath := range tagCfg.Context {
				ctxPath = resolvePath(ctxPath, resolver)
				if _, err := os.Stat(ctxPath); err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						logger.Warn("capability tag context file not found",
							"tag", tag, "path", ctxPath)
					} else {
						logger.Warn("capability tag context file unreadable",
							"tag", tag, "path", ctxPath, "error", err)
					}
				}
				resolved = append(resolved, ctxPath)
			}
			tagCfg.Context = resolved
			cfg.CapabilityTags[tag] = tagCfg
			logger.Debug("capability tag context files resolved",
				"tag", tag, "files", len(resolved))
		}

		// Build the shared tag context assembler early so KB article
		// counts are available for the manifest. It merges three
		// sources per active tag: static config files, tagged KB
		// articles (frontmatter tags: [forge]), and live providers.
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
		if forgeMgr != nil {
			loop.RegisterTagContextProvider("forge", forge.NewContextProvider(forgeMgr, forgeOpLog))
		}

		// Build manifest entries with enriched context info.
		kbCounts := tagCtxAssembler.KBArticleTags()
		liveProviders := loop.TagContextProviders()

		tagIndex := make(map[string][]string, len(cfg.CapabilityTags))
		descriptions := make(map[string]string, len(cfg.CapabilityTags))
		alwaysActive := make(map[string]bool, len(cfg.CapabilityTags))
		contextFiles := make(map[string][]string, len(cfg.CapabilityTags))
		for tag, tagCfg := range cfg.CapabilityTags {
			tagIndex[tag] = tagCfg.Tools
			descriptions[tag] = tagCfg.Description
			alwaysActive[tag] = tagCfg.AlwaysActive
			contextFiles[tag] = tagCfg.Context
		}
		manifest := tools.BuildCapabilityManifest(tagIndex, descriptions, alwaysActive, contextFiles)

		manifestEntries := make([]talents.ManifestEntry, len(manifest))
		for i, m := range manifest {
			manifestEntries[i] = talents.ManifestEntry{
				Tag:          m.Tag,
				Description:  m.Description,
				Tools:        m.Tools,
				Context:      m.Context,
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
		for _, t := range parsedTalents {
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
			parsedTalents = append([]talents.Talent{*manifestTalent}, parsedTalents...)
		}

		loop.SetCapabilityTags(cfg.CapabilityTags, parsedTalents)
		loop.Tools().SetCapabilityTools(loop, manifest)
		loop.SetTagContextAssembler(tagCtxAssembler)

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

	// --- Context providers ---
	// Dynamic system prompt injection. Providers add context based on
	// current state (e.g., pending anticipations) before each LLM call.
	anticipationProvider := scheduler.NewAnticipationProvider(anticipationStore)
	contextProvider := agent.NewCompositeContextProvider(anticipationProvider)
	contextProvider.Add(agent.NewChannelProvider(&contactNameLookup{store: contactStore}))
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
	// SQLite so the watchlist survives restarts.
	watchlistDB, err := database.Open(cfg.DataDir + "/watchlist.db")
	if err != nil {
		return fmt.Errorf("open watchlist db: %w", err)
	}
	defer watchlistDB.Close()

	watchlistStore, err := awareness.NewWatchlistStore(watchlistDB)
	if err != nil {
		return fmt.Errorf("watchlist store: %w", err)
	}

	if ha != nil {
		watchlistProvider := awareness.NewWatchlistProvider(watchlistStore, ha, logger)
		contextProvider.Add(watchlistProvider)

		// Register tag-scoped watchlist providers for entities added
		// with tags. Each distinct tag in the store gets a provider that
		// emits those entities only when the tag is active.
		if taggedTags, err := watchlistStore.DistinctTags(); err == nil && len(taggedTags) > 0 {
			for _, tag := range taggedTags {
				loop.RegisterTagContextProvider(tag,
					awareness.NewWatchlistTagProvider(tag, watchlistStore, ha, logger))
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

		if ha != nil {
			initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
			if err := personTracker.Initialize(initCtx, ha); err != nil {
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

		if _, err := loopRegistry.SpawnLoop(ctx, looppkg.Config{
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
		}, looppkg.Deps{
			Logger:   logger,
			EventBus: eventBus,
		}); err != nil {
			return fmt.Errorf("spawn unifi poller loop: %w", err)
		}

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
	if haWS != nil {
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
		if ha != nil {
			wakeCfg.HA = ha
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

		watcher := homeassistant.NewStateWatcher(haWS.Events(), filter, limiter, handler, logger)

		// Derive a cancellable context so the loop exits cleanly
		// when the HA event channel closes.
		haLoopCtx, haLoopCancel := context.WithCancel(ctx)

		// Track last cleanup time for periodic rate-limiter maintenance.
		lastCleanup := time.Now()
		const haCleanupInterval = 5 * time.Minute
		const haBatchWindow = 1 * time.Second
		const haBatchMax = 100

		events := watcher.Events()
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
				case ev, ok := <-events:
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
					case ev, ok := <-events:
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

	// --- Checkpointer ---
	// Periodically snapshots application state (conversations, facts,
	// scheduled tasks) to enable crash recovery. Also creates a snapshot
	// on clean shutdown and before model failover.
	checkpointDB, err := database.Open(cfg.DataDir + "/checkpoints.db")
	if err != nil {
		return fmt.Errorf("open checkpoint database: %w", err)
	}
	defer checkpointDB.Close()

	checkpointCfg := checkpoint.Config{
		PeriodicMessages: 50, // Snapshot every 50 messages
	}
	checkpointer, err := checkpoint.NewCheckpointer(checkpointDB, checkpointCfg, logger)
	if err != nil {
		return fmt.Errorf("create checkpointer: %w", err)
	}

	// Wire up the data providers that the checkpointer snapshots.
	convProvider := checkpoint.ConversationProviderFunc(func() ([]checkpoint.Conversation, error) {
		convs := mem.GetAllConversations()
		result := make([]checkpoint.Conversation, len(convs))
		for i, c := range convs {
			msgs := make([]checkpoint.MemoryMessage, len(c.Messages))
			for j, m := range c.Messages {
				msgs[j] = checkpoint.MemoryMessage{
					Role:      m.Role,
					Content:   m.Content,
					Timestamp: m.Timestamp,
				}
			}
			result[i] = checkpoint.ConvertMemoryConversation(c.ID, msgs, c.CreatedAt, c.UpdatedAt)
		}
		return result, nil
	})

	taskProvider := checkpoint.TaskProviderFunc(func() ([]checkpoint.Task, error) {
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
	})

	factProvider := checkpoint.FactProviderFunc(func() ([]checkpoint.Fact, error) {
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
	})

	checkpointer.SetProviders(convProvider, factProvider, taskProvider)
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
		return fmt.Errorf("create owu tracker: %w", err)
	}
	server.SetOWUTracker(owuTracker)

	// Optional second HTTP server that speaks the Ollama wire protocol.
	// Home Assistant's Ollama integration connects here, allowing Thane
	// to serve as a drop-in replacement for a standalone Ollama instance.
	var ollamaServer *api.OllamaServer
	if cfg.OllamaAPI.Enabled {
		ollamaServer = api.NewOllamaServer(cfg.OllamaAPI.Address, cfg.OllamaAPI.Port, loop, logger)
		ollamaServer.SetOWUTracker(owuTracker)
		go func() {
			if err := ollamaServer.Start(ctx); err != nil {
				logger.Error("ollama API server failed", "error", err)
			}
		}()
	}

	// --- CardDAV server ---
	// Optional: exposes the contacts store as a CardDAV address book so
	// native contact apps (macOS Contacts.app, iOS, Thunderbird) can sync.
	var carddavServer *cdav.Server
	if cfg.CardDAV.Configured() {
		carddavBackend := cdav.NewBackend(contactStore, logger)
		carddavServer = cdav.NewServer(
			cfg.CardDAV.Listen,
			cfg.CardDAV.Username,
			cfg.CardDAV.Password,
			carddavBackend,
			logger,
		)
		if err := carddavServer.Start(ctx); err != nil {
			logger.Error("carddav server failed to start", "error", err)
		}
	}

	// --- MQTT publisher ---
	// Optional: publishes HA MQTT discovery messages and periodic sensor
	// state updates so Thane appears as a native HA device.
	var mqttPub *mqtt.Publisher
	var mqttInstanceID string
	if cfg.MQTT.Configured() {
		var err error
		mqttInstanceID, err = mqtt.LoadOrCreateInstanceID(cfg.DataDir)
		if err != nil {
			return fmt.Errorf("load mqtt instance id: %w", err)
		}
		logger.Info("mqtt instance ID loaded", "instance_id", mqttInstanceID)

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
		if notifCallbackDispatcher != nil {
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

		mqttPub = mqtt.New(cfg.MQTT, mqttInstanceID, dailyTokens, statsAdapter, logger)

		// Composite MQTT message handler: routes the instance callback
		// topic to the notification dispatcher, everything else gets
		// default debug logging.
		if notifCallbackDispatcher != nil {
			dispatcher := notifCallbackDispatcher // capture for closure
			cbTopic := callbackTopic              // capture for closure
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
			if _, err := loopRegistry.SpawnLoop(ctx, looppkg.Config{
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
			}, looppkg.Deps{
				Logger:   logger,
				EventBus: eventBus,
			}); err != nil {
				return fmt.Errorf("spawn mqtt-publisher loop: %w", err)
			}
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
	if mqttPub != nil && personTracker != nil && cfg.Unifi.Configured() {
		var apSensors []mqtt.DynamicSensor
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
					ObjectID:            mqttPub.ObjectIDPrefix() + suffix,
					HasEntityName:       true,
					UniqueID:            mqttInstanceID + "_" + suffix,
					StateTopic:          mqttPub.StateTopic(suffix),
					JsonAttributesTopic: mqttPub.AttributesTopic(suffix),
					AvailabilityTopic:   mqttPub.AvailabilityTopic(),
					Device:              mqttPub.Device(),
					Icon:                "mdi:access-point",
				},
			})
		}

		mqttPub.RegisterSensors(apSensors)

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

			if err := mqttPub.PublishDynamicState(pubCtx, suffix, room, attrs); err != nil {
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
	if mqttPub != nil && cfg.MQTT.Telemetry.Enabled {
		telBuilder := &telemetry.SensorBuilder{
			InstanceID:        mqttInstanceID,
			Prefix:            mqttPub.ObjectIDPrefix(),
			StateTopicFn:      mqttPub.StateTopic,
			AttributesTopicFn: mqttPub.AttributesTopic,
			AvailabilityTopic: mqttPub.AvailabilityTopic(),
			Device:            mqttPub.Device(),
		}

		mqttPub.RegisterSensors(telBuilder.StaticSensors())

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
			LogsDB:       indexDB,
			DBPaths:      dbPaths,
			Logger:       logger,
		}
		if attachmentStore != nil {
			telSources.AttachmentSource = attachmentStore
		}

		telCollector := telemetry.NewCollector(telSources)
		telPub := telemetry.NewPublisher(telCollector, mqttPub, telBuilder, logger)

		telInterval := time.Duration(cfg.MQTT.Telemetry.Interval) * time.Second
		if _, err := loopRegistry.SpawnLoop(ctx, looppkg.Config{
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
		}, looppkg.Deps{
			Logger:   logger,
			EventBus: eventBus,
		}); err != nil {
			return fmt.Errorf("spawn mqtt-telemetry loop: %w", err)
		}

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
		if indexDB != nil {
			webCfg.LogQuerier = &logQueryAdapter{db: indexDB}
			// Content querier is only useful when content retention is
			// enabled — without it every request detail lookup returns
			// empty, making the inspectable chips misleading.
			if cfg.Logging.RetainContent {
				webCfg.ContentQuerier = &contentQueryAdapter{db: indexDB}
			}
		}
		server.SetWebServer(web.NewWebServer(webCfg))
		logger.Info("cognition engine dashboard enabled", "url", fmt.Sprintf("http://localhost:%d/", cfg.Listen.Port))
	}

	if cfg.Metacognitive.Enabled {
		metacogCfg, err := metacognitive.ParseConfig(cfg.Metacognitive)
		if err != nil {
			return fmt.Errorf("metacognitive config: %w", err)
		}

		// Resolve state file path: provenance store when configured,
		// workspace-relative otherwise. Uses filepath.Base to normalize
		// config values like "Thane/metacognitive.md" to flat layout.
		stateFileName := filepath.Base(metacogCfg.StateFile)
		var metacogStatePath string
		if provenanceStore != nil {
			metacogStatePath = provenanceStore.FilePath(stateFileName)
		} else {
			metacogStatePath = filepath.Join(cfg.Workspace.Path, metacogCfg.StateFile)
		}

		adapter := &loopAdapter{agentLoop: loop, router: rtr}
		loopCfg := metacognitive.BuildLoopConfig(metacogCfg, metacognitive.Opts{
			WorkspacePath:   cfg.Workspace.Path,
			StateFilePath:   metacogStatePath,
			ProvenanceStore: provenanceStore,
			StateFileName:   stateFileName,
		})
		loopCfg.Setup = func(l *looppkg.Loop) {
			metacognitive.RegisterTools(loop.Tools(), l, metacogCfg, metacogStatePath, provenanceStore)
		}

		if _, err := loopRegistry.SpawnLoop(ctx, loopCfg, looppkg.Deps{
			Runner:   adapter,
			Logger:   logger,
			EventBus: eventBus,
		}); err != nil {
			return fmt.Errorf("spawn metacognitive loop: %w", err)
		}

		logger.Info("metacognitive loop enabled",
			"state_file", cfg.Metacognitive.StateFile,
			"min_sleep", cfg.Metacognitive.MinSleep,
			"max_sleep", cfg.Metacognitive.MaxSleep,
			"supervisor_probability", cfg.Metacognitive.SupervisorProbability,
		)
	}

	// --- Signal handling and graceful shutdown ---
	// NotifyContext wraps the parent context so that SIGINT/SIGTERM
	// cancellation flows through the same ctx used by all components.
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Periodic cleanup of expired opstate keys (issue #457). Expired
	// keys are already invisible on read; this reclaims storage.
	// Launched after signal.NotifyContext so the goroutine stops on
	// SIGINT/SIGTERM before opStore.Close() runs.
	go func() {
		const cleanupInterval = 1 * time.Hour
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 30*time.Second)
				n, err := opStore.DeleteExpired(cleanupCtx)
				cleanupCancel()
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					logger.Warn("opstate expired cleanup failed", "error", err)
				} else if n > 0 {
					logger.Info("opstate expired keys cleaned up", "deleted", n)
				}
			}
		}
	}()

	go func() {
		<-ctx.Done()
		logger.Info("shutdown signal received")

		// Archive conversation before shutdown
		loop.ShutdownArchive("default")

		// Publish MQTT offline status before disconnecting.
		if mqttPub != nil {
			offlineCtx, offlineCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer offlineCancel()
			if err := mqttPub.Stop(offlineCtx); err != nil {
				logger.Error("mqtt shutdown failed", "error", err)
			}
		}

		if _, err := checkpointer.CreateShutdown(); err != nil {
			logger.Error("failed to create shutdown checkpoint", "error", err)
		}

		shutdownCtx := context.Background()
		_ = server.Shutdown(shutdownCtx)
		if ollamaServer != nil {
			_ = ollamaServer.Shutdown(shutdownCtx)
		}
		if carddavServer != nil {
			_ = carddavServer.Shutdown(shutdownCtx)
		}
	}()

	// Start the primary API server. This blocks until the server is shut
	// down (via context cancellation or fatal error).
	if err := server.Start(ctx); err != nil {
		if ctx.Err() == nil {
			return fmt.Errorf("server failed: %w", err)
		}
	}

	logger.Info("Thane stopped")
	return nil
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

// newHandler creates a structured [slog.Handler] that writes to w at
// the given level and format. This is the shared handler construction
// used by [newLogger] and (with optional wrapping) by the serve command.
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

// newLogger creates a structured logger that writes to w at the given level
// and format. Format must be "text" or "json"; any other value defaults to
// text. All log output in Thane goes through slog; this helper standardizes
// the handler configuration across subcommands.
//
// Every log line carries thane_version and thane_commit for forensics
// across upgrades.
func newLogger(w io.Writer, level slog.Level, format string) *slog.Logger {
	return slog.New(newHandler(w, level, format)).With(
		"thane_version", buildinfo.Version,
		"thane_commit", buildinfo.GitCommit,
	)
}

// loadConfig locates and parses the YAML configuration file. If explicit
// is non-empty, that exact path is used (and must exist). Otherwise,
// [config.FindConfig] searches the default locations. Returns the parsed
// config, the path that was loaded, and any error.
func loadConfig(explicit string) (*config.Config, string, error) {
	cfgPath, err := config.FindConfig(explicit)
	if err != nil {
		return nil, "", err
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, cfgPath, fmt.Errorf("load config %s: %w", cfgPath, err)
	}

	return cfg, cfgPath, nil
}

// createLLMClient builds a multi-provider LLM client from the configuration.
// Each model listed in config is mapped to its provider (ollama, anthropic,
// etc.). Models not explicitly mapped fall through to the Ollama provider,
// which acts as the default backend. The OllamaClient is created externally
// so that the caller can register a connwatch watcher on it.
func createLLMClient(cfg *config.Config, logger *slog.Logger, ollamaClient *llm.OllamaClient) llm.Client {
	multi := llm.NewMultiClient(ollamaClient)
	multi.AddProvider("ollama", ollamaClient)

	if cfg.Anthropic.Configured() {
		anthropicClient := llm.NewAnthropicClient(cfg.Anthropic.APIKey, logger)
		multi.AddProvider("anthropic", anthropicClient)
		logger.Info("Anthropic provider configured")
	}

	// Model providers are already defaulted to "ollama" by applyDefaults.
	for _, m := range cfg.Models.Available {
		multi.AddModel(m.Name, m.Provider)
	}

	defaultProvider := "ollama"
	for _, m := range cfg.Models.Available {
		if m.Name == cfg.Models.Default {
			defaultProvider = m.Provider
		}
	}
	logger.Info("LLM client initialized", "default_model", cfg.Models.Default, "default_provider", defaultProvider)

	return multi
}

// factSetterFunc adapts knowledge.Store to the memory.FactSetter interface,
// adding confidence reinforcement: if a fact already exists, its confidence
// is bumped by 0.1 (capped at 1.0) rather than overwritten. This rewards
// the model for re-extracting known knowledge.
type factSetterFunc struct {
	store  *knowledge.Store
	logger *slog.Logger
}

func (f *factSetterFunc) SetFact(category, key, value, source string, confidence float64) error {
	// Check for existing fact to apply confidence reinforcement.
	existing, err := f.store.Get(knowledge.Category(category), key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// Real database error (not just "fact doesn't exist yet") — log and bail.
		f.logger.Warn("failed to check existing fact for reinforcement",
			"category", category, "key", key, "error", err)
		return err
	}
	if err == nil && existing != nil {
		if existing.Value == value {
			// Same fact re-observed — reinforce confidence.
			reinforced := min(existing.Confidence+0.1, 1.0)
			if reinforced > confidence {
				confidence = reinforced
			}
			f.logger.Debug("reinforcing existing fact confidence",
				"category", category, "key", key,
				"old_confidence", existing.Confidence,
				"new_confidence", confidence)
		} else {
			// Value changed — this is a correction, not a reinforcement.
			// Use the incoming confidence as-is.
			f.logger.Debug("updating fact value (correction)",
				"category", category, "key", key,
				"old_value", existing.Value, "new_value", value,
				"confidence", confidence)
		}
	}

	_, err = f.store.Set(knowledge.Category(category), key, value, source, confidence, nil, "")
	return err
}

// mqttStatsAdapter bridges the API server and build info to the MQTT
// publisher's [mqtt.StatsSource] interface. It holds only a narrow
// reference to the server (via its lock-protected getter), not a
// direct pointer to mutable stats fields.
type mqttStatsAdapter struct {
	model  string
	server *api.Server
}

func (a *mqttStatsAdapter) Uptime() time.Duration      { return buildinfo.Uptime() }
func (a *mqttStatsAdapter) Version() string            { return buildinfo.Version }
func (a *mqttStatsAdapter) DefaultModel() string       { return a.model }
func (a *mqttStatsAdapter) LastRequestTime() time.Time { return a.server.LastRequest() }

// signalSessionRotator implements [sigcli.SessionRotator] with
// carry-forward context and farewell message generation. When a session
// is rotated, the rotator generates a farewell message via LLM, sends
// it to the originating channel, and closes the session with a
// carry-forward summary injected into the next session.
type signalSessionRotator struct {
	loop      *agent.Loop
	llmClient llm.Client
	router    *router.Router
	sender    sigcli.ChannelSender
	archiver  agent.SessionArchiver
	logger    *slog.Logger
}

// RotateIdleSession generates a farewell message and carry-forward
// summary, sends the farewell to the sender, then gracefully closes
// the session with carry-forward injected into the next session.
func (r *signalSessionRotator) RotateIdleSession(ctx context.Context, conversationID, sender string) bool {
	sid := r.archiver.ActiveSessionID(conversationID)
	if sid == "" {
		return false
	}

	// Get conversation transcript for farewell generation.
	transcript := r.loop.ConversationTranscript(conversationID)

	// Generate farewell + carry-forward if there's a transcript.
	var farewell, carryForward string
	if transcript != "" {
		farewell, carryForward = r.generateFarewell(ctx, conversationID, transcript, "idle timeout")
	}

	// Send farewell before closing the session.
	if farewell != "" && r.sender != nil {
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := r.sender.SendMessage(sendCtx, sender, farewell); err != nil {
			r.logger.Warn("failed to send farewell message",
				"conversation_id", conversationID,
				"error", err,
			)
		}
	}

	// Close session with carry-forward (archive + end + clear + new session + inject).
	if err := r.loop.CloseSession(conversationID, "idle", carryForward); err != nil {
		r.logger.Warn("idle session close failed",
			"conversation_id", conversationID,
			"error", err,
		)
		return false
	}

	r.logger.Info("signal session rotated (idle)",
		"conversation_id", conversationID,
		"farewell_sent", farewell != "",
		"carry_forward_len", len(carryForward),
	)
	return true
}

// generateFarewell calls the LLM to produce a farewell message and
// carry-forward summary from the conversation transcript. The reason
// parameter describes why the session is closing (e.g., "idle timeout").
func (r *signalSessionRotator) generateFarewell(ctx context.Context, conversationID, transcript, reason string) (farewell, carryForward string) {
	genCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Compute session stats: duration and approximate message count.
	var parts []string
	startedAt := r.archiver.ActiveSessionStartedAt(conversationID)
	if !startedAt.IsZero() {
		parts = append(parts, fmt.Sprintf("duration: %s", time.Since(startedAt).Round(time.Minute)))
	}
	if msgCount := strings.Count(transcript, "\n"); msgCount > 0 {
		parts = append(parts, fmt.Sprintf("~%d messages", msgCount))
	}
	stats := "unknown"
	if len(parts) > 0 {
		stats = strings.Join(parts, ", ")
	}

	// Route model selection for background generation.
	model, _ := r.router.Route(genCtx, router.Request{
		Query:    "session farewell generation",
		Priority: router.PriorityBackground,
		Hints: map[string]string{
			router.HintMission:      "background",
			router.HintQualityFloor: "5",
		},
	})

	prompt := prompts.FarewellPrompt(reason, stats, transcript)
	msgs := []llm.Message{{Role: "user", Content: prompt}}

	resp, err := r.llmClient.Chat(genCtx, model, msgs, nil)
	if err != nil {
		r.logger.Warn("farewell generation failed",
			"conversation_id", conversationID,
			"model", model,
			"error", err,
		)
		return "", ""
	}

	farewell, carryForward = parseFarewellResponse(resp.Message.Content)
	return farewell, carryForward
}

// parseFarewellResponse extracts farewell and carry_forward fields from
// the LLM's JSON response. Returns empty strings if parsing fails.
func parseFarewellResponse(content string) (string, string) {
	content = strings.TrimPrefix(content, "```json\n")
	content = strings.TrimPrefix(content, "```\n")
	content = strings.TrimSuffix(content, "\n```")
	content = strings.TrimSpace(content)

	var result struct {
		Farewell     string `json:"farewell"`
		CarryForward string `json:"carry_forward"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return "", ""
	}
	return result.Farewell, result.CarryForward
}

// signalChannelSender wraps a [sigcli.Client] as a [sigcli.ChannelSender]
// for delivering farewell messages during session rotation.
type signalChannelSender struct {
	client *sigcli.Client
}

// SendMessage delivers a text message to the given recipient via Signal.
func (s *signalChannelSender) SendMessage(ctx context.Context, recipient, message string) error {
	_, err := s.client.Send(ctx, recipient, message)
	return err
}

// emailContactResolver resolves email addresses to trust zone levels
// for the email package's send gating. Implements email.ContactResolver.
type emailContactResolver struct {
	store *contacts.Store
}

// ResolveTrustZone returns the trust zone for the contact matching the
// given email address. Returns ("", false, nil) if no contact is found.
func (r *emailContactResolver) ResolveTrustZone(addr string) (string, bool, error) {
	matches, err := r.store.FindByPropertyExact("EMAIL", addr)
	if err != nil {
		return "", false, err
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	return matches[0].TrustZone, true, nil
}

// contactPhoneResolver resolves phone numbers to contact names via the
// contact directory's property store. It looks up contacts with a TEL
// property matching the given phone number.
type contactPhoneResolver struct {
	store *contacts.Store
}

// ResolvePhone returns the name and trust zone of the contact whose TEL
// property matches the given phone number. Returns ("", "", false) if no match.
func (r *contactPhoneResolver) ResolvePhone(phone string) (string, string, bool) {
	matches, err := r.store.FindByPropertyExact("TEL", phone)
	if err != nil || len(matches) == 0 {
		return "", "", false
	}
	return matches[0].FormattedName, matches[0].TrustZone, true
}

// contactNameLookup resolves contact names to rich context profiles for
// channel context injection. Implements agent.ContactLookup.
type contactNameLookup struct {
	store *contacts.Store
}

// LookupContact returns a ContactContext for the given name, or nil if
// no matching contact is found. The source parameter identifies the
// channel so fields can be gated by trust zone — known-zone contacts
// only see the channel matching the current source. Database errors
// other than "not found" are logged so operational issues don't
// silently disable contact context injection.
func (r *contactNameLookup) LookupContact(name string, source string) *agent.ContactContext {
	if r == nil || r.store == nil {
		return nil
	}

	c, err := r.store.ResolveContact(name)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("failed to resolve contact by name", "name", name, "error", err)
		}
		return nil
	}

	props, err := r.store.GetProperties(c.ID)
	if err != nil {
		slog.Error("failed to get properties for contact", "contact_id", c.ID, "name", c.FormattedName, "error", err)
		props = nil
	}

	policy := contacts.Policy(c.TrustZone)
	return buildContactContext(c, props, policy, source, time.Now())
}

// buildContactContext assembles a ContactContext from a contact record,
// its properties, and the applicable trust policy. Fields are gated by
// trust zone — lower zones receive fewer fields.
// Size limits for ContactContext fields to prevent prompt bloat.
const (
	maxSummaryLen = 300 // characters in ai_summary
	maxGroups     = 10
	maxRelated    = 10
	maxTopics     = 10
)

func buildContactContext(c *contacts.Contact, props []contacts.Property, policy contacts.ZonePolicy, source string, now time.Time) *agent.ContactContext {
	ctx := &agent.ContactContext{
		ID:        c.ID.String(),
		Name:      c.FormattedName,
		TrustZone: c.TrustZone,
		TrustPolicy: &agent.TrustPolicyView{
			FrontierModel:     policy.FrontierModelAccess,
			ProactiveOutreach: policy.ProactiveOutreach,
			ToolAccess:        policy.ToolAccess,
			SendGating:        policy.SendGating,
		},
		ContactSince: c.CreatedAt.Format("2006-01-02"),
	}

	// Known zone: minimal fields — name, trust_zone, trust_policy,
	// current-channel only, contact_since.
	if c.TrustZone == contacts.ZoneKnown {
		channels := extractChannels(props)
		if filtered := filterChannelsForSource(channels, source); len(filtered) > 0 {
			ctx.Channels = filtered
		}
		return ctx
	}

	// Trusted, household, admin: full profile.
	ctx.GivenName = c.GivenName
	ctx.FamilyName = c.FamilyName
	summary := c.AISummary
	if len(summary) > maxSummaryLen {
		summary = summary[:maxSummaryLen] + "…"
	}
	ctx.Summary = summary

	if c.Org != "" {
		ctx.Org = &c.Org
	}
	if c.Title != "" {
		ctx.Title = &c.Title
	}
	if c.Role != "" {
		ctx.Role = &c.Role
	}

	// Extract structured data from properties, capped to prevent
	// large contact records from bloating the system prompt.
	ctx.Channels = extractChannels(props)
	if groups := extractGroups(props); len(groups) > maxGroups {
		ctx.Groups = groups[:maxGroups]
	} else {
		ctx.Groups = groups
	}
	if related := extractRelated(props); len(related) > maxRelated {
		ctx.Related = related[:maxRelated]
	} else {
		ctx.Related = related
	}

	// Interaction history (trusted+).
	if !c.LastInteraction.IsZero() {
		ref := &agent.InteractionRef{
			AgoSeconds: int64(c.LastInteraction.Sub(now).Truncate(time.Second).Seconds()),
		}
		if c.LastInteractionMeta != nil {
			ref.Channel = c.LastInteractionMeta.Channel
			ref.SessionID = c.LastInteractionMeta.SessionID
			topics := c.LastInteractionMeta.Topics
			if len(topics) > maxTopics {
				topics = topics[:maxTopics]
			}
			ref.Topics = topics
		}
		ctx.LastInteraction = ref
	}

	return ctx
}

// extractChannels builds a channels map from EMAIL, TEL, and IMPP
// properties. IMPP values are split on prefix (e.g., "signal:+1..." →
// channels["signal"]).
func extractChannels(props []contacts.Property) map[string]any {
	channels := make(map[string]any)

	var emails, tels []string
	imppByScheme := make(map[string][]string)

	for _, p := range props {
		switch p.Property {
		case "EMAIL":
			emails = append(emails, p.Value)
		case "TEL":
			tels = append(tels, p.Value)
		case "IMPP":
			scheme, addr, ok := strings.Cut(p.Value, ":")
			if ok {
				imppByScheme[scheme] = append(imppByScheme[scheme], addr)
			} else {
				imppByScheme["other"] = append(imppByScheme["other"], p.Value)
			}
		}
	}

	if len(emails) > 0 {
		channels["email"] = emails
	}
	if len(tels) > 0 {
		channels["tel"] = tels
	}
	for scheme, addrs := range imppByScheme {
		if len(addrs) == 1 {
			channels[scheme] = addrs[0]
		} else {
			channels[scheme] = addrs
		}
	}

	if len(channels) == 0 {
		return nil
	}
	return channels
}

// extractGroups returns group names from CATEGORIES properties.
// Each CATEGORIES value may be comma-separated per vCard spec.
func extractGroups(props []contacts.Property) []string {
	var groups []string
	for _, p := range props {
		if p.Property == "CATEGORIES" {
			for _, cat := range strings.Split(p.Value, ",") {
				cat = strings.TrimSpace(cat)
				if cat != "" {
					groups = append(groups, cat)
				}
			}
		}
	}
	return groups
}

// extractRelated returns related contacts from RELATED properties.
func extractRelated(props []contacts.Property) []RelatedContact {
	var related []RelatedContact
	for _, p := range props {
		if p.Property == "RELATED" {
			rc := RelatedContact{Name: p.Value}
			if p.Type != "" {
				rc.Type = p.Type
			}
			related = append(related, rc)
		}
	}
	return related
}

// RelatedContact mirrors agent.RelatedContact for the main package
// builder. We re-export the agent type alias here for clarity.
type RelatedContact = agent.RelatedContact

// filterChannelsForSource returns only channel entries relevant to the
// source hint. Used for known-zone contacts where only the current
// communication channel is revealed. For Signal, also includes "tel"
// since Signal contacts are often identified by phone number even when
// they lack an explicit IMPP signal: property.
func filterChannelsForSource(channels map[string]any, source string) map[string]any {
	if channels == nil {
		return nil
	}
	result := make(map[string]any)
	if val, ok := channels[source]; ok {
		result[source] = val
	}
	// Signal contacts may only have TEL properties without an IMPP
	// signal: entry. Include tel so the agent sees their phone number.
	if source == "signal" {
		if val, ok := channels["tel"]; ok {
			result["tel"] = val
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// updateContactInteraction resolves a contact from a conversation ID
// and updates their last interaction metadata. Conversation IDs follow
// the pattern "channel-address" (e.g., "signal-15551234567").
func updateContactInteraction(store *contacts.Store, logger *slog.Logger, conversationID, sessionID string, endedAt time.Time, topics []string) {
	channel, address, ok := strings.Cut(conversationID, "-")
	if !ok || channel == "" || address == "" {
		return // Not a channel conversation (e.g., API, scheduler).
	}

	contactID, found := resolveContactByChannelAddress(store, channel, address)
	if !found {
		return
	}

	meta := &contacts.InteractionMeta{
		Channel:   channel,
		SessionID: sessionID,
		Topics:    topics,
	}
	if err := store.UpdateLastInteraction(contactID, endedAt, meta); err != nil {
		logger.Warn("failed to update contact interaction",
			"contact_id", contactID,
			"conversation_id", conversationID,
			"error", err,
		)
	}
}

// resolveContactByChannelAddress finds a contact by their channel
// address. For Signal, checks IMPP (signal:address) then TEL fallback.
// For email, checks EMAIL property.
func resolveContactByChannelAddress(store *contacts.Store, channel, address string) (uuid.UUID, bool) {
	var nilID uuid.UUID

	switch channel {
	case "signal":
		// Signal conversation IDs use sanitizePhone which strips the "+"
		// prefix (e.g., "+15551234567" → "15551234567"), but contact
		// properties store the canonical form with "+". Try both forms.
		candidates := []string{address}
		if address != "" && address[0] != '+' {
			candidates = append(candidates, "+"+address)
		}
		for _, addr := range candidates {
			matches, err := store.FindByPropertyExact("IMPP", "signal:"+addr)
			if err == nil && len(matches) == 1 {
				return matches[0].ID, true
			}
		}
		// Fallback to TEL (also try both forms).
		for _, addr := range candidates {
			matches, err := store.FindByPropertyExact("TEL", addr)
			if err == nil && len(matches) == 1 {
				return matches[0].ID, true
			}
		}
	case "email":
		matches, err := store.FindByPropertyExact("EMAIL", address)
		if err == nil && len(matches) == 1 {
			return matches[0].ID, true
		}
	}

	return nilID, false
}

// notifSessionInjector adapts the memory store and archive adapter
// into a [notifications.SessionInjector]. It avoids importing
// notifications in the memory package by using this thin adapter in
// main.go.
type notifSessionInjector struct {
	mem      *memory.SQLiteStore
	archiver *memory.ArchiveAdapter
}

// InjectSystemMessage adds a system message to the conversation's
// memory so the agent sees it on the next turn.
func (n *notifSessionInjector) InjectSystemMessage(conversationID, message string) error {
	return n.mem.AddMessage(conversationID, "system", message)
}

// IsSessionAlive reports whether the conversation has an active
// archive session.
func (n *notifSessionInjector) IsSessionAlive(conversationID string) bool {
	return n.archiver.ActiveSessionID(conversationID) != ""
}

// notifDelegateSpawner adapts the delegate executor into a
// [notifications.DelegateSpawner].
type notifDelegateSpawner struct {
	exec *delegate.Executor
}

// Spawn executes the task in a lightweight delegate loop.
func (n *notifDelegateSpawner) Spawn(ctx context.Context, task, guidance string) error {
	_, err := n.exec.Execute(ctx, task, "", guidance, nil, nil)
	return err
}

// augmentPath prepends directories to the process PATH so that
// exec.LookPath (used during tool registration) can find binaries
// installed outside the default system PATH. On macOS, Homebrew
// directories are added automatically if they exist on disk.
// augmentPath prepends directories to the process PATH so that
// exec.LookPath can find binaries installed outside launchd's minimal
// PATH. Returns the list of directories that were prepended (for
// deferred logging after the final logger is configured).
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

// channelLoopAdapter bridges [awareness.ChannelLoopSource] to the loop
// registry, filtering for channel-category loops only.
type channelLoopAdapter struct {
	registry *looppkg.Registry
}

// ChannelLoops returns loop snapshots for all loops with
// category=channel metadata (both parents and children). Consumers
// that need only child loops should filter on channel-specific
// identifiers (e.g., sender for signal, conversation_id for owu).
func (a *channelLoopAdapter) ChannelLoops() []awareness.LoopSnapshot {
	statuses := a.registry.Statuses()
	var result []awareness.LoopSnapshot
	for _, s := range statuses {
		if s.Config.Metadata["category"] != "channel" {
			continue
		}
		result = append(result, awareness.LoopSnapshot{
			ID:            s.ID,
			Name:          s.Name,
			State:         string(s.State),
			LastWakeAt:    s.LastWakeAt,
			Metadata:      s.Config.Metadata,
			RecentConvIDs: s.RecentConvIDs,
		})
	}
	return result
}

// signalMemoryRecorder records outbound Signal notifications in
// conversation memory so the agent has context when the user replies.
// Implements [notifications.MessageRecorder].
type signalMemoryRecorder struct {
	mem memory.MemoryStore
}

// RecordOutbound stores an annotated assistant message in the Signal
// conversation for the given phone number.
func (r *signalMemoryRecorder) RecordOutbound(phone, message string) error {
	// Derive conversation ID the same way the Signal bridge does:
	// "signal-" + digits-only phone.
	var sb strings.Builder
	for _, c := range phone {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			sb.WriteRune(c)
		}
	}
	convID := "signal-" + sb.String()
	return r.mem.AddMessage(convID, "assistant", message)
}

// channelActivityAdapter bridges [notifications.ChannelActivitySource]
// to the loop registry, resolving sender identities to contact names.
type channelActivityAdapter struct {
	loops *channelLoopAdapter
	store *contacts.Store
}

// ActiveChannels returns channel activity entries for active channel
// child loops, resolving Signal phone numbers to contact names via
// both TEL and IMPP properties.
func (a *channelActivityAdapter) ActiveChannels() []notifications.ChannelActivity {
	loops := a.loops.ChannelLoops()
	var result []notifications.ChannelActivity
	for _, l := range loops {
		subsystem := l.Metadata["subsystem"]
		if subsystem == "" {
			continue
		}
		// Skip parent loops (no per-conversation identity).
		if subsystem == "signal" && l.Metadata["sender"] == "" {
			continue
		}
		if subsystem == "owu" && l.Metadata["conversation_id"] == "" {
			continue
		}

		entry := notifications.ChannelActivity{
			Channel:    subsystem,
			LastActive: l.LastWakeAt,
		}

		// Resolve contact name from channel-specific identifiers.
		if a.store != nil {
			switch subsystem {
			case "signal":
				if sender := l.Metadata["sender"]; sender != "" {
					entry.Contact = resolveSignalContact(a.store, sender)
				}
			}
		}

		result = append(result, entry)
	}
	return result
}

// resolveSignalContact resolves a phone number to a contact name by
// checking TEL properties first, then IMPP with signal: prefix.
func resolveSignalContact(store *contacts.Store, phone string) string {
	// Try TEL property.
	if matches, err := store.FindByPropertyExact("TEL", phone); err == nil && len(matches) > 0 {
		return matches[0].FormattedName
	}
	// Try IMPP with signal: prefix.
	if matches, err := store.FindByPropertyExact("IMPP", "signal:"+phone); err == nil && len(matches) > 0 {
		return matches[0].FormattedName
	}
	return ""
}

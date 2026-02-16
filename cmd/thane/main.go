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
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/anticipation"
	"github.com/nugget/thane-ai-agent/internal/api"
	"github.com/nugget/thane-ai-agent/internal/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/delegate"
	"github.com/nugget/thane-ai-agent/internal/embeddings"
	"github.com/nugget/thane-ai-agent/internal/episodic"
	"github.com/nugget/thane-ai-agent/internal/facts"
	"github.com/nugget/thane-ai-agent/internal/fetch"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/ingest"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/mcp"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/mqtt"
	"github.com/nugget/thane-ai-agent/internal/person"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
	"github.com/nugget/thane-ai-agent/internal/search"
	"github.com/nugget/thane-ai-agent/internal/talents"
	"github.com/nugget/thane-ai-agent/internal/tools"

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

	factStore, err := facts.NewStore(cfg.DataDir+"/facts.db", logger)
	if err != nil {
		return fmt.Errorf("open fact store: %w", err)
	}
	defer factStore.Close()

	// Embeddings are optional. When enabled, each ingested fact gets a
	// vector embedding for later semantic search.
	var embClient facts.EmbeddingClient
	if cfg.Embeddings.Enabled {
		embClient = embeddings.New(embeddings.Config{
			BaseURL: cfg.Embeddings.BaseURL,
			Model:   cfg.Embeddings.Model,
		})
		logger.Info("embeddings enabled", "model", cfg.Embeddings.Model)
	}

	source := "file:" + filePath
	ingester := ingest.NewMarkdownIngester(factStore, embClient, source, facts.CategoryArchitecture)

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

	// Reconfigure logger now that we know the desired level and format.
	// The initial Info-level text logger is used only for the startup
	// banner and config load message; everything after this point uses
	// the configured level and format.
	{
		level := slog.LevelInfo
		if cfg.LogLevel != "" {
			// ParseLogLevel is already validated by config.Validate(), so
			// this error path should be unreachable in practice.
			level, _ = config.ParseLogLevel(cfg.LogLevel)
		}
		logger = newLogger(stdout, level, cfg.LogFormat)
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
	var personTracker *person.Tracker

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
	// Immutable archive of all conversation transcripts. Messages are
	// archived before compaction, reset, or shutdown — primary source data
	// is never discarded.
	archiveStore, err := memory.NewArchiveStore(cfg.DataDir+"/archive.db", nil, logger)
	if err != nil {
		return fmt.Errorf("open archive store: %w", err)
	}
	defer archiveStore.Close()

	// --- Working memory ---
	// Persists free-form experiential context per conversation. Shares
	// the archive database so it participates in the same backup/lifecycle.
	wmStore, err := memory.NewWorkingMemoryStore(archiveStore.DB())
	if err != nil {
		return fmt.Errorf("create working memory store: %w", err)
	}
	logger.Info("working memory store initialized")

	// --- Delegation persistence ---
	// Stores every thane_delegate execution for replay and model evaluation.
	// Shares the archive database alongside working memory.
	delegationStore, err := delegate.NewDelegationStore(archiveStore.DB())
	if err != nil {
		return fmt.Errorf("create delegation store: %w", err)
	}
	logger.Info("delegation store initialized")

	archiveAdapter := memory.NewArchiveAdapter(archiveStore, logger)
	archiveAdapter.SetToolCallSource(mem)

	// Choose which model generates session metadata — async, latency doesn't matter.
	metadataModel := cfg.Archive.MetadataModel
	if metadataModel == "" {
		metadataModel = cfg.Models.Default
	}
	logger.Info("archive metadata model configured", "model", metadataModel)

	archiveAdapter.SetMetadataGenerator(func(ctx context.Context, messages []memory.ArchivedMessage, toolCalls []memory.ArchivedToolCall) (*memory.SessionMetadata, string, []string, error) {
		// Build a condensed transcript for the LLM
		var transcript strings.Builder
		for _, m := range messages {
			if m.Role == "system" {
				continue
			}
			transcript.WriteString(fmt.Sprintf("[%s] %s: %s\n",
				m.Timestamp.Format("15:04"), m.Role, m.Content))
			if transcript.Len() > 8000 {
				transcript.WriteString("\n... (truncated)\n")
				break
			}
		}

		// Build tool usage summary
		toolUsage := make(map[string]int)
		for _, tc := range toolCalls {
			toolUsage[tc.ToolName]++
		}

		prompt := prompts.MetadataPrompt(transcript.String())

		msgs := []llm.Message{{Role: "user", Content: prompt}}
		resp, err := llmClient.Chat(ctx, metadataModel, msgs, nil)
		if err != nil {
			return nil, "", nil, err
		}

		// Parse the LLM response as JSON
		content := resp.Message.Content
		// Strip markdown code fences if present
		content = strings.TrimPrefix(content, "```json\n")
		content = strings.TrimPrefix(content, "```\n")
		content = strings.TrimSuffix(content, "\n```")
		content = strings.TrimSpace(content)

		var result struct {
			Title        string   `json:"title"`
			Tags         []string `json:"tags"`
			OneLiner     string   `json:"one_liner"`
			Paragraph    string   `json:"paragraph"`
			Detailed     string   `json:"detailed"`
			KeyDecisions []string `json:"key_decisions"`
			Participants []string `json:"participants"`
			SessionType  string   `json:"session_type"`
		}
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			// If JSON parsing fails, fall back to using the raw text as a summary
			logger.Warn("session metadata JSON parse failed, using raw summary",
				"error", err,
			)
			meta := &memory.SessionMetadata{Paragraph: resp.Message.Content}
			return meta, "", nil, nil
		}

		meta := &memory.SessionMetadata{
			OneLiner:     result.OneLiner,
			Paragraph:    result.Paragraph,
			Detailed:     result.Detailed,
			KeyDecisions: result.KeyDecisions,
			Participants: result.Participants,
			SessionType:  result.SessionType,
			ToolsUsed:    toolUsage,
		}

		return meta, result.Title, result.Tags, nil
	})

	// --- Conversation compactor ---
	// When a conversation grows too long, the compactor summarizes older
	// messages to stay within the model's context window. Uses the default
	// LLM model to generate summaries.
	compactionConfig := memory.CompactionConfig{
		MaxTokens:            8000,
		TriggerRatio:         0.7, // Compact at 70% of MaxTokens
		KeepRecent:           10,  // Preserve the last 10 messages verbatim
		MinMessagesToCompact: 15,  // Don't bother compacting tiny conversations
	}

	summarizeFunc := func(ctx context.Context, prompt string) (string, error) {
		msgs := []llm.Message{{Role: "user", Content: prompt}}
		resp, err := llmClient.Chat(ctx, cfg.Models.Default, msgs, nil)
		if err != nil {
			return "", err
		}
		return resp.Message.Content, nil
	}

	summarizer := memory.NewLLMSummarizer(summarizeFunc)
	compactor := memory.NewCompactor(mem, compactionConfig, summarizer, logger)
	compactor.SetArchiver(archiveStore)
	compactor.SetWorkingMemoryStore(wmStore)

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

	// --- Scheduler ---
	// Persistent task scheduler for deferred and recurring work (e.g.,
	// wake events, periodic checks). Tasks survive restarts.
	schedStore, err := scheduler.NewStore(cfg.DataDir + "/scheduler.db")
	if err != nil {
		return fmt.Errorf("open scheduler database: %w", err)
	}
	defer schedStore.Close()

	// Forward-declare `loop` so the executeTask closure can reference it.
	// The closure captures by reference; by the time any task fires, loop
	// is fully initialized.
	var loop *agent.Loop

	executeTask := func(ctx context.Context, task *scheduler.Task, exec *scheduler.Execution) error {
		return runScheduledTask(ctx, task, exec, loop, logger)
	}

	sched := scheduler.New(logger, schedStore, executeTask)
	if err := sched.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}
	defer sched.Stop()

	// --- Agent loop ---
	// The core conversation engine. Receives messages, manages context,
	// invokes tools, and streams responses. All other components plug
	// into it.
	defaultContextWindow := cfg.ContextWindowForModel(cfg.Models.Default, 200000)

	loop = agent.NewLoop(logger, mem, compactor, rtr, ha, sched, llmClient, cfg.Models.Default, talentContent, personaContent, defaultContextWindow)
	loop.SetTimezone(cfg.Timezone)
	loop.SetArchiver(archiveAdapter)

	// --- Static context injection ---
	if len(cfg.Context.InjectFiles) > 0 {
		var ctxBuf strings.Builder
		var injectedCount int
		for _, path := range cfg.Context.InjectFiles {
			// Expand ~ to the user's home directory.
			if strings.HasPrefix(path, "~") {
				if home, err := os.UserHomeDir(); err == nil {
					switch {
					case path == "~":
						path = home
					case strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)):
						path = filepath.Join(home, path[2:])
					}
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					logger.Warn("context inject file not found", "path", path)
				} else {
					logger.Warn("context inject file unreadable", "path", path, "error", err)
				}
				continue
			}
			logger.Debug("context file injected", "path", path, "size", len(data))
			injectedCount++
			if ctxBuf.Len() > 0 {
				ctxBuf.WriteString("\n\n---\n\n")
			}
			ctxBuf.WriteString(string(data))
		}
		if injectedCount > 0 {
			logger.Info("context injected", "files", injectedCount, "total_bytes", ctxBuf.Len())
		}
		if ctxBuf.Len() > 0 {
			loop.SetInjectedContext(ctxBuf.String())
		}
	}

	// Start initial session
	archiveAdapter.EnsureSession("default")

	// --- Fact store ---
	// Long-term memory backed by SQLite. Facts are discrete pieces of
	// knowledge that persist across conversations and restarts.
	factStore, err := facts.NewStore(cfg.DataDir+"/facts.db", logger)
	if err != nil {
		return fmt.Errorf("open fact store: %w", err)
	}
	defer factStore.Close()

	factTools := facts.NewTools(factStore)
	loop.Tools().SetFactTools(factTools)
	logger.Info("fact store initialized", "path", cfg.DataDir+"/facts.db")

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
	anticipationDB, err := sql.Open("sqlite3", cfg.DataDir+"/anticipations.db")
	if err != nil {
		return fmt.Errorf("open anticipation db: %w", err)
	}
	defer anticipationDB.Close()

	anticipationStore, err := anticipation.NewStore(anticipationDB)
	if err != nil {
		return fmt.Errorf("create anticipation store: %w", err)
	}

	anticipationTools := anticipation.NewTools(anticipationStore)
	loop.Tools().SetAnticipationTools(anticipationTools)
	logger.Info("anticipation store initialized", "path", cfg.DataDir+"/anticipations.db")

	// --- File tools ---
	// When a workspace path is configured, the agent can read and write
	// files within that directory. All paths are sandboxed.
	if cfg.Workspace.Path != "" {
		fileTools := tools.NewFileTools(cfg.Workspace.Path, cfg.Workspace.ReadOnlyDirs)
		loop.Tools().SetFileTools(fileTools)
		logger.Info("file tools enabled", "workspace", cfg.Workspace.Path)
	} else {
		logger.Info("file tools disabled (no workspace path configured)")
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
	loop.Tools().SetFetcher(fetch.New())

	// --- Archive tools ---
	// Gives the agent the ability to search and recall past conversations.
	loop.Tools().SetArchiveStore(archiveStore)
	loop.Tools().SetConversationResetter(loop)
	logger.Info("web fetch enabled")

	// --- Embeddings ---
	// Optional semantic search over the fact store. When enabled, facts
	// are indexed with vector embeddings generated by a local model.
	if cfg.Embeddings.Enabled {
		embClient := embeddings.New(embeddings.Config{
			BaseURL: cfg.Embeddings.BaseURL,
			Model:   cfg.Embeddings.Model,
		})
		factTools.SetEmbeddingClient(embClient)
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

	// --- Delegation ---
	// Register thane_delegate tool AFTER all other tools so the delegate
	// executor's parent registry snapshot includes the full tool set.
	delegateExec := delegate.NewExecutor(logger, llmClient, rtr, loop.Tools(), cfg.Models.Default)
	delegateExec.SetTimezone(cfg.Timezone)
	delegateExec.SetStore(delegationStore)
	loop.Tools().Register(&tools.Tool{
		Name:        "thane_delegate",
		Description: delegate.ToolDescription,
		Parameters:  delegate.ToolDefinition(),
		Handler:     delegate.ToolHandler(delegateExec),
	})
	logger.Info("delegation enabled", "profiles", delegateExec.ProfileNames())

	// --- Iter-0 tool gating ---
	// When delegation_required is true, the first LLM iteration only sees
	// lightweight tools (delegate + memory), steering the primary model
	// toward delegation instead of direct tool use.
	if cfg.Agent.DelegationRequired {
		loop.SetIter0Tools(cfg.Agent.Iter0Tools)
		logger.Info("iter-0 tool gating enabled", "tools", cfg.Agent.Iter0Tools)
	}

	// --- Context providers ---
	// Dynamic system prompt injection. Providers add context based on
	// current state (e.g., pending anticipations) before each LLM call.
	anticipationProvider := anticipation.NewProvider(anticipationStore)
	contextProvider := agent.NewCompositeContextProvider(anticipationProvider)

	episodicProvider := episodic.NewProvider(archiveStore, logger, episodic.Config{
		Timezone:          cfg.Timezone,
		DailyDir:          cfg.Episodic.DailyDir,
		LookbackDays:      cfg.Episodic.LookbackDays,
		HistoryTokens:     cfg.Episodic.HistoryTokens,
		SessionGapMinutes: cfg.Episodic.SessionGapMinutes,
	})
	contextProvider.Add(episodicProvider)

	wmProvider := memory.NewWorkingMemoryProvider(wmStore, tools.ConversationIDFromContext)
	contextProvider.Add(wmProvider)

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
		personTracker = person.NewTracker(cfg.Person.Track, cfg.Timezone, logger)
		contextProvider.Add(personTracker)
		logger.Info("person tracking enabled", "entities", cfg.Person.Track)

		if ha != nil {
			initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
			if err := personTracker.Initialize(initCtx, ha); err != nil {
				logger.Warn("person tracker initial sync incomplete", "error", err)
			}
			initCancel()
		}
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

		bridge := NewWakeBridge(WakeBridgeConfig{
			Store:    anticipationStore,
			Runner:   loop,
			Provider: anticipationProvider,
			Logger:   logger,
			Ctx:      ctx,
			Cooldown: cooldown,
		})

		// Compose handler: person tracker and wake bridge both see
		// every state change that passes the filter and rate limiter.
		var handler homeassistant.StateWatchHandler = bridge.HandleStateChange
		if personTracker != nil {
			bridgeHandler := handler
			handler = func(entityID, oldState, newState string) {
				personTracker.HandleStateChange(entityID, oldState, newState)
				bridgeHandler(entityID, oldState, newState)
			}
		}

		watcher := homeassistant.NewStateWatcher(haWS.Events(), filter, limiter, handler, logger)
		go watcher.Run(ctx)
		logger.Info("state watcher started",
			"entity_globs", globs,
			"rate_limit_per_minute", cfg.HomeAssistant.Subscribe.RateLimitPerMinute,
			"cooldown", cooldown,
		)
	}

	// --- API server ---
	// The primary HTTP server exposing the OpenAI-compatible chat API,
	// health endpoint, router introspection, and the web UI.
	server := api.NewServer(cfg.Listen.Address, cfg.Listen.Port, loop, rtr, logger)
	server.SetMemoryStore(mem)
	server.SetArchiveStore(archiveStore)
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
	checkpointDB, err := sql.Open("sqlite3", cfg.DataDir+"/checkpoints.db")
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
	// Optional second HTTP server that speaks the Ollama wire protocol.
	// Home Assistant's Ollama integration connects here, allowing Thane
	// to serve as a drop-in replacement for a standalone Ollama instance.
	var ollamaServer *api.OllamaServer
	if cfg.OllamaAPI.Enabled {
		ollamaServer = api.NewOllamaServer(cfg.OllamaAPI.Address, cfg.OllamaAPI.Port, loop, logger)
		go func() {
			if err := ollamaServer.Start(ctx); err != nil {
				logger.Error("ollama API server failed", "error", err)
			}
		}()
	}

	// --- MQTT publisher ---
	// Optional: publishes HA MQTT discovery messages and periodic sensor
	// state updates so Thane appears as a native HA device.
	var mqttPub *mqtt.Publisher
	if cfg.MQTT.Configured() {
		instanceID, err := mqtt.LoadOrCreateInstanceID(cfg.DataDir)
		if err != nil {
			return fmt.Errorf("load mqtt instance id: %w", err)
		}
		logger.Info("mqtt instance ID loaded", "instance_id", instanceID)

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

		mqttPub = mqtt.New(cfg.MQTT, instanceID, dailyTokens, statsAdapter, logger)
		go func() {
			if err := mqttPub.Start(ctx); err != nil {
				logger.Error("mqtt publisher failed", "error", err)
			}
		}()

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

	// --- Signal handling and graceful shutdown ---
	// NotifyContext wraps the parent context so that SIGINT/SIGTERM
	// cancellation flows through the same ctx used by all components.
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

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

// newLogger creates a structured logger that writes to w at the given level
// and format. Format must be "text" or "json"; any other value defaults to
// text. All log output in Thane goes through slog; this helper standardizes
// the handler configuration across subcommands.
func newLogger(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: config.ReplaceLogLevelNames,
	}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
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

// factSetterFunc adapts facts.Store to the memory.FactSetter interface,
// adding confidence reinforcement: if a fact already exists, its confidence
// is bumped by 0.1 (capped at 1.0) rather than overwritten. This rewards
// the model for re-extracting known facts.
type factSetterFunc struct {
	store  *facts.Store
	logger *slog.Logger
}

func (f *factSetterFunc) SetFact(category, key, value, source string, confidence float64) error {
	// Check for existing fact to apply confidence reinforcement.
	existing, err := f.store.Get(facts.Category(category), key)
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

	_, err = f.store.Set(facts.Category(category), key, value, source, confidence)
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

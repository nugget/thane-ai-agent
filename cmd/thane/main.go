// Package main is the entry point for the Thane agent.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/anticipation"
	"github.com/nugget/thane-ai-agent/internal/api"
	"github.com/nugget/thane-ai-agent/internal/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/embeddings"
	"github.com/nugget/thane-ai-agent/internal/facts"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/ingest"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
	"github.com/nugget/thane-ai-agent/internal/talents"
	"github.com/nugget/thane-ai-agent/internal/tools"

	_ "github.com/mattn/go-sqlite3"
)

// main does as little as we can get away with.
func main() {
	ctx := context.Background()

	if err := run(ctx, os.Stdout, os.Stderr, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

// run is the real entry point. All dependencies are injected so the full
// startup-to-shutdown lifecycle can be exercised from tests without
// subprocesses, real environment variables, or os.Exit.
func run(ctx context.Context, stdout io.Writer, stderr io.Writer, args []string) error {
	// Parse args manually (flag package uses globals, not test-friendly)
	var configPath string
	var command string
	var cmdArgs []string

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-config" && i+1 < len(args):
			configPath = args[i+1]
			i++ // skip value
		case strings.HasPrefix(args[i], "-config="):
			configPath = strings.TrimPrefix(args[i], "-config=")
		case args[i] == "-h" || args[i] == "-help" || args[i] == "--help":
			return printUsage(stdout)
		case !strings.HasPrefix(args[i], "-") && command == "":
			command = args[i]
		default:
			if command != "" {
				cmdArgs = append(cmdArgs, args[i])
			} else {
				return fmt.Errorf("unknown flag: %s", args[i])
			}
		}
	}

	switch command {
	case "serve":
		return runServe(ctx, stdout, stderr, configPath)
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
		fmt.Fprintln(stdout, buildinfo.String())
		for k, v := range buildinfo.Info() {
			fmt.Fprintf(stdout, "  %-12s %s\n", k+":", v)
		}
		return nil
	case "":
		return printUsage(stdout)
	default:
		return fmt.Errorf("unknown command: %s", command)
	}
}

func printUsage(w io.Writer) error {
	fmt.Fprintln(w, "Thane - Autonomous Home Assistant Agent")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: thane [flags] <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  serve    Start the API server")
	fmt.Fprintln(w, "  ask      Ask a single question (for testing)")
	fmt.Fprintln(w, "  ingest   Import markdown docs into fact store")
	fmt.Fprintln(w, "  version  Show version information")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -config <path>  Path to config file (default: auto-discover)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Config search order: ./config.yaml, ~/.config/thane/config.yaml, /etc/thane/config.yaml")
	return nil
}

func runAsk(ctx context.Context, stdout io.Writer, stderr io.Writer, configPath string, args []string) error {
	logger := newLogger(stdout, slog.LevelInfo)

	question := strings.Join(args, " ")

	// Load config
	cfg, cfgPath, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	logger.Info("config loaded", "path", cfgPath)

	// Home Assistant client
	var ha *homeassistant.Client
	if cfg.HomeAssistant.URL != "" && cfg.HomeAssistant.Token != "" {
		ha = homeassistant.NewClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token)
	}

	// Create LLM client
	llmClient := createLLMClient(cfg, logger)

	// Load talents
	talentsDir := cfg.TalentsDir
	if talentsDir == "" {
		talentsDir = "./talents"
	}
	talentLoader := talents.NewLoader(talentsDir)
	talentContent, _ := talentLoader.Load()

	// Create minimal memory store (in-memory for ask)
	mem := memory.NewStore(100)

	// Create agent loop (no router/scheduler for CLI mode - uses default model)
	loop := agent.NewLoop(logger, mem, nil, nil, ha, nil, llmClient, cfg.Models.Default, talentContent, "", 0)

	// Process the question
	threadID := "cli-test"
	response, err := loop.Process(ctx, threadID, question)
	if err != nil {
		return fmt.Errorf("ask: %w", err)
	}

	fmt.Fprintln(stdout, response)
	return nil
}

func runIngest(ctx context.Context, stdout io.Writer, stderr io.Writer, configPath string, filePath string) error {
	logger := newLogger(stdout, slog.LevelInfo)
	logger.Info("ingesting markdown document", "file", filePath)

	// Load config
	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	// Data directory
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	// Open fact store
	factStore, err := facts.NewStore(dataDir + "/facts.db")
	if err != nil {
		return fmt.Errorf("open fact store: %w", err)
	}
	defer factStore.Close()

	// Set up embedding client if configured
	var embClient facts.EmbeddingClient
	if cfg.Embeddings.Enabled {
		embURL := cfg.Embeddings.BaseURL
		if embURL == "" {
			embURL = cfg.Models.OllamaURL
			if embURL == "" {
				embURL = "http://localhost:11434"
			}
		}
		embModel := cfg.Embeddings.Model
		if embModel == "" {
			embModel = "nomic-embed-text"
		}
		embClient = embeddings.New(embeddings.Config{
			BaseURL: embURL,
			Model:   embModel,
		})
		logger.Info("embeddings enabled", "model", embModel)
	}

	// Create ingester (defaults to architecture category for docs)
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

func runServe(ctx context.Context, stdout io.Writer, stderr io.Writer, configPath string) error {
	logger := newLogger(stdout, slog.LevelInfo)
	logger.Info("starting Thane", "version", buildinfo.Version, "commit", buildinfo.GitCommit, "branch", buildinfo.GitBranch, "built", buildinfo.BuildTime)

	// Load config
	cfg, cfgPath, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	// Reconfigure logger with config-driven level
	if cfg.LogLevel != "" {
		level, err := config.ParseLogLevel(cfg.LogLevel)
		if err != nil {
			return fmt.Errorf("invalid log_level in config: %w", err)
		}
		logger = slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{
			Level:       level,
			ReplaceAttr: config.ReplaceLogLevelNames,
		}))
	}

	logger.Info("config loaded",
		"path", cfgPath,
		"port", cfg.Listen.Port,
		"model", cfg.Models.Default,
		"ollama_url", cfg.Models.OllamaURL,
	)

	// Create memory store (SQLite)
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}

	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data directory %s: %w", dataDir, err)
	}

	dbPath := dataDir + "/thane.db"
	mem, err := memory.NewSQLiteStore(dbPath, 100)
	if err != nil {
		return fmt.Errorf("open memory database %s: %w", dbPath, err)
	}
	defer mem.Close()
	logger.Info("memory database opened", "path", dbPath)

	// Home Assistant client
	var ha *homeassistant.Client
	if cfg.HomeAssistant.URL != "" && cfg.HomeAssistant.Token != "" {
		ha = homeassistant.NewClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token)
		logger.Info("Home Assistant configured", "url", cfg.HomeAssistant.URL)
		if err := ha.Ping(ctx); err != nil {
			logger.Error("Home Assistant ping failed", "error", err)
		} else {
			logger.Info("Home Assistant ping succeeded")
		}
	} else {
		logger.Warn("Home Assistant not configured - tools will be limited")
	}

	// Ollama URL from config or environment
	ollamaURL := cfg.Models.OllamaURL
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}

	// Create LLM client based on provider
	llmClient := createLLMClient(cfg, logger)

	// Create compactor with LLM summarizer
	compactionConfig := memory.CompactionConfig{
		MaxTokens:            8000, // Adjust based on model
		TriggerRatio:         0.7,  // Compact at 70% full
		KeepRecent:           10,   // Keep last 10 messages
		MinMessagesToCompact: 15,   // Need enough to be worth summarizing
	}

	// LLM summarization function
	summarizeFunc := func(ctx context.Context, prompt string) (string, error) {
		msgs := []llm.Message{{Role: "user", Content: prompt}}
		resp, err := llmClient.Chat(ctx, cfg.Models.Default, msgs, nil)
		if err != nil {
			return "", err
		}
		return resp.Message.Content, nil
	}

	summarizer := memory.NewLLMSummarizer(summarizeFunc)
	compactor := memory.NewCompactor(mem, compactionConfig, summarizer)

	// Load talents
	talentsDir := cfg.TalentsDir
	if talentsDir == "" {
		talentsDir = "./talents"
	}
	talentLoader := talents.NewLoader(talentsDir)
	talentContent, err := talentLoader.Load()
	if err != nil {
		return fmt.Errorf("load talents: %w", err)
	}
	if talentContent != "" {
		talentList, _ := talentLoader.List()
		logger.Info("talents loaded", "count", len(talentList), "talents", talentList)
	}

	// Load persona file (replaces default system prompt if set)
	var personaContent string
	if cfg.PersonaFile != "" {
		data, err := os.ReadFile(cfg.PersonaFile)
		if err != nil {
			return fmt.Errorf("load persona %s: %w", cfg.PersonaFile, err)
		}
		personaContent = string(data)
		logger.Info("persona loaded", "path", cfg.PersonaFile, "size", len(personaContent))
	}

	// Create model router
	routerCfg := router.Config{
		DefaultModel: cfg.Models.Default,
		LocalFirst:   cfg.Models.LocalFirst,
		MaxAuditLog:  1000,
	}

	// Convert config models to router models
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

	// Create scheduler
	schedStore, err := scheduler.NewStore(dataDir + "/scheduler.db")
	if err != nil {
		return fmt.Errorf("open scheduler database: %w", err)
	}
	defer schedStore.Close()

	// Scheduler execution callback - will be wired to agent loop
	executeTask := func(ctx context.Context, task *scheduler.Task, exec *scheduler.Execution) error {
		// For wake payloads, we'd inject a message into the agent
		// For now, just log it
		logger.Info("task executed",
			"task_id", task.ID,
			"task_name", task.Name,
			"payload_kind", task.Payload.Kind,
		)
		return nil
	}

	sched := scheduler.New(logger, schedStore, executeTask)
	if err := sched.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}
	defer sched.Stop()

	// Find context window for default model
	defaultContextWindow := 200000 // sensible default
	for _, m := range cfg.Models.Available {
		if m.Name == cfg.Models.Default {
			defaultContextWindow = m.ContextWindow
			break
		}
	}

	loop := agent.NewLoop(logger, mem, compactor, rtr, ha, sched, llmClient, cfg.Models.Default, talentContent, personaContent, defaultContextWindow)

	// Create fact store for long-term memory
	factStore, err := facts.NewStore(dataDir + "/facts.db")
	if err != nil {
		return fmt.Errorf("open fact store: %w", err)
	}
	defer factStore.Close()

	factTools := facts.NewTools(factStore)
	loop.Tools().SetFactTools(factTools)
	logger.Info("fact store initialized", "path", dataDir+"/facts.db")

	// Create anticipation store for bridging intent to wake
	anticipationDB, err := sql.Open("sqlite3", dataDir+"/anticipations.db")
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
	logger.Info("anticipation store initialized", "path", dataDir+"/anticipations.db")

	// Set up file tools for workspace access
	if cfg.Workspace.Path != "" {
		fileTools := tools.NewFileTools(cfg.Workspace.Path, cfg.Workspace.ReadOnlyDirs)
		loop.Tools().SetFileTools(fileTools)
		logger.Info("file tools enabled", "workspace", cfg.Workspace.Path)
	} else {
		logger.Info("file tools disabled (no workspace path configured)")
	}

	// Set up shell exec tools
	if cfg.ShellExec.Enabled {
		timeout := cfg.ShellExec.DefaultTimeoutSec
		if timeout == 0 {
			timeout = 30
		}
		shellCfg := tools.ShellExecConfig{
			Enabled:        true,
			WorkingDir:     cfg.ShellExec.WorkingDir,
			AllowedCmds:    cfg.ShellExec.AllowedPrefixes,
			DeniedCmds:     cfg.ShellExec.DeniedPatterns,
			DefaultTimeout: time.Duration(timeout) * time.Second,
		}
		// Add default denied patterns if none configured
		if len(shellCfg.DeniedCmds) == 0 {
			shellCfg.DeniedCmds = tools.DefaultShellExecConfig().DeniedCmds
		}
		shellExec := tools.NewShellExec(shellCfg)
		loop.Tools().SetShellExec(shellExec)
		logger.Info("shell exec enabled", "working_dir", cfg.ShellExec.WorkingDir)
	} else {
		logger.Info("shell exec disabled")
	}

	// Set up embedding client for semantic search
	if cfg.Embeddings.Enabled {
		embURL := cfg.Embeddings.BaseURL
		if embURL == "" {
			embURL = ollamaURL
		}
		embModel := cfg.Embeddings.Model
		if embModel == "" {
			embModel = "nomic-embed-text"
		}
		embClient := embeddings.New(embeddings.Config{
			BaseURL: embURL,
			Model:   embModel,
		})
		factTools.SetEmbeddingClient(embClient)
		logger.Info("embeddings enabled", "model", embModel, "url", embURL)
	}

	// Set up context providers for dynamic system prompt injection
	anticipationProvider := anticipation.NewProvider(anticipationStore)
	contextProvider := agent.NewCompositeContextProvider(anticipationProvider)
	// TODO: Add facts.ContextProvider when semantic search is ready
	loop.SetContextProvider(contextProvider)
	logger.Info("context providers initialized")

	server := api.NewServer(cfg.Listen.Address, cfg.Listen.Port, loop, rtr, logger)
	server.SetMemoryStore(mem)

	// Create checkpointer
	checkpointDB, err := sql.Open("sqlite3", dataDir+"/checkpoints.db")
	if err != nil {
		return fmt.Errorf("open checkpoint database: %w", err)
	}
	defer checkpointDB.Close()

	checkpointCfg := checkpoint.Config{
		PeriodicMessages: 50, // Checkpoint every 50 messages
	}
	checkpointer, err := checkpoint.NewCheckpointer(checkpointDB, checkpointCfg, logger)
	if err != nil {
		return fmt.Errorf("create checkpointer: %w", err)
	}

	// Wire up providers for checkpointing
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
				Description: "", // Not in scheduler type yet
				Schedule:    t.Schedule.Cron,
				Action:      string(t.Payload.Kind),
				Enabled:     t.Enabled,
				CreatedAt:   t.CreatedAt,
			}
		}
		return result, nil
	})

	// Fact provider for checkpointing
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
	loop.SetFailoverHandler(checkpointer) // Checkpoint before model failover
	logger.Info("checkpointing enabled", "periodic_messages", checkpointCfg.PeriodicMessages)

	// Log what state we're resuming with
	checkpointer.LogStartupStatus()

	// Start Ollama-compatible API server if configured
	var ollamaServer *api.OllamaServer
	if cfg.OllamaAPI.Enabled {
		port := cfg.OllamaAPI.Port
		if port == 0 {
			port = 11434 // Default Ollama port
		}
		ollamaServer = api.NewOllamaServer(cfg.OllamaAPI.Address, port, loop, logger)
		go func() {
			if err := ollamaServer.Start(ctx); err != nil {
				logger.Error("ollama API server failed", "error", err)
			}
		}()
	}

	// Setup graceful shutdown
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-ctx.Done()
		logger.Info("shutdown signal received")

		// Create shutdown checkpoint
		if _, err := checkpointer.CreateShutdown(); err != nil {
			logger.Error("failed to create shutdown checkpoint", "error", err)
		}

		shutdownCtx := context.Background()
		_ = server.Shutdown(shutdownCtx)
		if ollamaServer != nil {
			_ = ollamaServer.Shutdown(shutdownCtx)
		}
	}()

	// Start server (blocks until shutdown)
	if err := server.Start(ctx); err != nil {
		if ctx.Err() == nil {
			return fmt.Errorf("server failed: %w", err)
		}
	}

	logger.Info("Thane stopped")
	return nil
}

// newLogger creates a slog.Logger writing to the given writer.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
	}))
}

// loadConfig finds and loads the config file. Returns the config, the path
// that was loaded, and any error.
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

// createLLMClient creates a multi-provider LLM client based on config.
// Routes each model to its configured provider. Falls back to Ollama for unknown models.
func createLLMClient(cfg *config.Config, logger *slog.Logger) llm.Client {
	ollamaURL := cfg.Models.OllamaURL
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}

	ollamaClient := llm.NewOllamaClient(ollamaURL, logger)
	multi := llm.NewMultiClient(ollamaClient)
	multi.AddProvider("ollama", ollamaClient)

	// Register Anthropic provider if configured
	if cfg.Anthropic.APIKey != "" {
		anthropicClient := llm.NewAnthropicClient(cfg.Anthropic.APIKey, logger)
		multi.AddProvider("anthropic", anthropicClient)
		logger.Info("Anthropic provider configured")
	}

	// Map each model to its provider
	for _, m := range cfg.Models.Available {
		provider := m.Provider
		if provider == "" {
			provider = "ollama"
		}
		multi.AddModel(m.Name, provider)
	}

	// Log default model's provider
	defaultProvider := "ollama"
	for _, m := range cfg.Models.Available {
		if m.Name == cfg.Models.Default && m.Provider != "" {
			defaultProvider = m.Provider
		}
	}
	logger.Info("LLM client initialized", "default_model", cfg.Models.Default, "default_provider", defaultProvider)

	return multi
}

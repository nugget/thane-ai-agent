// Package main is the entry point for the Thane agent.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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

func main() {
	// Parse flags
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// Setup logging
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Handle subcommands
	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "serve":
			runServe(logger, *configPath)
		case "ask":
			if flag.NArg() < 2 {
				fmt.Fprintln(os.Stderr, "usage: thane ask <question>")
				os.Exit(1)
			}
			runAsk(logger, *configPath, flag.Args()[1:])
		case "ingest":
			if flag.NArg() < 2 {
				fmt.Fprintln(os.Stderr, "usage: thane ingest <file.md>")
				os.Exit(1)
			}
			runIngest(logger, *configPath, flag.Arg(1))
		case "version":
			fmt.Println(buildinfo.String())
			for k, v := range buildinfo.Info() {
				fmt.Printf("  %-12s %s\n", k+":", v)
			}
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s\n", flag.Arg(0))
			os.Exit(1)
		}
		return
	}

	// Default: show help
	fmt.Println("Thane - Autonomous Home Assistant Agent")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  serve    Start the API server")
	fmt.Println("  ask      Ask a single question (for testing)")
	fmt.Println("  ingest   Import markdown docs into fact store")
	fmt.Println("  version  Show version")
	fmt.Println()
	fmt.Println("Flags:")
	flag.PrintDefaults()
}

func runAsk(logger *slog.Logger, configPath string, args []string) {
	question := args[0]
	for _, a := range args[1:] {
		question += " " + a
	}

	// Load config
	cfgPath, err := config.FindConfig(configPath)
	if err != nil {
		logger.Error("config", "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("failed to load config", "path", cfgPath, "error", err)
		os.Exit(1)
	}

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
	ctx := context.Background()
	threadID := "cli-test"

	response, err := loop.Process(ctx, threadID, question)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(response)
}

func runIngest(logger *slog.Logger, configPath string, filePath string) {
	logger.Info("ingesting markdown document", "file", filePath)

	// Load config
	cfgPath, err := config.FindConfig(configPath)
	if err != nil {
		logger.Error("config", "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("failed to load config", "path", cfgPath, "error", err)
		os.Exit(1)
	}

	// Data directory
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Error("failed to create data directory", "error", err)
		os.Exit(1)
	}

	// Open fact store
	factStore, err := facts.NewStore(dataDir + "/facts.db")
	if err != nil {
		logger.Error("failed to open fact store", "error", err)
		os.Exit(1)
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

	// Run ingestion
	ctx := context.Background()
	count, err := ingester.IngestFile(ctx, filePath)
	if err != nil {
		logger.Error("ingestion failed", "error", err)
		os.Exit(1)
	}

	logger.Info("ingestion complete", "facts_created", count, "source", source)
	fmt.Printf("Successfully ingested %d facts from %s\n", count, filePath)
}

func runServe(logger *slog.Logger, configPath string) {
	logger.Info("starting Thane", "version", buildinfo.Version, "commit", buildinfo.GitCommit, "branch", buildinfo.GitBranch, "built", buildinfo.BuildTime)

	// Load config
	cfgPath, err := config.FindConfig(configPath)
	if err != nil {
		logger.Error("config", "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("failed to load config", "path", cfgPath, "error", err)
		os.Exit(1)
	}

	// Reconfigure logger with config-driven level
	if cfg.LogLevel != "" {
		level, err := config.ParseLogLevel(cfg.LogLevel)
		if err != nil {
			logger.Error("invalid log_level in config", "error", err)
			os.Exit(1)
		}
		logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
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
		logger.Error("failed to create data directory", "path", dataDir, "error", err)
		os.Exit(1)
	}

	dbPath := dataDir + "/thane.db"
	mem, err := memory.NewSQLiteStore(dbPath, 100)
	if err != nil {
		logger.Error("failed to open memory database", "path", dbPath, "error", err)
		os.Exit(1)
	}
	defer mem.Close()
	logger.Info("memory database opened", "path", dbPath)

	// Home Assistant client
	var ha *homeassistant.Client
	if cfg.HomeAssistant.URL != "" && cfg.HomeAssistant.Token != "" {
		ha = homeassistant.NewClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token)
		logger.Info("Home Assistant configured", "url", cfg.HomeAssistant.URL)
		if err := ha.Ping(context.Background()); err != nil {
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
		logger.Error("failed to load talents", "error", err)
		os.Exit(1)
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
			logger.Error("failed to load persona file", "path", cfg.PersonaFile, "error", err)
			os.Exit(1)
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
		logger.Error("failed to open scheduler database", "error", err)
		os.Exit(1)
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
	if err := sched.Start(context.Background()); err != nil {
		logger.Error("failed to start scheduler", "error", err)
		os.Exit(1)
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
		logger.Error("failed to open fact store", "error", err)
		os.Exit(1)
	}
	defer factStore.Close()

	factTools := facts.NewTools(factStore)
	loop.Tools().SetFactTools(factTools)
	logger.Info("fact store initialized", "path", dataDir+"/facts.db")

	// Create anticipation store for bridging intent to wake
	anticipationDB, err := sql.Open("sqlite3", dataDir+"/anticipations.db")
	if err != nil {
		logger.Error("failed to open anticipation db", "error", err)
		os.Exit(1)
	}
	defer anticipationDB.Close()

	anticipationStore, err := anticipation.NewStore(anticipationDB)
	if err != nil {
		logger.Error("failed to create anticipation store", "error", err)
		os.Exit(1)
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
		logger.Error("failed to open checkpoint database", "error", err)
		os.Exit(1)
	}
	defer checkpointDB.Close()

	checkpointCfg := checkpoint.Config{
		PeriodicMessages: 50, // Checkpoint every 50 messages
	}
	checkpointer, err := checkpoint.NewCheckpointer(checkpointDB, checkpointCfg, logger)
	if err != nil {
		logger.Error("failed to create checkpointer", "error", err)
		os.Exit(1)
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
			if err := ollamaServer.Start(context.Background()); err != nil {
				logger.Error("ollama API server failed", "error", err)
			}
		}()
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		logger.Info("shutdown signal received")

		// Create shutdown checkpoint
		if _, err := checkpointer.CreateShutdown(); err != nil {
			logger.Error("failed to create shutdown checkpoint", "error", err)
		}

		cancel()
		_ = server.Shutdown(context.Background())
		if ollamaServer != nil {
			_ = ollamaServer.Shutdown(context.Background())
		}
	}()

	// Start server
	if err := server.Start(ctx); err != nil {
		if ctx.Err() == nil {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}

	logger.Info("Thane stopped")
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

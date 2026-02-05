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

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/api"
	"github.com/nugget/thane-ai-agent/internal/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/facts"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
	"github.com/nugget/thane-ai-agent/internal/talents"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
	// Parse flags
	configPath := flag.String("config", "", "path to config file")
	port := flag.Int("port", 0, "override listen port")
	flag.Parse()

	// Setup logging
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Handle subcommands
	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "serve":
			runServe(logger, *configPath, *port)
		case "ask":
			if flag.NArg() < 2 {
				fmt.Fprintln(os.Stderr, "usage: thane ask <question>")
				os.Exit(1)
			}
			runAsk(logger, *configPath, flag.Args()[1:])
		case "version":
			fmt.Println("thane v0.1.0")
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
	var cfg *config.Config
	var err error
	if configPath != "" {
		cfg, err = config.Load(configPath)
		if err != nil {
			logger.Error("failed to load config", "path", configPath, "error", err)
			os.Exit(1)
		}
	} else {
		cfg = config.Default()
	}
	
	// Home Assistant client
	var ha *homeassistant.Client
	if cfg.HomeAssistant.URL != "" && cfg.HomeAssistant.Token != "" {
		ha = homeassistant.NewClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token)
	}
	
	// Ollama URL
	ollamaURL := cfg.Models.OllamaURL
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	
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
	loop := agent.NewLoop(logger, mem, nil, nil, ha, nil, ollamaURL, cfg.Models.Default, talentContent)
	
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

func runServe(logger *slog.Logger, configPath string, portOverride int) {
	logger.Info("starting Thane", "version", "0.1.0")

	// Load config
	var cfg *config.Config
	var err error
	if configPath != "" {
		cfg, err = config.Load(configPath)
		if err != nil {
			logger.Error("failed to load config", "path", configPath, "error", err)
			os.Exit(1)
		}
	} else {
		cfg = config.Default()
	}

	// Apply overrides
	if portOverride > 0 {
		cfg.Listen.Port = portOverride
	}

	logger.Info("config loaded",
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
	} else {
		logger.Warn("Home Assistant not configured - tools will be limited")
	}
	
	// Ollama URL from config or environment
	ollamaURL := cfg.Models.OllamaURL
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	
	// Create LLM client for summarization
	llmClient := llm.NewOllamaClient(ollamaURL)
	
	// Create compactor with LLM summarizer
	compactionConfig := memory.CompactionConfig{
		MaxTokens:            8000,  // Adjust based on model
		TriggerRatio:         0.7,   // Compact at 70% full
		KeepRecent:           10,    // Keep last 10 messages
		MinMessagesToCompact: 15,    // Need enough to be worth summarizing
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
	
	loop := agent.NewLoop(logger, mem, compactor, rtr, ha, sched, ollamaURL, cfg.Models.Default, talentContent)
	
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
	
	server := api.NewServer(cfg.Listen.Port, loop, rtr, logger)
	
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
		server.Shutdown(context.Background())
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

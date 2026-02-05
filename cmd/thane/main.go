// Package main is the entry point for the Thane agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/api"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
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
	fmt.Println("  version  Show version")
	fmt.Println()
	fmt.Println("Flags:")
	flag.PrintDefaults()
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
	
	loop := agent.NewLoop(logger, mem, compactor, ha, ollamaURL, cfg.Models.Default)
	server := api.NewServer(cfg.Listen.Port, loop, logger)

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		logger.Info("shutdown signal received")
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

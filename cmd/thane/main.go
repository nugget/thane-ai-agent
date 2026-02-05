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

	// Create components
	loop := agent.NewLoop(logger)
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

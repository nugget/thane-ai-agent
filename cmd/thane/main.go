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
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/app"
	"github.com/nugget/thane-ai-agent/internal/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/knowledge"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/models"
	"github.com/nugget/thane-ai-agent/internal/talents"

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

	llmSetup, err := createLLMSetup(cfg, logger)
	if err != nil {
		return err
	}

	talentLoader := talents.NewLoader(cfg.TalentsDir)
	cliTalents, _ := talentLoader.Talents()

	// In-memory store is fine for a single question — nothing to persist.
	mem := memory.NewStore(100)

	// Minimal loop: no router, no scheduler, no compactor. The default
	// model handles everything for CLI one-shots.
	loop := agent.NewLoop(logger, mem, nil, nil, ha, nil, llmSetup.Client, llmSetup.Catalog.DefaultModel, cliTalents, "", 0)
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

// runServe handles the "thane serve" subcommand. It loads config,
// constructs the App via [app.New], and then runs [app.Serve] which
// blocks until a shutdown signal arrives.
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

	llmSetup, err := createLLMSetup(cfg, logger)
	if err != nil {
		return err
	}

	// Set up signal handling with explicit logging so operators see
	// confirmation that the shutdown was received. A second signal
	// forces an immediate exit for cases where graceful shutdown hangs.
	//
	// Buffer of 2 ensures back-to-back signals aren't coalesced before
	// the goroutine reads the first. The goroutine selects on the parent
	// context (before we derive our own) so it exits cleanly when
	// shutdown is triggered by context cancellation rather than an OS
	// signal.
	parentCtx := ctx
	ctx, cancel := context.WithCancel(parentCtx)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			logger.Info("received shutdown signal, stopping gracefully", "signal", sig)
			cancel()
		case <-parentCtx.Done():
			return
		}
		// Graceful shutdown is in progress. Wait for a second signal
		// or for the function to return (signal.Stop closes our window).
		// If a second signal arrives, the deferred cleanup is the thing
		// that's stuck — os.Exit is intentional here as the only way out.
		sig, ok := <-sigCh
		if ok {
			logger.Warn("received second signal, forcing exit", "signal", sig)
			os.Exit(1)
		}
	}()

	// stopSignals deregisters the signal handler and closes the channel
	// so the signal goroutine can exit cleanly when shutdown completes.
	stopSignals := func() {
		signal.Stop(sigCh)
		close(sigCh)
	}

	a, err := app.New(ctx, cfg, logger, stdout, llmSetup.Client, llmSetup.OllamaClients, llmSetup.Catalog)
	if err != nil {
		stopSignals()
		cancel()
		return err
	}
	// LIFO ordering: cancel fires first (stops goroutines), then Close
	// tears down the resources they were using.
	defer a.Close()
	defer cancel()
	defer stopSignals()

	// Log with the fully-configured logger (file handler, index handler,
	// correct level/format) so this line is captured in rotated logs.
	a.Logger().Info("config loaded",
		"path", cfgPath,
		"port", cfg.Listen.Port,
		"model", llmSetup.Catalog.DefaultModel,
		"ollama_resources", len(llmSetup.OllamaClients),
		"primary_ollama_url", llmSetup.Catalog.PrimaryOllamaURL(),
	)

	if err := a.StartWorkers(ctx); err != nil {
		return err
	}

	return a.Serve(ctx)
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

type llmSetup struct {
	Catalog       *models.Catalog
	Client        llm.Client
	OllamaClients map[string]*llm.OllamaClient
}

func createLLMSetup(cfg *config.Config, logger *slog.Logger) (*llmSetup, error) {
	catalog, err := models.BuildCatalog(cfg)
	if err != nil {
		return nil, fmt.Errorf("build model catalog: %w", err)
	}
	normalizeConfiguredModelRefs(cfg, catalog)

	bundle, err := models.BuildClients(catalog, cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("build llm clients: %w", err)
	}

	logger.Info("LLM client initialized",
		"default_model", catalog.DefaultModel,
		"resources", len(catalog.Resources),
		"deployments", len(catalog.Deployments),
	)

	return &llmSetup{
		Catalog:       catalog,
		Client:        bundle.Client,
		OllamaClients: bundle.OllamaClients,
	}, nil
}

func normalizeConfiguredModelRefs(cfg *config.Config, catalog *models.Catalog) {
	cfg.Models.Default = catalog.DefaultModel
	cfg.Models.RecoveryModel = catalog.RecoveryModel

	resolve := func(ref string) string {
		if ref == "" {
			return ""
		}
		if id, err := catalog.ResolveModelRef(ref); err == nil {
			return id
		}
		return ref
	}

	cfg.Archive.MetadataModel = resolve(cfg.Archive.MetadataModel)
	cfg.Extraction.Model = resolve(cfg.Extraction.Model)
	cfg.Media.SummarizeModel = resolve(cfg.Media.SummarizeModel)
	cfg.Attachments.Vision.Model = resolve(cfg.Attachments.Vision.Model)
}

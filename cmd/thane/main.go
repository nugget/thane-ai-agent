// Thane is an autonomous Home Assistant agent.
//
// It exposes an OpenAI-compatible API, an optional Ollama-compatible API
// (for Home Assistant integration), and a CLI for one-shot queries and
// document ingestion. Configuration is loaded from a single YAML file
// at {workspace}/core/config.yaml (see [config.FindConfig]).
//
// Usage:
//
//	thane serve              Start the API server
//	thane init [dir]         Initialize a working directory with defaults
//	thane ask <question>     Ask a single question (for testing)
//	thane ingest <file.md>   Import a markdown document into the fact store
//	thane version            Print version and build information
//	thane -o json version    Output version information as JSON
//
// The one-off log layout migration (#937) ships as a separate
// binary at cmd/archive-migration/.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nugget/thane-ai-agent/internal/app"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/coreintegrity"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// ExitTerminal is the exit status for a failure that retrying cannot
// fix: a missing or invalid config, a core that fails verification, a
// malformed command line. It is sysexits.h EX_CONFIG.
//
// The distinction a supervisor needs is not which subsystem failed but
// whether waiting will help. A broker that is briefly unreachable
// deserves a restart; a core with an unsigned config will fail
// identically forever, and a supervisor that keeps restarting it turns
// one clear error into an endless stream of them. systemd expresses this
// as RestartPreventExitStatus=78.
const ExitTerminal = 78

// main is intentionally minimal. It constructs the OS-level environment
// (context, stdio, argv) and delegates immediately to [run]. This keeps
// os.Exit, os.Stdout, and os.Args out of the application logic so that
// the full startup-to-shutdown lifecycle can be driven from tests.
func main() {
	ctx := context.Background()

	if err := run(ctx, os.Stdout, os.Stderr, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(exitCodeFor(err))
	}
}

// terminalError marks a failure a restart cannot resolve.
type terminalError struct{ err error }

func (e terminalError) Error() string { return e.err.Error() }
func (e terminalError) Unwrap() error { return e.err }

// terminal wraps an error as needing human intervention.
func terminal(err error) error {
	if err == nil {
		return nil
	}
	return terminalError{err: err}
}

// exitCodeFor maps an error to a process exit status.
func exitCodeFor(err error) int {
	var t terminalError
	if errors.As(err, &t) {
		return ExitTerminal
	}
	return 1
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
// errInsecureConfigNeedsPath is returned when -insecure-config is given
// without a usable path.
var errInsecureConfigNeedsPath = errors.New("-insecure-config needs a path (for example: -insecure-config /etc/thane/config.yaml); omit it entirely to load <workspace>/core/config.yaml")

// thaneCommands are the subcommand names. A flag expecting a value must
// not swallow one of these: the operator who typed it meant the command.
var thaneCommands = map[string]bool{
	"serve": true, "init": true, "validate": true, "ask": true,
	"ingest": true, "caps": true, "health": true, "version": true,
}

// looksLikeCommandOrFlag reports whether a token is a subcommand or
// another flag, and so cannot be the value the preceding flag wanted.
func looksLikeCommandOrFlag(token string) bool {
	return thaneCommands[token] || strings.HasPrefix(token, "-")
}

func run(ctx context.Context, stdout io.Writer, stderr io.Writer, args []string) error {
	// Parse arguments by hand. The flag package relies on package-level
	// globals (flag.CommandLine), which makes it impossible to call run()
	// concurrently from tests. Our argument surface is small enough that
	// manual parsing is clearer than bringing in a CLI framework.
	var configPath string
	var workspacePath string
	var outputFmt string // "text" (default) or "json"
	var command string
	var cmdArgs []string

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-insecure-config" && i+1 < len(args) && !looksLikeCommandOrFlag(args[i+1]):
			configPath = args[i+1]
			i++ // skip the value
		case args[i] == "-insecure-config":
			// Reached when the flag is last, or when the next token is a
			// subcommand or another flag. Consuming a subcommand as the
			// path would report "config file not found: validate", which
			// sends the operator hunting for a typo they did not make.
			return terminal(errInsecureConfigNeedsPath)
		case strings.HasPrefix(args[i], "-insecure-config="):
			configPath = strings.TrimPrefix(args[i], "-insecure-config=")
			if strings.TrimSpace(configPath) == "" {
				return terminal(errInsecureConfigNeedsPath)
			}
		case args[i] == "-config" || strings.HasPrefix(args[i], "-config="):
			// Renamed rather than aliased. A config outside core cannot
			// be covered by the instance's signed history — that is what
			// verification means — so loading one is insecure by
			// construction, and the flag that does it should say so
			// before it is typed, not after it is diagnosed.
			return terminal(fmt.Errorf("-config was renamed to -insecure-config\n\nThane loads its config from <workspace>/core/config.yaml, where it is signed and version-controlled. Pass -workspace to point at a different instance. Use -insecure-config only to load a config from outside the trust boundary, for recovery"))
		case args[i] == "-workspace" && i+1 < len(args):
			workspacePath = args[i+1]
			i++ // skip the value
		case strings.HasPrefix(args[i], "-workspace="):
			workspacePath = strings.TrimPrefix(args[i], "-workspace=")
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
		return runServe(ctx, stdout, stderr, configPath, workspacePath)
	case "init":
		return runInitCommand(stdout, stderr, cmdArgs)
	case "validate":
		return runValidate(stdout, configPath, workspacePath, outputFmt)
	case "ask":
		if len(cmdArgs) == 0 {
			return fmt.Errorf("usage: thane ask <question>")
		}
		return runAsk(ctx, stdout, stderr, configPath, workspacePath, cmdArgs)
	case "ingest":
		if len(cmdArgs) == 0 {
			return fmt.Errorf("usage: thane ingest <file.md>")
		}
		return runIngest(ctx, stdout, stderr, configPath, workspacePath, cmdArgs[0])
	case "version":
		return runVersion(stdout, outputFmt)
	case "health":
		return runHealth(ctx, stdout, cmdArgs)
	case "caps":
		return runCaps(ctx, stdout, configPath, workspacePath, outputFmt, cmdArgs)
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

// runHealth probes a running daemon's /health endpoint and returns a
// non-nil error (non-zero exit) when it is unreachable or unhealthy. It
// is the container HEALTHCHECK entrypoint: the distroless runtime image
// has no shell or wget, so the binary checks itself. The target URL
// defaults to the local server and may be overridden by the first arg.
func runHealth(ctx context.Context, w io.Writer, args []string) error {
	url := "http://127.0.0.1:8080/health"
	if len(args) > 0 && args[0] != "" {
		url = args[0]
	}

	client := httpkit.NewClient(httpkit.WithTimeout(3 * time.Second))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer httpkit.DrainAndClose(resp.Body, 4096)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health check returned HTTP %d", resp.StatusCode)
	}
	fmt.Fprintln(w, "ok")
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
	fmt.Fprintln(w, "  validate     Parse and validate the config without starting services")
	fmt.Fprintln(w, "  ask          Ask a single question (for testing)")
	fmt.Fprintln(w, "  ingest       Import markdown docs into fact store")
	fmt.Fprintln(w, "  caps         Show resolved capability tags from a running daemon")
	fmt.Fprintln(w, "  health [url] Probe a running daemon's /health endpoint (exit 0 if healthy)")
	fmt.Fprintln(w, "  version      Show version information")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -workspace <dir>  Instance workspace (default: ~/Thane)")
	fmt.Fprintln(w, "  -insecure-config <path>")
	fmt.Fprintln(w, "                    Load config from outside the trust boundary,")
	fmt.Fprintln(w, "                    for recovery. Not signature-verified.")
	fmt.Fprintln(w, "  -o, --output fmt  Output format: text (default) or json")
	fmt.Fprintln(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Config location:")
	fmt.Fprintln(w, "  <workspace>/core/config.yaml — the canonical location, and the only")
	fmt.Fprintln(w, "  one Thane discovers. Config lives inside core so it can be signed")
	fmt.Fprintln(w, "  and version-controlled; it decides what the rest of the system")
	fmt.Fprintln(w, "  trusts. -insecure-config loads a path outside that boundary.")
	return nil
}

// runAsk handles the "thane ask <question>" subcommand. It boots a
// minimal agent (in-memory conversation store, no router, no scheduler)
// and processes a single question, printing the response to stdout.
// Useful for quick smoke tests and debugging without starting the server.
func runAsk(ctx context.Context, stdout io.Writer, stderr io.Writer, configPath, workspacePath string, args []string) error {
	logger := newLogger(stdout, slog.LevelInfo, "text")

	question := strings.Join(args, " ")

	cfg, cfgPath, err := loadConfig(configPath, workspacePath)
	if err != nil {
		return err
	}
	logger.Info("config loaded", "path", cfgPath)

	// Home Assistant client (optional — ask works without it)
	var ha *homeassistant.Client
	if cfg.HomeAssistant.Configured() {
		ha = homeassistant.NewClient(cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, logger)
	}

	llmSetup, err := createLLMSetup(ctx, cfg, logger)
	if err != nil {
		return err
	}
	logLLMSetup(logger, llmSetup)

	talentLoader := talents.NewLoader(cfg.TalentsDir)
	cliTalents, _ := talentLoader.Talents()

	// In-memory store is fine for a single question — nothing to persist.
	mem := memory.NewStore(100)

	// Minimal loop: no router, no scheduler, no compactor. The default
	// model handles everything for CLI one-shots.
	var haInject homeassistant.StateFetcher
	if ha != nil {
		haInject = ha
	}
	loop, err := agent.NewLoop(agent.LoopOptions{
		Logger:        logger,
		Memory:        mem,
		HomeAssistant: ha,
		LLM:           llmSetup.Client,
		Model:         llmSetup.Catalog.DefaultModel,
		ParsedTalents: cliTalents,
		HAInject:      haInject,
	})
	if err != nil {
		return fmt.Errorf("build agent loop: %w", err)
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
func runIngest(ctx context.Context, stdout io.Writer, stderr io.Writer, configPath, workspacePath string, filePath string) error {
	logger := newLogger(stdout, slog.LevelInfo, "text")
	logger.Info("ingesting markdown document", "file", filePath)

	cfg, _, err := loadConfig(configPath, workspacePath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	factDB, err := database.Open(cfg.DataDir + "/knowledge.db")
	if err != nil {
		return fmt.Errorf("open knowledge database: %w", err)
	}
	defer factDB.Close()
	factStore, err := knowledge.NewStore(factDB, logger)
	if err != nil {
		return fmt.Errorf("open fact store: %w", err)
	}

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
//
// gateOnCoreIntegrity refuses to serve when the instance's core is not
// in the state the runtime requires.
//
// This is the point of the whole arc: a config that decides what the
// system trusts is worth nothing unless something checks that it is the
// config an entitled party actually committed. The gate applies to serve
// rather than to every subcommand because serve is what runs
// autonomously and unattended; the one-shot commands are an operator
// already sitting at the keyboard.
//
// A config loaded through -insecure-config skips the gate by definition
// — it lives outside the boundary the gate checks — but says so loudly,
// because an instance running unverified should be obvious in the logs
// without anyone going looking.
func gateOnCoreIntegrity(ctx context.Context, logger *slog.Logger, cfg *config.Config, explicitConfig string) error {
	if explicitConfig != "" {
		logger.Warn("running on a config from outside the trust boundary; it carries no verified history and no signature was checked",
			"config", explicitConfig,
			"canonical", cfg.CoreConfigPath(),
		)
		return nil
	}
	report, err := coreintegrity.Run(ctx, cfg.Workspace.Path, coreintegrity.Options{
		ConfigFileName: config.ConfigFileName,
		SeedSigners:    app.CoreSeedSigners(cfg),
	})
	if err != nil {
		return fmt.Errorf("check core integrity: %w", err)
	}
	if report.OK() {
		logger.Info("core integrity verified", "core", report.CorePath, "checks", len(report.Checks))
		return nil
	}

	failed := make([]string, 0, len(report.Checks))
	for _, check := range report.Failures() {
		failed = append(failed, check.Name)
	}
	// Logged as well as returned: a supervisor scraping structured logs
	// gets the failing check names as fields, without parsing the human
	// message written for the operator.
	logger.Error("refusing to start: core integrity check failed",
		"core", report.CorePath,
		"failed_checks", failed,
		"exit_code", ExitTerminal,
	)

	var b strings.Builder
	fmt.Fprintf(&b, "refusing to start: core integrity check failed for %s\n", report.CorePath)
	for _, check := range report.Failures() {
		fmt.Fprintf(&b, "\n  %s: %s\n", check.Name, check.Detail)
		if check.Fix != "" {
			fmt.Fprintf(&b, "    fix: %s\n", check.Fix)
		}
	}
	fmt.Fprintf(&b, "\nRun 'thane validate' for the full report. To start anyway with a config from outside the trust boundary, use -insecure-config")
	return terminal(errors.New(b.String()))
}

func runServe(ctx context.Context, stdout io.Writer, stderr io.Writer, configPath, workspacePath string) error {
	logger := newLogger(stdout, slog.LevelInfo, "text")
	logger.Info("starting Thane", "version", buildinfo.Version, "commit", buildinfo.GitCommit, "branch", buildinfo.GitBranch, "built", buildinfo.BuildTime)

	cfg, cfgPath, err := loadConfig(configPath, workspacePath)
	if err != nil {
		return terminal(err)
	}
	if err := gateOnCoreIntegrity(ctx, logger, cfg, configPath); err != nil {
		return err
	}

	llmSetup, err := createLLMSetup(ctx, cfg, logger)
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

	a, err := app.New(ctx, cfg, logger, stdout, llmSetup.Client, llmSetup.OllamaClients, llmSetup.HealthClients, llmSetup.ModelRuntime)
	if err != nil {
		stopSignals()
		cancel()
		// A failure in the authored content of the core root — a loop
		// definition that does not parse — is not resolved by starting
		// again. Marking it terminal stops the supervisor rather than
		// letting it re-read the same document until someone notices.
		var authoring *app.CoreAuthoringError
		if errors.As(err, &authoring) {
			return terminal(err)
		}
		return err
	}
	// LIFO ordering: cancel fires first (stops goroutines), then Close
	// tears down the resources they were using.
	defer a.Close()
	defer cancel()
	defer stopSignals()

	logLLMSetup(a.Logger(), llmSetup)

	// Log with the fully-configured logger (file handler, index handler,
	// correct level/format) so this line is captured in rotated logs.
	a.Logger().Info("config loaded",
		"path", cfgPath,
		"port", cfg.Listen.Port,
		"model", llmSetup.Catalog.DefaultModel,
		"resource_clients", len(llmSetup.HealthClients),
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

// loadConfig parses the runtime configuration. Without an explicit
// path it loads the one canonical location, {workspace}/core/config.yaml,
// so the file the rest of the system trusts always has a fixed name.
// Returns the parsed config, the path that was loaded, and any error.
func loadConfig(explicit, workspace string) (*config.Config, string, error) {
	cfgPath, err := config.FindConfig(explicit, workspace)
	if err != nil {
		return nil, "", err
	}

	// The fallback applies only when a workspace was actually named. If
	// it defaulted, an out-of-core config would silently adopt ~/Thane
	// and be judged against an instance the operator never mentioned.
	fallbackWorkspace := ""
	if strings.TrimSpace(workspace) != "" {
		// An unresolvable -workspace is reported here rather than
		// swallowed. Falling back to empty would leave the instance
		// running with no workspace at all — file tools disabled — and
		// surface later as something that looks unrelated to the flag
		// that caused it.
		resolved, wErr := config.ExpandWorkspace(workspace)
		if wErr != nil {
			return nil, "", terminal(wErr)
		}
		fallbackWorkspace = resolved
	}
	cfg, err := config.LoadWithWorkspace(cfgPath, fallbackWorkspace)
	if err != nil {
		return nil, cfgPath, fmt.Errorf("load config %s: %w", cfgPath, err)
	}
	if explicit != "" {
		// Marked at load rather than at the gate so every command
		// inherits it, not only the one that verifies. A config named
		// on the command line sits outside the boundary by definition,
		// and nothing downstream should have to re-derive that.
		cfg.MarkUnverified()
	}

	return cfg, cfgPath, nil
}

type llmSetup struct {
	Catalog       *fleet.Catalog
	ModelRegistry *fleet.Registry
	ModelRuntime  *fleet.Runtime
	Client        llm.Client
	HealthClients map[string]fleet.ResourceHealthClient
	OllamaClients map[string]*modelproviders.OllamaClient
}

func createLLMSetup(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*llmSetup, error) {
	baseCatalog, err := fleet.BuildCatalog(cfg)
	if err != nil {
		return nil, fmt.Errorf("build model catalog: %w", err)
	}
	normalizeConfiguredModelRefs(cfg, baseCatalog)

	runtime, err := fleet.NewRuntime(ctx, baseCatalog, cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("build model runtime: %w", err)
	}

	return &llmSetup{
		Catalog:       runtime.Registry().Catalog(),
		ModelRegistry: runtime.Registry(),
		ModelRuntime:  runtime,
		Client:        runtime.Client(),
		HealthClients: runtime.HealthClients(),
		OllamaClients: runtime.OllamaClients(),
	}, nil
}

func logLLMSetup(logger *slog.Logger, setup *llmSetup) {
	if logger == nil || setup == nil {
		return
	}

	snapshot := setup.ModelRegistry.Snapshot()
	if snapshot == nil {
		return
	}

	for _, res := range snapshot.Resources {
		switch {
		case res.LastError != "":
			logger.Warn("model inventory discovery failed",
				"resource", res.ID,
				"provider", res.Provider,
				"error", res.LastError,
			)
		case res.LastRefresh != "":
			logger.Info("model inventory discovered",
				"resource", res.ID,
				"provider", res.Provider,
				"models", res.DiscoveredModels,
			)
		}
	}

	logger.Info("LLM client initialized",
		"default_model", snapshot.DefaultModel,
		"resources", len(snapshot.Resources),
		"deployments", len(snapshot.Deployments),
		"discovered_deployments", countDiscoveredDeployments(snapshot),
	)
}

func countDiscoveredDeployments(snapshot *fleet.RegistrySnapshot) int {
	if snapshot == nil {
		return 0
	}
	discovered := 0
	for _, dep := range snapshot.Deployments {
		if dep.Source == fleet.DeploymentSourceDiscovered {
			discovered++
		}
	}
	return discovered
}

func normalizeConfiguredModelRefs(cfg *config.Config, catalog *fleet.Catalog) {
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

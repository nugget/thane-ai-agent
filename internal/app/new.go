package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
)

// New constructs and initializes a fully wired App from the provided
// configuration. The llmClient, resource health clients, and model
// registry/catalog are pre-constructed by
// the caller (cmd/thane) so that runAsk and runServe can share the
// same startup normalization path without importing internal/app.
//
// New opens resources, wires dependencies, and registers background
// workers but does not start them. Call [App.StartWorkers] to launch
// all deferred goroutines and persistent loops, then [App.Serve] to
// start external servers and block until shutdown.
//
// All resources that require cleanup are registered on the closer
// stack via [onClose] / [onCloseErr]. Workers register their stop
// functions during [StartWorkers]. The LIFO ordering guarantees
// workers stop before the resources they depend on are released.
// See [App.shutdown] for the two-phase teardown sequence.
//
// Initialization is split into focused phases, each in its own file:
//
//   - [initLogging]              — logger, dataset writer, index DB, content writer
//   - [initStores]               — data stores, HA client, connwatch, router, scheduler
//   - [initAgentLoop]            — agent loop, path resolver, context injection
//   - [initChannels]             — tools, email, forge, MCP, Signal, facts, contacts
//   - [initAwareness]            — context providers, watchlist, person tracker, state watcher
//   - [initDelegation]           — delegate executor, notification routing, lenses, channel tags
//   - [initServers]              — API server, checkpointer, MQTT, dashboard, metacognitive
//   - [finalizeCapabilityTags]   — resolve capability tags from fully-assembled registry
//
// Phase ordering rules for tool visibility (see #733):
//
//   - Every global tool — including mqtt_wake_* registered in initServers
//     and watchlist tools registered in initAwareness — must be in the
//     registry before finalizeCapabilityTags runs so the snapshot
//     reflects the complete catalog. The finalizer is deliberately last.
//   - Subsystems whose backing runtime starts asynchronously (Signal)
//     declare their tools up front via tools.Provider and return
//     tools.ErrUnavailable from the handler until Bind is called. Those
//     tools DO appear in the snapshot; they are not in s.deferredTools.
//   - s.deferredTools is a narrow remaining escape hatch for tool
//     families whose handler is still registered inside a deferWorker
//     closure (today: macos_calendar_events). New subsystems should use
//     Provider + declared-but-unavailable instead of adding entries here.
//   - initDelegation no longer takes a capability-tag snapshot; it
//     creates the delegate executor without tag state and leaves
//     alwaysActiveTags / SetTagContextFunc to the finalizer.
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger, stdout io.Writer, llmClient llm.Client, ollamaClients map[string]*modelproviders.OllamaClient, healthClients map[string]fleet.ResourceHealthClient, modelRuntime *fleet.Runtime) (*App, error) {
	if modelRuntime == nil {
		return nil, fmt.Errorf("nil model runtime")
	}
	modelRegistry := modelRuntime.Registry()
	modelCatalog := modelRegistry.Catalog()
	a := &App{
		cfg:                   cfg,
		logger:                logger,
		stdout:                stdout,
		llmClient:             llmClient,
		ollamaClients:         ollamaClients,
		resourceHealthClients: healthClients,
		modelRuntime:          modelRuntime,
		modelRegistry:         modelRegistry,
		modelCatalog:          modelCatalog,
	}

	// Augment PATH before any exec.LookPath calls (tool registration,
	// media client init, etc.) so Homebrew and user-configured binaries
	// are discoverable. Logging is deferred until the final logger is
	// configured (the initial logger is Info-level so Debug would be lost).
	augmentedDirs := augmentPath(cfg.ExtraPath)

	if err := a.initLogging(augmentedDirs); err != nil {
		return nil, err
	}

	// Late-bind the configured logger into provider clients that were
	// built during bootstrap with the Info-level stdout logger. Without
	// this, every Debug log inside the model providers (preparing
	// request, response received, outbound cache markers, etc.) is
	// suppressed because the bootstrap logger's handler short-circuits
	// at LevelInfo. Anything else constructed before initLogging that
	// emits Debug logs needs a similar rebind hook.
	a.modelRuntime.SetLogger(a.logger)

	s := &newState{ctx: ctx}

	if err := a.initStores(s); err != nil {
		return nil, err
	}
	if err := a.initAgentLoop(s); err != nil {
		return nil, err
	}
	if err := a.initChannels(s); err != nil {
		return nil, err
	}
	if err := a.initAwareness(s); err != nil {
		return nil, err
	}
	if err := a.initDelegation(s); err != nil {
		return nil, err
	}
	if err := a.initServers(s); err != nil {
		return nil, err
	}
	if err := a.finalizeCapabilityTags(s); err != nil {
		return nil, err
	}

	return a, nil
}

// newHandler creates a structured [slog.Handler] that writes to w at
// the given level and format. This is the shared handler construction
// used by newLogger and (with optional wrapping) by the serve command.
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

// augmentPath prepends directories to the process PATH so that
// exec.LookPath (used during tool registration) can find binaries
// installed outside the default system PATH. On macOS, Homebrew
// directories are added automatically if they exist on disk.
// Returns the list of directories that were prepended (for deferred
// logging after the final logger is configured).
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

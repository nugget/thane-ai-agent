//go:build uiharness

// UI harness: a build-tagged, fake-backed instance of the native /v1 server
// for iterating on the web console without a real Thane, a database, or
// production. It registers a curated subset of the real handlers — so the
// JSON it serves is byte-identical to production — backed by in-memory
// synthetic data. Endpoints not registered here simply 404, which the console
// degrades through (client.tryGet treats 404/503 as "absent").
//
// Build and run via cmd/uiharness (also //go:build uiharness); never compiled
// into the production binary.
package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// harnessLoopReg is a static LoopStatusReader seeded with synthetic loops.
type harnessLoopReg struct {
	statuses []looppkg.Status
	byID     map[string]looppkg.Status
}

func (h harnessLoopReg) Statuses() []looppkg.Status { return h.statuses }

func (h harnessLoopReg) StatusByID(id string) (looppkg.Status, bool) {
	st, ok := h.byID[id]
	return st, ok
}

// harnessLogQuerier returns an empty log set for any query — enough to keep the
// log panel functional without seeding a structured log index.
type harnessLogQuerier struct{}

func (harnessLogQuerier) Query(logging.QueryParams) ([]logging.LogEntry, error) {
	return nil, nil
}

// harnessLoops is the synthetic loop tree: a processing supervisor root with a
// mix of sleeping, event-driven, and errored children, plus a parent/child
// channel pair — enough to exercise the graph's hierarchy, state styling, and
// service-degradation paths.
func harnessLoops() []looppkg.Status {
	now := time.Now()
	boot := now.Add(-6 * time.Hour)
	return []looppkg.Status{
		{
			ID: "supervisor", Name: "supervisor", State: looppkg.StateProcessing,
			StartedAt: boot, LastWakeAt: now.Add(-2 * time.Second),
			Iterations: 1423, Attempts: 1440,
			TotalInputTokens: 9_200_000, TotalOutputTokens: 1_100_000,
			LastInputTokens: 7400, LastOutputTokens: 820, ContextWindow: 200000,
		},
		{
			ID: "curator", Name: "curator", State: looppkg.StateSleeping, ParentID: "supervisor",
			StartedAt: boot, Iterations: 318, Attempts: 320,
			TotalInputTokens: 5_400_000, TotalOutputTokens: 410_000, ContextWindow: 131072,
		},
		{
			ID: "archivist", Name: "archivist", State: looppkg.StateSleeping, ParentID: "supervisor",
			StartedAt: boot, Iterations: 96, Attempts: 96, ContextWindow: 131072,
		},
		{
			ID: "signal", Name: "signal", State: looppkg.StateWaiting, ParentID: "supervisor",
			StartedAt: boot, EventDriven: true, Iterations: 51, Attempts: 51,
		},
		{
			ID: "signal/aimee", Name: "signal/aimee", State: looppkg.StateProcessing, ParentID: "signal",
			StartedAt: now.Add(-90 * time.Minute), LastWakeAt: now.Add(-1 * time.Second),
			Iterations: 12, Attempts: 12, LastInputTokens: 5200, LastOutputTokens: 640, ContextWindow: 200000,
		},
		{
			ID: "mqtt", Name: "mqtt", State: looppkg.StateError, ParentID: "supervisor",
			StartedAt: boot, EventDriven: true,
			LastError: "connection refused: broker unreachable", ConsecutiveErrors: 4,
		},
	}
}

// harnessHealth reports synthetic dependency health, with mqtt degraded so the
// system status reads "degraded" and the mqtt loop renders in its degraded
// styling (the graph matches loop names against service keys).
func harnessHealth() map[string]DependencyStatus {
	stamp := time.Now().Format(time.RFC3339)
	return map[string]DependencyStatus{
		"signal":         {Name: "signal", Ready: true, LastCheck: stamp},
		"home_assistant": {Name: "home_assistant", Ready: true, LastCheck: stamp},
		"mqtt":           {Name: "mqtt", Ready: false, LastCheck: stamp, LastError: "broker unreachable"},
	}
}

// RunUIHarness serves the web console at addr, backed by synthetic /v1 data.
// staticDir is the directory of console assets to serve live (so JS/CSS/HTML
// edits show on reload without rebuilding the harness).
func RunUIHarness(addr, staticDir string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	loops := harnessLoops()
	byID := make(map[string]looppkg.Status, len(loops))
	for _, l := range loops {
		byID[l.ID] = l
	}

	s := &Server{
		logger:       logger,
		loopRegistry: harnessLoopReg{statuses: loops, byID: byID},
		logQuerier:   harnessLogQuerier{},
		eventBus:     events.New(),
		healthDeps:   harnessHealth,
	}

	mux := http.NewServeMux()

	// Curated /v1 surface the node graph consumes. Anything not here 404s and
	// the console degrades through it.
	mux.HandleFunc("GET /v1/system", s.handleSystem)
	mux.HandleFunc("GET /v1/version", s.handleVersion)
	mux.HandleFunc("GET /v1/loops", s.handleLoops)
	mux.HandleFunc("GET /v1/loops/events", s.handleLoopEvents)
	mux.HandleFunc("GET /v1/loops/{id}", s.handleLoop)
	mux.HandleFunc("GET /v1/loops/{id}/logs", s.handleLoopLogs)

	// Console static assets, served live from staticDir.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
	})

	logger.Warn("ui harness listening", "addr", addr, "static", staticDir)
	return http.ListenAndServe(addr, mux)
}

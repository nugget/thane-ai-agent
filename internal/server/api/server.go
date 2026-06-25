// Package api implements Thane's HTTP API surfaces. Server is the Thane-native,
// loops-first /v1 management and observability API on the primary listen port;
// OpenAIServer and OllamaServer are the frozen OpenAI- and Ollama-compatible
// shims, each served on its own port.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/platform/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/server/openapi"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// WebServerRegistrar is implemented by types that can register HTTP
// routes on a ServeMux. It decouples the API server from the concrete
// web.WebServer type so that the web package can be wired in without a
// circular import.
type WebServerRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// writeJSON encodes v as JSON to w, logging any errors at debug level.
// Errors here typically mean the client disconnected mid-response,
// which is not actionable but worth tracking for debugging.
func writeJSON(w http.ResponseWriter, v any, logger *slog.Logger) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Debug("failed to write JSON response", "error", err)
	}
}

// DependencyStatus describes the health of a single watched dependency.
type DependencyStatus struct {
	Name      string `json:"name"`
	Ready     bool   `json:"ready"`
	LastCheck string `json:"last_check,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// HealthStatusFunc returns dependency health information for the /health endpoint.
type HealthStatusFunc func() map[string]DependencyStatus

// TokenObserver is notified after each LLM completion with the token
// counts from that request. Implementations must be safe for
// concurrent use.
type TokenObserver interface {
	OnTokens(inputTokens, outputTokens int)
}

// Server is the HTTP API server.
type Server struct {
	address                            string
	port                               int
	loop                               *agent.Loop
	router                             *router.Router
	checkpointer                       *checkpoint.Checkpointer
	memoryStore                        *memory.SQLiteStore
	archiveStore                       *memory.ArchiveStore
	healthDeps                         HealthStatusFunc
	tokenObserver                      TokenObserver
	eventBus                           *events.Bus
	owuTracker                         *OWUTracker
	webServer                          WebServerRegistrar
	companionHandler                   http.Handler
	modelRegistry                      *fleet.Registry
	contactStore                       *contacts.Store
	loopDefinitionRegistry             *looppkg.DefinitionRegistry
	loopDefinitionView                 func() *looppkg.DefinitionRegistryView
	loopRegistry                       LoopStatusReader
	logQuerier                         LogQuerier
	requestReader                      RequestReader
	schedulerReader                    SchedulerReader
	capSurface                         func() []toolcatalog.CapabilitySurface
	usageStore                         *usage.Store
	persistModelRegistryPolicy         func(string, fleet.DeploymentPolicy) error
	deleteModelRegistryPolicy          func(string) error
	persistModelRegistryResourcePolicy func(string, fleet.ResourcePolicy) error
	deleteModelRegistryResourcePolicy  func(string) error
	commitLoopDefinition               func(context.Context, looppkg.Spec, time.Time) error
	deleteLoopDefinition               func(string) error
	persistLoopDefinitionPolicy        func(string, looppkg.DefinitionPolicy) error
	deleteLoopDefinitionPolicy         func(string) error
	reconcileLoopDefinition            func(context.Context, string) error
	launchLoopDefinition               func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error)
	launchChatLoop                     func(context.Context, looppkg.Launch) (looppkg.LaunchResult, error)
	anthropicRateLimitSnapshot         func() *fleet.AnthropicRateLimitSnapshot
	logger                             *slog.Logger
	server                             *http.Server
	stats                              *SessionStats
}

// SetOWUTracker configures the Open WebUI loop tracker for dashboard visibility.
func (s *Server) SetOWUTracker(t *OWUTracker) {
	s.owuTracker = t
}

// SetConnManager sets the dependency health provider for the /health endpoint.
func (s *Server) SetConnManager(fn HealthStatusFunc) {
	s.healthDeps = fn
}

// ConfigureAnthropicRateLimitSnapshotSource configures the provider for the
// latest Anthropic rate-limit snapshot included in router stats.
func (s *Server) ConfigureAnthropicRateLimitSnapshotSource(fn func() *fleet.AnthropicRateLimitSnapshot) {
	s.anthropicRateLimitSnapshot = fn
}

// SetTokenObserver registers an observer that is notified after each
// LLM completion with the token counts from that request. This is used
// by the MQTT publisher's daily token accumulator.
func (s *Server) SetTokenObserver(obs TokenObserver) {
	s.tokenObserver = obs
}

// SetEventBus configures the event bus for the WebSocket event stream.
func (s *Server) SetEventBus(bus *events.Bus) {
	s.eventBus = bus
}

// SetWebServer configures the web dashboard. When set, the dashboard
// serves HTML at "/" and the old JSON root handler becomes a fallback.
func (s *Server) SetWebServer(ws WebServerRegistrar) {
	s.webServer = ws
}

// SetCompanionHandler configures the WebSocket handler for native
// companion app connections.
func (s *Server) SetCompanionHandler(h http.Handler) {
	s.companionHandler = h
}

// UseContactStore configures the native contact-directory API.
func (s *Server) UseContactStore(store *contacts.Store) {
	s.contactStore = store
}

// UseLoopDefinitionRegistry configures the persistent loop definition
// registry exposed by the API.
func (s *Server) UseLoopDefinitionRegistry(reg *looppkg.DefinitionRegistry) {
	s.loopDefinitionRegistry = reg
}

// ConfigureLoopDefinitionView configures the effective combined
// definition registry view used by loop read surfaces.
func (s *Server) ConfigureLoopDefinitionView(fn func() *looppkg.DefinitionRegistryView) {
	s.loopDefinitionView = fn
}

// ConfigureLoopDefinitionPersistence configures the durable-commit and
// delete callbacks for dynamic loop-definition overlay mutations. commit
// runs the full persist → overlay upsert → reconcile sequence (the same
// chokepoint the loop-authoring tools use) so the HTTP write path cannot
// drift from them.
//
// Contract: commit must tag its failures with *looppkg.CommitError (stage
// persist/register/reconcile) — that is what handleLoopDefinitionSet's
// respondLoopCommitError uses to map a register failure to 400 and a
// persist/reconcile failure to 500. A commit that returns a raw error
// instead silently collapses every failure to 500. App.commitLoopDefinition
// satisfies this; alternative wiring (tests, custom setups) must too.
func (s *Server) ConfigureLoopDefinitionPersistence(
	commit func(context.Context, looppkg.Spec, time.Time) error,
	remove func(string) error,
) {
	s.commitLoopDefinition = commit
	s.deleteLoopDefinition = remove
}

// ConfigureLoopDefinitionLifecycle configures runtime lifecycle
// callbacks for stored loop definitions: policy persistence, live
// reconcile, and launch-by-definition.
func (s *Server) ConfigureLoopDefinitionLifecycle(
	persistPolicy func(string, looppkg.DefinitionPolicy) error,
	deletePolicy func(string) error,
	reconcile func(context.Context, string) error,
	launch func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error),
) {
	s.persistLoopDefinitionPolicy = persistPolicy
	s.deleteLoopDefinitionPolicy = deletePolicy
	s.reconcileLoopDefinition = reconcile
	s.launchLoopDefinition = launch
}

// ConfigureChatLoopLauncher configures the request/reply loop launcher used
// by the native OpenAI-compatible chat endpoints.
func (s *Server) ConfigureChatLoopLauncher(launch func(context.Context, looppkg.Launch) (looppkg.LaunchResult, error)) {
	s.launchChatLoop = launch
}

// DashboardSnapshot returns a copy of the current session stats
// enriched with context window, message count, and build information.
// This is used by the web dashboard to display runtime overview data.
func (s *Server) DashboardSnapshot() SessionStatsSnapshot {
	snap := s.stats.Snapshot()
	memStats := s.loop.MemoryStats()
	if msgs, ok := memStats["messages"].(int); ok {
		snap.MessageCount = msgs
	}
	snap.ContextTokens = s.loop.GetTokenCount("default")
	snap.ContextWindow = s.loop.GetContextWindow()
	snap.Build = buildinfo.RuntimeInfo()
	return snap
}

// LastRequest returns when the most recent LLM request completed.
// Returns the zero value if no requests have been recorded. This
// method is safe for concurrent use.
func (s *Server) LastRequest() time.Time {
	return s.stats.LastRequest()
}

// SessionStats tracks token usage and cost for the current session.
type SessionStats struct {
	TotalInputTokens              int64     `json:"total_input_tokens"`
	TotalOutputTokens             int64     `json:"total_output_tokens"`
	TotalCacheCreationInputTokens int64     `json:"total_cache_creation_input_tokens"`
	TotalCacheReadInputTokens     int64     `json:"total_cache_read_input_tokens"`
	TotalRequests                 int64     `json:"total_requests"`
	EstimatedCostUSD              float64   `json:"estimated_cost_usd"`
	ReportedBalance               float64   `json:"reported_balance_usd,omitempty"`
	BalanceSetAt                  string    `json:"balance_set_at,omitempty"`
	LastRequestAt                 time.Time `json:"-"` // Used by MQTT publisher, not exposed in JSON.
	ByModel                       map[string]usage.Summary
	ByUpstreamModel               map[string]usage.Summary
	ByProvider                    map[string]usage.Summary
	ByResource                    map[string]usage.Summary
	pricing                       map[string]config.PricingEntry
	mu                            sync.Mutex
}

// Record accumulates token usage and cost for a model. Cost is computed
// from the config-driven pricing table.
func (s *SessionStats) Record(identity usage.ModelIdentity, inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalInputTokens += int64(inputTokens)
	s.TotalOutputTokens += int64(outputTokens)
	s.TotalCacheCreationInputTokens += int64(cacheCreationInputTokens)
	s.TotalCacheReadInputTokens += int64(cacheReadInputTokens)
	s.TotalRequests++
	s.LastRequestAt = time.Now()
	cost := usage.ComputeDetailedCostForIdentity(identity, inputTokens, cacheCreationInputTokens, cacheReadInputTokens, outputTokens, s.pricing)
	s.EstimatedCostUSD += cost
	recordSessionUsageSummary(s.ByModel, identity.Model, inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens, cost)
	recordSessionUsageSummary(s.ByUpstreamModel, identity.UpstreamModel, inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens, cost)
	recordSessionUsageSummary(s.ByProvider, identity.Provider, inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens, cost)
	recordSessionUsageSummary(s.ByResource, identity.Resource, inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens, cost)
}

// LastRequest returns when the most recent LLM request completed.
// Returns the zero value if no requests have been recorded.
func (s *SessionStats) LastRequest() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastRequestAt
}

// recordUsage records token usage in session stats and notifies the
// token observer (if set) so external consumers (e.g., the MQTT daily
// token accumulator) are updated.
func (s *Server) recordUsage(model string, inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens int) {
	var cat *fleet.Catalog
	if s.modelRegistry != nil {
		cat = s.modelRegistry.Catalog()
	}
	identity := usage.ResolveModelIdentity(model, cat)
	s.stats.Record(identity, inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens)
	if s.tokenObserver != nil {
		s.tokenObserver.OnTokens(inputTokens, outputTokens)
	}
}

func (s *SessionStats) SetBalance(balance float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ReportedBalance = balance
	s.BalanceSetAt = time.Now().UTC().Format(time.RFC3339)
}

// SessionStatsSnapshot is a copy-safe snapshot of session stats.
type SessionStatsSnapshot struct {
	TotalInputTokens              int64 `json:"total_input_tokens"`
	TotalOutputTokens             int64 `json:"total_output_tokens"`
	TotalCacheCreationInputTokens int64 `json:"total_cache_creation_input_tokens"`
	TotalCacheReadInputTokens     int64 `json:"total_cache_read_input_tokens"`
	// CacheHitRate is cache_read / (cache_read + cache_creation) over
	// the session so far, expressed as a fraction in [0, 1]. Exposed
	// to the dashboard so operators can see at a glance whether
	// prompt caching is actually saving tokens (cold start = near
	// zero, warm session = high).
	CacheHitRate     float64                  `json:"cache_hit_rate"`
	TotalRequests    int64                    `json:"total_requests"`
	EstimatedCostUSD float64                  `json:"estimated_cost_usd"`
	ReportedBalance  float64                  `json:"reported_balance_usd,omitempty"`
	BalanceSetAt     string                   `json:"balance_set_at,omitempty"`
	ByModel          map[string]usage.Summary `json:"by_model,omitempty"`
	ByUpstreamModel  map[string]usage.Summary `json:"by_upstream_model,omitempty"`
	ByProvider       map[string]usage.Summary `json:"by_provider,omitempty"`
	ByResource       map[string]usage.Summary `json:"by_resource,omitempty"`
	ContextTokens    int                      `json:"context_tokens"`
	ContextWindow    int                      `json:"context_window"`
	MessageCount     int                      `json:"message_count"`
	Build            map[string]string        `json:"build,omitempty"`
}

func (s *SessionStats) Snapshot() SessionStatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionStatsSnapshot{
		TotalInputTokens:              s.TotalInputTokens,
		TotalOutputTokens:             s.TotalOutputTokens,
		TotalCacheCreationInputTokens: s.TotalCacheCreationInputTokens,
		TotalCacheReadInputTokens:     s.TotalCacheReadInputTokens,
		CacheHitRate:                  llm.CacheHitRate(int(s.TotalCacheReadInputTokens), int(s.TotalCacheCreationInputTokens)),
		TotalRequests:                 s.TotalRequests,
		EstimatedCostUSD:              s.EstimatedCostUSD,
		ReportedBalance:               s.ReportedBalance,
		BalanceSetAt:                  s.BalanceSetAt,
		ByModel:                       cloneSessionUsageMap(s.ByModel),
		ByUpstreamModel:               cloneSessionUsageMap(s.ByUpstreamModel),
		ByProvider:                    cloneSessionUsageMap(s.ByProvider),
		ByResource:                    cloneSessionUsageMap(s.ByResource),
	}
}

type usageSummaryResponse struct {
	Start   string                 `json:"start"`
	End     string                 `json:"end"`
	Hours   int                    `json:"hours"`
	GroupBy string                 `json:"group_by,omitempty"`
	Summary *usage.Summary         `json:"summary"`
	Groups  []usage.GroupedSummary `json:"groups,omitempty"`
}

func recordSessionUsageSummary(dst map[string]usage.Summary, key string, inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens int, cost float64) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	sum := dst[key]
	sum.TotalRecords++
	sum.TotalInputTokens += int64(inputTokens)
	sum.TotalOutputTokens += int64(outputTokens)
	sum.TotalCacheCreationInputTokens += int64(cacheCreationInputTokens)
	sum.TotalCacheReadInputTokens += int64(cacheReadInputTokens)
	sum.TotalCostUSD += cost
	dst[key] = sum
}

func cloneSessionUsageMap(src map[string]usage.Summary) map[string]usage.Summary {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]usage.Summary, len(src))
	for key, sum := range src {
		dst[key] = sum
	}
	return dst
}

// NewServer creates a new API server. The pricing map drives cost
// estimation in session stats; pass nil for zero-cost defaults.
func NewServer(
	address string,
	port int,
	loop *agent.Loop,
	rtr *router.Router,
	pricing map[string]config.PricingEntry,
	registry *fleet.Registry,
	usageStore *usage.Store,
	persistPolicy func(string, fleet.DeploymentPolicy) error,
	deletePolicy func(string) error,
	persistResourcePolicy func(string, fleet.ResourcePolicy) error,
	deleteResourcePolicy func(string) error,
	logger *slog.Logger,
) *Server {
	return &Server{
		address:                            address,
		port:                               port,
		loop:                               loop,
		router:                             rtr,
		modelRegistry:                      registry,
		usageStore:                         usageStore,
		persistModelRegistryPolicy:         persistPolicy,
		deleteModelRegistryPolicy:          deletePolicy,
		persistModelRegistryResourcePolicy: persistResourcePolicy,
		deleteModelRegistryResourcePolicy:  deleteResourcePolicy,
		logger:                             logger,
		stats: &SessionStats{
			pricing:         pricing,
			ByModel:         make(map[string]usage.Summary),
			ByUpstreamModel: make(map[string]usage.Summary),
			ByProvider:      make(map[string]usage.Summary),
			ByResource:      make(map[string]usage.Summary),
		},
	}
}

// SetCheckpointer configures the checkpointer for checkpoint API endpoints.
func (s *Server) SetCheckpointer(cp *checkpoint.Checkpointer) {
	s.checkpointer = cp
}

// SetMemoryStore configures the memory store for history API endpoints.
func (s *Server) SetMemoryStore(ms *memory.SQLiteStore) {
	s.memoryStore = ms
}

// SetArchiveStore configures the archive store for archive API endpoints.
func (s *Server) SetArchiveStore(as *memory.ArchiveStore) {
	s.archiveStore = as
}

// Start begins serving HTTP requests.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Simplified chat endpoint (easier testing)
	mux.HandleFunc("POST /v1/chat", s.handleSimpleChat)

	// Health and system endpoints
	mux.HandleFunc("GET /v1/version", s.handleVersion)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /v1/system", s.handleSystem)
	mux.HandleFunc("GET /v1/system/logs", s.handleSystemLogs)

	// Insights — consolidated router, tool, and usage analytics
	mux.HandleFunc("GET /v1/insights/router", s.handleRouterInsights)
	mux.HandleFunc("GET /v1/insights/tools", s.handleToolInsights)
	mux.HandleFunc("GET /v1/insights/usage", s.handleUsageSummary)
	mux.HandleFunc("GET /v1/insights/capabilities", s.handleCapabilities)
	mux.HandleFunc("GET /v1/insights/capabilities/{tag}", s.handleCapability)

	// Request introspection — detail, routing decision, and tool calls
	mux.HandleFunc("GET /v1/requests/{id}", s.handleRequest)
	mux.HandleFunc("GET /v1/requests/{id}/routing", s.handleRequestRouting)
	mux.HandleFunc("GET /v1/requests/{id}/tools", s.handleRequestTools)

	// Model registry endpoints
	mux.HandleFunc("GET /v1/models", s.handleModelFleet)
	mux.HandleFunc("GET /v1/models/registry", s.handleModelRegistry)
	mux.HandleFunc("PUT /v1/models/registry/policy", s.handleModelRegistryPolicySet)
	mux.HandleFunc("DELETE /v1/models/registry/policy", s.handleModelRegistryPolicyDelete)
	mux.HandleFunc("PUT /v1/models/registry/resource-policy", s.handleModelRegistryResourcePolicySet)
	mux.HandleFunc("DELETE /v1/models/registry/resource-policy", s.handleModelRegistryResourcePolicyDelete)

	// Contact directory endpoints
	mux.HandleFunc("GET /v1/contacts", s.handleContactsList)
	mux.HandleFunc("GET /v1/contacts/{id}", s.handleContactGet)
	mux.HandleFunc("POST /v1/contacts", s.handleContactCreate)
	mux.HandleFunc("PUT /v1/contacts/{id}", s.handleContactUpdate)
	mux.HandleFunc("DELETE /v1/contacts/{id}", s.handleContactDelete)

	// Loop definition registry endpoints
	mux.HandleFunc("GET /v1/loop-definitions", s.handleLoopDefinitions)
	mux.HandleFunc("GET /v1/loop-definitions/{name}", s.handleLoopDefinitionGet)
	mux.HandleFunc("POST /v1/loop-definitions", s.handleLoopDefinitionSet)
	mux.HandleFunc("DELETE /v1/loop-definitions/{name}", s.handleLoopDefinitionDelete)
	mux.HandleFunc("POST /v1/loop-definitions/policy", s.handleLoopDefinitionPolicySet)
	mux.HandleFunc("DELETE /v1/loop-definitions/policy", s.handleLoopDefinitionPolicyDelete)
	mux.HandleFunc("POST /v1/loop-definitions/{name}/launch", s.handleLoopDefinitionLaunch)

	// Running loops (the loops-first protagonist), consolidated from the
	// dashboard's former /api/loops surface in the web package.
	mux.HandleFunc("GET /v1/loops", s.handleLoops)
	mux.HandleFunc("GET /v1/loops/events", s.handleLoopEvents)
	mux.HandleFunc("GET /v1/loops/{id}", s.handleLoop)
	mux.HandleFunc("GET /v1/loops/{id}/logs", s.handleLoopLogs)

	// Scheduler: durable scheduled tasks and their execution history,
	// surfacing the previously internal-only scheduler subsystem.
	mux.HandleFunc("GET /v1/schedules", s.handleSchedules)
	mux.HandleFunc("GET /v1/schedules/{id}", s.handleSchedule)
	mux.HandleFunc("GET /v1/schedules/{id}/executions", s.handleScheduleExecutions)

	// Checkpoint endpoints
	mux.HandleFunc("POST /v1/checkpoints", s.handleCheckpointCreate)
	mux.HandleFunc("GET /v1/checkpoints", s.handleCheckpointList)
	mux.HandleFunc("GET /v1/checkpoints/{id}", s.handleCheckpointGet)
	mux.HandleFunc("DELETE /v1/checkpoints/{id}", s.handleCheckpointDelete)
	mux.HandleFunc("POST /v1/checkpoints/{id}/restore", s.handleCheckpointRestore)

	// History endpoints
	mux.HandleFunc("GET /v1/conversations", s.handleConversationList)
	mux.HandleFunc("GET /v1/conversations/{id}", s.handleConversationGet)

	// Session stats
	mux.HandleFunc("GET /v1/sessions/stats", s.handleSessionStats)
	mux.HandleFunc("POST /v1/sessions/balance", s.handleSetBalance)
	mux.HandleFunc("POST /v1/sessions/reset", s.handleSessionReset)
	mux.HandleFunc("POST /v1/sessions/compact", s.handleSessionCompact)
	mux.HandleFunc("GET /v1/sessions/history", s.handleSessionHistory)

	// Archive endpoints
	mux.HandleFunc("GET /v1/archive/sessions", s.handleArchiveSessions)
	mux.HandleFunc("GET /v1/archive/sessions/{id}", s.handleArchiveSessionGet)
	mux.HandleFunc("GET /v1/archive/sessions/{id}/export", s.handleArchiveSessionExport)
	mux.HandleFunc("GET /v1/archive/search", s.handleArchiveSearch)
	mux.HandleFunc("GET /v1/archive/messages", s.handleArchiveMessages)
	mux.HandleFunc("GET /v1/archive/stats", s.handleArchiveStats)

	// First-party realtime WebSocket. /v1/realtime/ws is the canonical path
	// (per native.yaml); /v1/companion/ws and /v1/platform/ws are legacy
	// aliases for existing thane-agent-macos installs.
	if s.companionHandler != nil {
		mux.Handle("GET /v1/realtime/ws", s.companionHandler)
		mux.Handle("GET /v1/companion/ws", s.companionHandler)
		mux.Handle("GET /v1/platform/ws", s.companionHandler)
	}

	// When a WebServerRegistrar is wired in, it owns "/" and related
	// UI routes. Otherwise, fall back to the JSON root handler.
	if s.webServer != nil {
		s.webServer.RegisterRoutes(mux)
	} else {
		mux.HandleFunc("GET /", s.handleRoot)
	}

	// OpenAPI explorer: interactive reference for the native + compat
	// surfaces, served at /docs from embedded assets (vendored Scalar, no CDN).
	openapi.RegisterRoutes(mux)

	// Note: Ollama-compatible API is served on a separate port via OllamaServer
	// when ollama_api.enabled is true in config. Use RegisterOllamaRoutes()
	// only if you need single-port operation.

	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.address, s.port),
		Handler:      s.withLogging(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second, // Long for streaming responses
	}

	addr := s.address
	if addr == "" {
		addr = "0.0.0.0"
	}
	s.logger.Info("starting API server", "address", addr, "port", s.port)
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := logging.NewAccessResponseWriter(w)
		next.ServeHTTP(rw, r)
		s.logger.Info("request handled",
			"kind", logging.KindHTTPAccess,
			"server", "api",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.StatusCode(),
			"response_bytes", rw.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]string{
		"name":    "Thane",
		"version": buildinfo.Version,
		"status":  "ok",
	}, s.logger)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, buildinfo.RuntimeInfo(), s.logger)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	health := map[string]any{"status": "healthy"}
	if s.healthDeps != nil {
		deps := s.healthDeps()
		health["dependencies"] = deps
		// Degrade to "degraded" if any dependency is down.
		for _, dep := range deps {
			if !dep.Ready {
				health["status"] = "degraded"
				break
			}
		}
	}
	writeJSON(w, health, s.logger)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	// OpenAI-compatible models list
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":       "thane",
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "thane",
			},
		},
	}, s.logger)
}

// ChatCompletionRequest is the OpenAI-compatible request format.
type ChatCompletionRequest struct {
	Model    string                         `json:"model"`
	Messages []chatCompletionRequestMessage `json:"messages"`
	Stream   bool                           `json:"stream,omitempty"`
}

// ChatCompletionResponse is the OpenAI-compatible response format.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a completion choice.
type Choice struct {
	Index        int           `json:"index"`
	Message      agent.Message `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// Usage represents token usage.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// runChatLoop routes a native API request through the loop runtime's
// request/reply path. The API layer still owns OpenAI wire formatting,
// while the loop owns request preparation, telemetry, accounting, and
// runner execution.
func (s *Server) runChatLoop(ctx context.Context, req *agent.Request, streamCallback agent.StreamCallback, loopName string) (*agent.Response, error) {
	if s.launchChatLoop == nil {
		return nil, fmt.Errorf("api chat loop launcher is not configured")
	}

	loopReq := loopRequestFromAgent(req)
	if loopReq.RoutingFactors == nil {
		loopReq.RoutingFactors = make(map[string]string, 2)
	}
	if _, ok := loopReq.RoutingFactors["source"]; !ok {
		loopReq.RoutingFactors["source"] = "api"
	}
	if _, ok := loopReq.RoutingFactors["channel"]; !ok {
		loopReq.RoutingFactors["channel"] = "api"
	}
	stream := loopStreamFromAgent(streamCallback)

	result, err := s.launchChatLoop(ctx, looppkg.Launch{
		Spec: looppkg.Spec{
			Name:       loopName,
			Operation:  looppkg.OperationRequestReply,
			Completion: looppkg.CompletionReturn,
			Tags:       []string{"api"},
			TurnBuilder: func(context.Context, looppkg.TurnInput) (*looppkg.AgentTurn, error) {
				return &looppkg.AgentTurn{
					Request:    cloneLoopRequest(loopReq),
					RunContext: ctx,
					Stream:     stream,
				}, nil
			},
			Metadata: map[string]string{
				"subsystem": "api",
				"category":  "chat",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	if result.Response == nil {
		return nil, fmt.Errorf("api chat loop returned no response")
	}
	return agentResponseFromLoop(result.Response), nil
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	log := s.logger.With("subsystem", logging.SubsystemAPI)
	ctx := logging.WithLogger(r.Context(), log)

	messages, err := req.AgentMessages()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	hints := map[string]string{
		"channel": "api", // Native OpenAI-compatible API
	}
	model, hints, delegationGating, systemPrompt := normalizeModelSelection(req.Model, hints, premiumQualityFloor(s.router), log)

	agentReq := &agent.Request{
		Messages:         messages,
		Model:            model,
		RoutingFactors:   hints,
		DelegationGating: delegationGating,
		SystemPrompt:     systemPrompt,
	}

	if req.Stream {
		s.handleStreamingCompletion(w, r.WithContext(ctx), agentReq)
		return
	}

	// Non-streaming: run and return complete response
	resp, err := s.runChatLoop(ctx, agentReq, nil, "api/chat-completions")
	if err != nil {
		log.Error("agent loop failed", "error", err)
		code, message := agentErrorDetails(err)
		s.errorResponse(w, code, message)
		return
	}

	// Record usage stats
	s.recordUsage(resp.Model, resp.InputTokens, resp.OutputTokens, resp.CacheCreationInputTokens, resp.CacheReadInputTokens)

	// Format as OpenAI response
	completion := ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []Choice{
			{
				Index: 0,
				Message: agent.Message{
					Role:    "assistant",
					Content: resp.Content,
				},
				FinishReason: resp.FinishReason,
			},
		},
		Usage: Usage{
			PromptTokens:     0, // TODO: actual counting
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, completion, s.logger)
}

// SimpleChatRequest is a minimal chat request for easy testing.
type SimpleChatRequest struct {
	Message        string `json:"message"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// SimpleChatResponse is a minimal chat response.
type SimpleChatResponse struct {
	Response       string   `json:"response"`
	Model          string   `json:"model"`
	ConversationID string   `json:"conversation_id"`
	ToolCalls      []string `json:"tool_calls,omitempty"` // Tool names used
}

// handleSimpleChat provides a simplified chat interface for testing.
// POST /v1/chat {"message": "turn on the lights"}
func (s *Server) handleSimpleChat(w http.ResponseWriter, r *http.Request) {
	var req SimpleChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Message == "" {
		s.errorResponse(w, http.StatusBadRequest, "message is required")
		return
	}

	convID := req.ConversationID
	if convID == "" {
		convID = uuid.New().String()
	}

	log := s.logger.With("subsystem", logging.SubsystemAPI)
	ctx := logging.WithLogger(r.Context(), log)

	agentReq := &agent.Request{
		Messages: []agent.Message{
			{Role: "user", Content: req.Message},
		},
		ConversationID: convID,
		RoutingFactors: map[string]string{
			"channel": "api",
		},
	}

	resp, err := s.runChatLoop(ctx, agentReq, nil, "api/simple-chat")
	if err != nil {
		log.Error("agent loop failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "agent error: "+err.Error())
		return
	}

	s.recordUsage(resp.Model, resp.InputTokens, resp.OutputTokens, resp.CacheCreationInputTokens, resp.CacheReadInputTokens)

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, SimpleChatResponse{
		Response:       resp.Content,
		Model:          resp.Model,
		ConversationID: convID,
	}, s.logger)
}

// StreamChunk is the SSE format for streaming responses.
type StreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
}

// StreamChoice represents a streaming choice with delta content.
type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

// StreamDelta represents incremental content.
type StreamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

func (s *Server) handleStreamingCompletion(w http.ResponseWriter, r *http.Request, agentReq *agent.Request) {
	// Set SSE headers. Omit "Connection: keep-alive" — it's a hop-by-hop
	// header forbidden in HTTP/2 (RFC 9113 §8.2.2).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	completionID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	modelName := "thane" // Will be updated when we get the response
	var writeMu sync.Mutex

	// Send initial chunk with role
	initialChunk := StreamChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelName,
		Choices: []StreamChoice{{
			Index: 0,
			Delta: StreamDelta{Role: "assistant"},
		}},
	}
	writeMu.Lock()
	s.writeSSE(w, initialChunk)
	flusher.Flush()
	writeMu.Unlock()

	// Track if any tokens were streamed (greeting fast-path skips streaming)
	streamed := false

	// Get response controller for deadline management (Go 1.20+)
	rc := http.NewResponseController(w)

	// Stream callback sends tokens and keepalives during tool execution
	streamCallback := func(event agent.StreamEvent) {
		switch event.Kind {
		case agent.KindToken:
			streamed = true
			chunk := StreamChunk{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   modelName,
				Choices: []StreamChoice{{
					Index: 0,
					Delta: StreamDelta{Content: event.Token},
				}},
			}
			writeMu.Lock()
			s.writeSSE(w, chunk)
			flusher.Flush()
			writeMu.Unlock()

		case agent.KindToolCallStart, agent.KindToolCallDone:
			// Send SSE comment as keepalive to prevent write timeout
			writeMu.Lock()
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
			writeMu.Unlock()
		}

		// Reset write deadline after every event to prevent timeout
		// during multi-iteration tool loops
		writeMu.Lock()
		if err := rc.SetWriteDeadline(time.Now().Add(120 * time.Second)); err != nil {
			s.logger.Debug("failed to reset write deadline", "error", err)
		}
		writeMu.Unlock()
	}

	// Run agent with streaming — context carries the subsystem logger
	// injected by the calling OpenAI-compatible handler.
	resp, err := s.runChatLoop(r.Context(), agentReq, streamCallback, "api/chat-completions")
	if err != nil {
		s.logger.Error("agent loop failed", "error", err)
		// Can't change status code after streaming started, just close
		return
	}

	// If content was not streamed (e.g. greeting fast-path), emit it now
	if !streamed && resp.Content != "" {
		streamCallback(agent.StreamEvent{Kind: agent.KindToken, Token: resp.Content})
	}

	// Record usage stats
	s.recordUsage(resp.Model, resp.InputTokens, resp.OutputTokens, resp.CacheCreationInputTokens, resp.CacheReadInputTokens)

	// Update model name and send final chunk
	modelName = resp.Model
	finishReason := resp.FinishReason
	finalChunk := StreamChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelName,
		Choices: []StreamChoice{{
			Index:        0,
			Delta:        StreamDelta{},
			FinishReason: &finishReason,
		}},
	}
	writeMu.Lock()
	s.writeSSE(w, finalChunk)
	flusher.Flush()

	// Send [DONE] marker
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
	writeMu.Unlock()
}

func (s *Server) writeSSE(w http.ResponseWriter, chunk StreamChunk) {
	data, err := json.Marshal(chunk)
	if err != nil {
		s.logger.Debug("failed to marshal SSE chunk", "error", err)
		return
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		s.logger.Debug("failed to write SSE chunk", "error", err)
	}
}

func (s *Server) errorResponse(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	writeJSON(w, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	}, s.logger)
}

// Router introspection handlers

type routerStatsResponse struct {
	router.Stats
	AnthropicRateLimit *fleet.AnthropicRateLimitSnapshot `json:"anthropic_rate_limit,omitempty"`
}

type setModelRegistryPolicyRequest struct {
	Deployment string `json:"deployment"`
	State      string `json:"state"`
	Routable   *bool  `json:"routable,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type setModelRegistryResourcePolicyRequest struct {
	Resource string `json:"resource"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
}

type modelRegistryPolicyResponse struct {
	Status     string                           `json:"status"`
	Generation int64                            `json:"generation"`
	Deployment fleet.RegistryDeploymentSnapshot `json:"deployment"`
}

type modelRegistryResourcePolicyResponse struct {
	Status     string                         `json:"status"`
	Generation int64                          `json:"generation"`
	Resource   fleet.RegistryResourceSnapshot `json:"resource"`
}

func (s *Server) handleModelRegistry(w http.ResponseWriter, r *http.Request) {
	if s.modelRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	snapshot := s.modelRegistry.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, snapshot, s.logger)
}

// handleModelFleet returns the native fleet view: the deployable models with
// their resource, provider, capabilities, and routability, as a bare array.
// The OpenAI-shaped list lives on the separate OpenAI-compatible server.
// [GET /v1/models]
func (s *Server) handleModelFleet(w http.ResponseWriter, r *http.Request) {
	if s.modelRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}
	deployments := s.modelRegistry.Snapshot().Deployments
	if deployments == nil {
		deployments = []fleet.RegistryDeploymentSnapshot{}
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, deployments, s.logger)
}

func (s *Server) handleModelRegistryPolicySet(w http.ResponseWriter, r *http.Request) {
	if s.modelRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	var req setModelRegistryPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Deployment = strings.TrimSpace(req.Deployment)
	if req.Deployment == "" {
		s.errorResponse(w, http.StatusBadRequest, "deployment is required")
		return
	}

	if strings.TrimSpace(req.State) == "" && req.Routable == nil {
		s.errorResponse(w, http.StatusBadRequest, "state or routable is required")
		return
	}

	current := findRegistryDeployment(s.modelRegistry.Snapshot(), req.Deployment)
	if !current.found {
		s.errorResponse(w, http.StatusNotFound, (&fleet.UnknownDeploymentError{Deployment: req.Deployment}).Error())
		return
	}

	state := current.snapshot.PolicyState
	if raw := strings.TrimSpace(req.State); raw != "" {
		parsed, err := fleet.ParseDeploymentPolicyState(raw)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		state = parsed
	}

	policy := fleet.DeploymentPolicy{
		State:     state,
		Routable:  req.Routable,
		Reason:    req.Reason,
		UpdatedAt: time.Now(),
	}
	if s.persistModelRegistryPolicy != nil {
		if err := s.persistModelRegistryPolicy(req.Deployment, policy); err != nil {
			s.logger.Error("persist model registry policy failed", "deployment", req.Deployment, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to persist model registry policy")
			return
		}
	}

	if err := s.modelRegistry.ApplyDeploymentPolicy(req.Deployment, policy, policy.UpdatedAt); err != nil {
		if fleet.IsUnknownDeployment(err) {
			s.errorResponse(w, http.StatusNotFound, err.Error())
			return
		}
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.router != nil {
		s.router.UpdateConfig(s.modelRegistry.Catalog().RouterConfig(0))
	}

	snapshot := s.modelRegistry.Snapshot()
	deployment := findRegistryDeployment(snapshot, req.Deployment)
	if !deployment.found {
		s.errorResponse(w, http.StatusInternalServerError, "deployment policy applied but deployment snapshot is unavailable")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, modelRegistryPolicyResponse{
		Status:     "ok",
		Generation: snapshot.Generation,
		Deployment: deployment.snapshot,
	}, s.logger)
}

func (s *Server) handleModelRegistryPolicyDelete(w http.ResponseWriter, r *http.Request) {
	if s.modelRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	id := strings.TrimSpace(r.URL.Query().Get("deployment"))
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "deployment is required")
		return
	}

	if s.deleteModelRegistryPolicy != nil {
		if err := s.deleteModelRegistryPolicy(id); err != nil {
			s.logger.Error("delete persisted model registry policy failed", "deployment", id, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to delete persisted model registry policy")
			return
		}
	}

	if err := s.modelRegistry.ClearDeploymentPolicy(id, time.Now()); err != nil {
		if fleet.IsUnknownDeployment(err) {
			s.errorResponse(w, http.StatusNotFound, err.Error())
			return
		}
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.router != nil {
		s.router.UpdateConfig(s.modelRegistry.Catalog().RouterConfig(0))
	}

	snapshot := s.modelRegistry.Snapshot()
	deployment := findRegistryDeployment(snapshot, id)
	if !deployment.found {
		s.errorResponse(w, http.StatusInternalServerError, "deployment policy cleared but deployment snapshot is unavailable")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, modelRegistryPolicyResponse{
		Status:     "ok",
		Generation: snapshot.Generation,
		Deployment: deployment.snapshot,
	}, s.logger)
}

func (s *Server) handleModelRegistryResourcePolicySet(w http.ResponseWriter, r *http.Request) {
	if s.modelRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	var req setModelRegistryResourcePolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Resource = strings.TrimSpace(req.Resource)
	if req.Resource == "" {
		s.errorResponse(w, http.StatusBadRequest, "resource is required")
		return
	}

	parsed, err := fleet.ParseDeploymentPolicyState(req.State)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	policy := fleet.ResourcePolicy{
		State:     parsed,
		Reason:    req.Reason,
		UpdatedAt: time.Now(),
	}
	if s.persistModelRegistryResourcePolicy != nil {
		if err := s.persistModelRegistryResourcePolicy(req.Resource, policy); err != nil {
			s.logger.Error("persist model registry resource policy failed", "resource", req.Resource, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to persist model registry resource policy")
			return
		}
	}

	if err := s.modelRegistry.ApplyResourcePolicy(req.Resource, policy, policy.UpdatedAt); err != nil {
		if fleet.IsUnknownResource(err) {
			s.errorResponse(w, http.StatusNotFound, err.Error())
			return
		}
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.router != nil {
		s.router.UpdateConfig(s.modelRegistry.Catalog().RouterConfig(0))
	}

	snapshot := s.modelRegistry.Snapshot()
	resource := findRegistryResource(snapshot, req.Resource)
	if !resource.found {
		s.errorResponse(w, http.StatusInternalServerError, "resource policy applied but resource snapshot is unavailable")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, modelRegistryResourcePolicyResponse{
		Status:     "ok",
		Generation: snapshot.Generation,
		Resource:   resource.snapshot,
	}, s.logger)
}

func (s *Server) handleModelRegistryResourcePolicyDelete(w http.ResponseWriter, r *http.Request) {
	if s.modelRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	id := strings.TrimSpace(r.URL.Query().Get("resource"))
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "resource is required")
		return
	}

	if s.deleteModelRegistryResourcePolicy != nil {
		if err := s.deleteModelRegistryResourcePolicy(id); err != nil {
			s.logger.Error("delete persisted model registry resource policy failed", "resource", id, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to delete persisted model registry resource policy")
			return
		}
	}

	if err := s.modelRegistry.ClearResourcePolicy(id, time.Now()); err != nil {
		if fleet.IsUnknownResource(err) {
			s.errorResponse(w, http.StatusNotFound, err.Error())
			return
		}
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.router != nil {
		s.router.UpdateConfig(s.modelRegistry.Catalog().RouterConfig(0))
	}

	snapshot := s.modelRegistry.Snapshot()
	resource := findRegistryResource(snapshot, id)
	if !resource.found {
		s.errorResponse(w, http.StatusInternalServerError, "resource policy cleared but resource snapshot is unavailable")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, modelRegistryResourcePolicyResponse{
		Status:     "ok",
		Generation: snapshot.Generation,
		Resource:   resource.snapshot,
	}, s.logger)
}

type registryDeploymentLookup struct {
	snapshot fleet.RegistryDeploymentSnapshot
	found    bool
}

type registryResourceLookup struct {
	snapshot fleet.RegistryResourceSnapshot
	found    bool
}

func findRegistryDeployment(snapshot *fleet.RegistrySnapshot, id string) registryDeploymentLookup {
	if snapshot == nil {
		return registryDeploymentLookup{}
	}
	for _, dep := range snapshot.Deployments {
		if dep.ID == id {
			return registryDeploymentLookup{snapshot: dep, found: true}
		}
	}
	return registryDeploymentLookup{}
}

func findRegistryResource(snapshot *fleet.RegistrySnapshot, id string) registryResourceLookup {
	if snapshot == nil {
		return registryResourceLookup{}
	}
	for _, res := range snapshot.Resources {
		if res.ID == id {
			return registryResourceLookup{snapshot: res, found: true}
		}
	}
	return registryResourceLookup{}
}

// Checkpoint handlers

type checkpointCreateRequest struct {
	Trigger string `json:"trigger,omitempty"` // defaults to "manual"
	Note    string `json:"note,omitempty"`
}

func (s *Server) handleCheckpointCreate(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	var req checkpointCreateRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	trigger := checkpoint.TriggerManual
	if req.Trigger != "" {
		trigger = checkpoint.Trigger(req.Trigger)
	}

	cp, err := s.checkpointer.Create(trigger, req.Note)
	if err != nil {
		s.logger.Error("checkpoint create failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "failed to create checkpoint")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, cp, s.logger)
}

func (s *Server) handleCheckpointList(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	checkpoints, err := s.checkpointer.List(limit)
	if err != nil {
		s.logger.Error("checkpoint list failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "failed to list checkpoints")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"count":       len(checkpoints),
		"checkpoints": checkpoints,
	}, s.logger)
}

func (s *Server) handleCheckpointGet(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid checkpoint id")
		return
	}

	cp, err := s.checkpointer.Get(id)
	if err != nil {
		s.logger.Error("checkpoint get failed", "error", err, "id", idStr)
		s.errorResponse(w, http.StatusNotFound, "checkpoint not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, cp, s.logger)
}

func (s *Server) handleCheckpointDelete(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid checkpoint id")
		return
	}

	if err := s.checkpointer.Delete(id); err != nil {
		s.logger.Error("checkpoint delete failed", "error", err, "id", idStr)
		s.errorResponse(w, http.StatusNotFound, "checkpoint not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCheckpointRestore(w http.ResponseWriter, r *http.Request) {
	if s.checkpointer == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "checkpointing not configured")
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid checkpoint id")
		return
	}

	if err := s.checkpointer.Restore(id); err != nil {
		s.logger.Error("checkpoint restore failed", "error", err, "id", idStr)
		s.errorResponse(w, http.StatusInternalServerError, "failed to restore checkpoint")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"status":  "restored",
		"id":      idStr,
		"message": "checkpoint restored successfully",
	}, s.logger)
}

// History endpoints
//
// handleConversationList lives in conversations.go (the queryable
// filter/sort/keyset surface); handleConversationGet stays here.

func (s *Server) handleConversationGet(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "memory store not configured")
		return
	}

	id := r.PathValue("id")
	conv := s.memoryStore.GetConversation(id)
	if conv == nil {
		s.errorResponse(w, http.StatusNotFound, "conversation not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, conv, s.logger)
}

func (s *Server) handleSessionStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, s.DashboardSnapshot(), s.logger)
}

func (s *Server) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	if s.usageStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "usage store not configured")
		return
	}

	hours := 24
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			s.errorResponse(w, http.StatusBadRequest, "hours must be a positive integer")
			return
		}
		hours = parsed
	}

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-time.Duration(hours) * time.Hour)
	queryEnd := end.Add(1 * time.Second)

	summary, err := s.usageStore.Summary(start, queryEnd)
	if err != nil {
		s.logger.Error("usage summary query failed", "error", err, "hours", hours)
		s.errorResponse(w, http.StatusInternalServerError, "usage summary query failed")
		return
	}

	resp := usageSummaryResponse{
		Start:   start.UTC().Format(time.RFC3339),
		End:     end.UTC().Format(time.RFC3339),
		Hours:   hours,
		Summary: summary,
	}

	if groupBy := strings.TrimSpace(r.URL.Query().Get("group_by")); groupBy != "" {
		grouped, err := s.usageStore.SummaryByGroup(groupBy, start, queryEnd)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		resp.GroupBy = groupBy
		resp.Groups = grouped
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, resp, s.logger)
}

func (s *Server) handleSetBalance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Balance float64 `json:"balance_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	s.stats.SetBalance(req.Balance)
	s.logger.Info("balance updated", "balance_usd", req.Balance)
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"status": "ok", "balance_usd": req.Balance}, s.logger)
}

func (s *Server) handleSessionReset(w http.ResponseWriter, r *http.Request) {
	if err := s.loop.ResetConversation("default"); err != nil {
		s.logger.Error("session reset failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "reset failed")
		return
	}
	s.logger.Info("session reset via API")
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"status": "ok", "message": "conversation cleared"}, s.logger)
}

func (s *Server) handleSessionCompact(w http.ResponseWriter, r *http.Request) {
	if err := s.loop.TriggerCompaction(r.Context(), "default"); err != nil {
		s.logger.Error("compaction failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "compaction failed: "+err.Error())
		return
	}
	s.logger.Info("compaction triggered via API")
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"status": "ok", "message": "conversation compacted"}, s.logger)
}

func (s *Server) handleSessionHistory(w http.ResponseWriter, r *http.Request) {
	messages := s.loop.GetHistory("default")

	// Filter to user/assistant messages only (skip system/tool)
	type historyMessage struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		Timestamp string `json:"timestamp"`
	}

	var filtered []historyMessage
	for _, m := range messages {
		if m.Role == "user" || m.Role == "assistant" {
			filtered = append(filtered, historyMessage{
				Role:      m.Role,
				Content:   m.Content,
				Timestamp: m.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{"messages": filtered}, s.logger)
}

// --- Archive endpoints ---

func (s *Server) handleArchiveSessions(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	convID := r.URL.Query().Get("conversation_id")
	limit := parseIntParam(r, "limit", 50)

	sessions, err := s.archiveStore.ListSessions(convID, limit)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "list sessions: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"sessions": sessions,
		"count":    len(sessions),
	}, s.logger)
}

func (s *Server) handleArchiveSessionGet(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	id := r.PathValue("id")

	sess, err := s.archiveStore.GetSession(id)
	if err != nil || sess == nil {
		s.errorResponse(w, http.StatusNotFound, "session not found")
		return
	}

	transcript, err := s.archiveStore.GetSessionTranscript(id)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "get transcript: "+err.Error())
		return
	}

	toolCalls, err := s.archiveStore.GetSessionToolCalls(id)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "get tool calls: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"session":    sess,
		"transcript": transcript,
		"tool_calls": toolCalls,
	}, s.logger)
}

func (s *Server) handleArchiveSessionExport(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	id := r.PathValue("id")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "markdown"
	}

	switch format {
	case "markdown", "md":
		md, err := s.archiveStore.ExportSessionMarkdown(id)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "export: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"session-%s.md\"", memory.ShortID(id)))
		fmt.Fprint(w, md)

	case "json":
		sess, err := s.archiveStore.GetSession(id)
		if err != nil || sess == nil {
			s.errorResponse(w, http.StatusNotFound, "session not found")
			return
		}
		transcript, err := s.archiveStore.GetSessionTranscript(id)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "get transcript: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"session-%s.json\"", memory.ShortID(id)))
		writeJSON(w, map[string]any{
			"session":    sess,
			"transcript": transcript,
		}, s.logger)

	default:
		s.errorResponse(w, http.StatusBadRequest, "unsupported format: "+format+" (use markdown or json)")
	}
}

func (s *Server) handleArchiveSearch(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		s.errorResponse(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	opts := memory.SearchOptions{
		Query:          query,
		ConversationID: r.URL.Query().Get("conversation_id"),
		Limit:          parseIntParam(r, "limit", 10),
	}

	// Parse silence threshold
	if silenceStr := r.URL.Query().Get("silence"); silenceStr != "" {
		if d, err := time.ParseDuration(silenceStr); err == nil {
			opts.SilenceThreshold = d
		}
	}

	// Parse context=0 to disable context expansion
	if r.URL.Query().Get("context") == "0" {
		opts.NoContext = true
	}

	results, err := s.archiveStore.Search(opts)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "search: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"results": results,
		"count":   len(results),
		"query":   query,
	}, s.logger)
}

func (s *Server) handleArchiveMessages(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	if fromStr == "" || toStr == "" {
		s.errorResponse(w, http.StatusBadRequest, "from and to parameters are required (RFC3339)")
		return
	}

	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid from time: "+err.Error())
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid to time: "+err.Error())
		return
	}

	convID := r.URL.Query().Get("conversation_id")
	limit := parseIntParam(r, "limit", 500)

	messages, err := s.archiveStore.GetMessagesByTimeRange(from, to, convID, limit)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "query: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"messages": messages,
		"count":    len(messages),
		"from":     fromStr,
		"to":       toStr,
	}, s.logger)
}

func (s *Server) handleArchiveStats(w http.ResponseWriter, r *http.Request) {
	if s.archiveStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "archive not configured")
		return
	}

	stats, err := s.archiveStore.Stats()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "stats: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, stats, s.logger)
}

func parseIntParam(r *http.Request, name string, defaultVal int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return defaultVal
	}
	return n
}

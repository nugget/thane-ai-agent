// Package router handles intelligent model selection.
package router

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Request contains the information needed for routing decisions.
type Request struct {
	Query          string            // The user's input
	ContextSize    int               // Estimated tokens of context (talents, history)
	NeedsTools     bool              // Whether tool calling is required
	NeedsStreaming bool              // Whether a streaming response is required
	NeedsImages    bool              // Whether image/multimodal input is required
	ToolCount      int               // Number of tools available
	Priority       Priority          // Latency requirements
	Hints          map[string]string // Caller-supplied routing hints (see HintXxx constants)
}

// Hint keys for routing decisions. Callers set these to influence model selection.
const (
	// HintChannel identifies the request source: "ollama", "homeassistant", "voice", "api"
	HintChannel = "channel"
	// HintQualityFloor is the minimum quality rating (1-10) the caller requires.
	HintQualityFloor = "quality_floor"
	// HintModelPreference suggests a specific model (soft preference, not override).
	HintModelPreference = "model_preference"
	// HintMission describes the task context: "conversation", "device_control", "background", "automation", "metacognitive"
	HintMission = "mission"
	// HintLocalOnly restricts routing to free/local models when set to "true".
	HintLocalOnly = "local_only"
	// HintDelegationGating controls whether delegation-first tool gating is
	// active. Set to "disabled" to give the model direct access to all tools
	// on every iteration (used by thane:ops).
	HintDelegationGating = "delegation_gating"
	// HintPreferSpeed indicates the caller benefits from faster response
	// times over higher quality. When "true", any model with Speed >= 7
	// receives a scoring bonus regardless of cost tier or provider. Can be
	// decisive among similarly priced options. Use for background/delegation
	// tasks where latency and resource efficiency matter more than maximum
	// output quality.
	HintPreferSpeed = "prefer_speed"
)

// Priority indicates latency requirements.
type Priority int

const (
	PriorityInteractive Priority = iota // User waiting, needs fast response
	PriorityBackground                  // Can take longer for better quality
)

// Decision records why a model was selected.
type Decision struct {
	RequestID string    `json:"request_id"`
	Timestamp time.Time `json:"timestamp"`

	// Input analysis
	QueryLength    int        `json:"query_length"`
	ContextSize    int        `json:"context_size"`
	NeedsTools     bool       `json:"needs_tools"`
	NeedsStreaming bool       `json:"needs_streaming,omitempty"`
	NeedsImages    bool       `json:"needs_images,omitempty"`
	Priority       string     `json:"priority"`
	DetectedIntent string     `json:"detected_intent,omitempty"`
	Complexity     Complexity `json:"complexity"`

	// Decision process
	RulesEvaluated []string            `json:"rules_evaluated"`
	RulesMatched   []string            `json:"rules_matched"`
	RejectedModels map[string][]string `json:"rejected_models,omitempty"`
	Scores         map[string]int      `json:"scores,omitempty"`
	NoEligible     bool                `json:"no_eligible,omitempty"`

	// Outcome
	ModelSelected         string `json:"model_selected"`
	UpstreamModelSelected string `json:"upstream_model_selected,omitempty"`
	ProviderSelected      string `json:"provider_selected,omitempty"`
	ResourceSelected      string `json:"resource_selected,omitempty"`
	Reasoning             string `json:"reasoning"`

	// Post-execution (filled in later)
	LatencyMs  int64 `json:"latency_ms,omitempty"`
	TokensUsed int   `json:"tokens_used,omitempty"`
	Success    *bool `json:"success,omitempty"`
}

// Complexity categorizes query difficulty.
type Complexity int

const (
	ComplexitySimple   Complexity = iota // Direct command, single action
	ComplexityModerate                   // Multi-step or needs context
	ComplexityComplex                    // Reasoning, analysis, explanation
)

// String returns the human-readable name of a complexity level.
func (c Complexity) String() string {
	switch c {
	case ComplexitySimple:
		return "simple"
	case ComplexityModerate:
		return "moderate"
	case ComplexityComplex:
		return "complex"
	default:
		return "unknown"
	}
}

// Model represents an available model with its capabilities.
type Model struct {
	Name                  string     // Route/deployment identifier (e.g., "qwen3:4b" or "spark/qwen3:32b")
	UpstreamModel         string     // Provider-native model name (e.g., "qwen3:32b")
	Provider              string     // "ollama" or "anthropic" etc
	ResourceID            string     // Provider resource identity (e.g., server name)
	Server                string     // Configured server name when applicable
	SupportsTools         bool       // Deployment is configured for tool calling
	ProviderSupportsTools bool       // Underlying provider supports tool calling
	SupportsStreaming     bool       // Deployment/provider can stream
	SupportsImages        bool       // Deployment/provider accepts image input
	ContextWindow         int        // Max tokens
	Speed                 int        // Relative speed (1-10, 10=fastest)
	Quality               int        // Relative quality (1-10, 10=best)
	CostTier              int        // 0=free/local, 1=cheap, 2=moderate, 3=expensive
	MinComplexity         Complexity // Don't use for simpler than this
}

// Config holds router configuration.
type Config struct {
	Models       []Model // Available models
	DefaultModel string  // Fallback if no rules match
	LocalFirst   bool    // Prefer local models when possible
	MaxAuditLog  int     // How many decisions to keep in memory
}

// Router selects models based on request characteristics.
type Router struct {
	logger *slog.Logger
	config Config

	mu                    sync.RWMutex
	auditLog              []Decision
	stats                 Stats
	resourceCooldownUntil map[string]time.Time
}

func cloneModels(in []Model) []Model {
	if len(in) == 0 {
		return nil
	}
	out := make([]Model, len(in))
	copy(out, in)
	return out
}

func (r *Router) configSnapshot() Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg := r.config
	cfg.Models = cloneModels(r.config.Models)
	return cfg
}

// Stats tracks routing statistics.
type Stats struct {
	TotalRequests    int64                      `json:"total_requests"`
	ModelCounts      map[string]int64           `json:"model_counts"`
	AvgLatencyMs     map[string]int64           `json:"avg_latency_ms"`
	ComplexityCounts map[string]int64           `json:"complexity_counts"`
	SuccessCount     int64                      `json:"success_count"`
	FailureCount     int64                      `json:"failure_count"`
	ProviderCounts   map[string]int64           `json:"provider_counts,omitempty"`
	ResourceCounts   map[string]int64           `json:"resource_counts,omitempty"`
	ResourceHealth   map[string]ResourceHealth  `json:"resource_health,omitempty"`
	DeploymentStats  map[string]DeploymentStats `json:"deployment_stats,omitempty"`
}

// DeploymentStats tracks routing and outcome state for one concrete
// deployment/route target.
type DeploymentStats struct {
	Provider      string `json:"provider"`
	Resource      string `json:"resource,omitempty"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	Requests      int64  `json:"requests"`
	Successes     int64  `json:"successes"`
	Failures      int64  `json:"failures"`
	AvgLatencyMs  int64  `json:"avg_latency_ms,omitempty"`
	AvgTokensUsed int64  `json:"avg_tokens_used,omitempty"`
}

// ResourceHealth exposes request-plane routing health for one resource.
type ResourceHealth struct {
	CooldownUntil  time.Time `json:"cooldown_until,omitempty"`
	CooldownReason string    `json:"cooldown_reason,omitempty"`
}

// NewRouter creates a router with the given configuration.
func NewRouter(logger *slog.Logger, config Config) *Router {
	if config.MaxAuditLog <= 0 {
		config.MaxAuditLog = 1000
	}
	return &Router{
		logger:   logger,
		config:   config,
		auditLog: make([]Decision, 0, config.MaxAuditLog),
		stats: Stats{
			ModelCounts:      make(map[string]int64),
			AvgLatencyMs:     make(map[string]int64),
			ComplexityCounts: make(map[string]int64),
			ProviderCounts:   make(map[string]int64),
			ResourceCounts:   make(map[string]int64),
			DeploymentStats:  make(map[string]DeploymentStats),
		},
		resourceCooldownUntil: make(map[string]time.Time),
	}
}

const resourceTimeoutCooldown = 2 * time.Minute

// ContextWindowForModel returns the context window size for the named
// model. If the model is not found in the router's configuration, it
// returns 0.
func (r *Router) ContextWindowForModel(name string) int {
	cfg := r.configSnapshot()
	maxByUpstream := 0
	for _, m := range cfg.Models {
		if m.Name == name {
			return m.ContextWindow
		}
		if m.UpstreamModel == name && m.ContextWindow > maxByUpstream {
			maxByUpstream = m.ContextWindow
		}
	}
	if maxByUpstream > 0 {
		return maxByUpstream
	}
	return 0
}

// MaxQuality returns the highest quality rating among configured models.
// If no models are configured it returns 10 as a safe default that
// selects the best available model at runtime.
func (r *Router) MaxQuality() int {
	cfg := r.configSnapshot()
	max := 0
	for _, m := range cfg.Models {
		if m.Quality > max {
			max = m.Quality
		}
	}
	if max == 0 {
		return 10
	}
	return max
}

// Route selects a model for the given request.
func (r *Router) Route(ctx context.Context, req Request) (string, *Decision) {
	cfg := r.configSnapshot()
	decision := &Decision{
		RequestID:      generateRequestID(),
		Timestamp:      time.Now(),
		QueryLength:    len(req.Query),
		ContextSize:    req.ContextSize,
		NeedsTools:     req.NeedsTools,
		NeedsStreaming: req.NeedsStreaming,
		NeedsImages:    req.NeedsImages,
		Priority:       priorityString(req.Priority),
	}

	// Analyze complexity
	decision.Complexity = r.analyzeComplexity(req.Query)
	decision.DetectedIntent = r.detectIntent(req.Query)

	// Evaluate rules and select model
	model := r.selectModel(cfg, req, decision)
	decision.ModelSelected = model
	r.populateSelectionMetadata(cfg, decision, model)

	// Log the decision
	r.recordDecision(*decision)

	r.logger.Info("model routed",
		"request_id", decision.RequestID,
		"model", model,
		"complexity", decision.Complexity.String(),
		"reasoning", decision.Reasoning,
	)

	return model, decision
}

// analyzeComplexity estimates query difficulty.
//
// Retrieval verbs at the start of the query (search, read, list, etc.)
// are checked first because they represent concrete, actionable tasks
// that should use fast/cheap models even when the query text contains
// words like "history" that would otherwise trigger complex classification.
func (r *Router) analyzeComplexity(query string) Complexity {
	q := strings.ToLower(query)

	// Retrieval/action verbs at the start of the query indicate concrete
	// tasks that don't require deep reasoning. Checked first to prevent
	// false-positive complex classification when the object of the
	// retrieval contains complex-sounding words (e.g., "search archives
	// for distributed.net history" is retrieval, not analysis).
	retrievalPrefixes := []string{
		"search ", "read ", "list ", "fetch ", "find ", "check ",
	}
	for _, p := range retrievalPrefixes {
		if strings.HasPrefix(q, p) {
			return ComplexitySimple
		}
	}

	// Complex indicators — reasoning, analysis, explanation tasks.
	// Checked before simple commands because "why did the lights turn on"
	// is a reasoning question, not a device command.
	complexWords := []string{"explain", "why", "analyze", "compare", "history", "pattern", "trend", "recommend"}
	for _, w := range complexWords {
		if strings.Contains(q, w) {
			return ComplexityComplex
		}
	}

	// Simple indicators (direct commands). Checked after complex so that
	// "why did X turn on" is classified as complex, not simple. Trailing
	// space on "open" and "close" avoids matching questions like "is the
	// door open".
	simplePatterns := []string{"turn on", "turn off", "set ", "lock", "unlock", "open ", "close "}
	for _, p := range simplePatterns {
		if strings.Contains(q, p) {
			return ComplexitySimple
		}
	}

	// Questions about state are moderate
	if strings.Contains(q, "?") || strings.HasPrefix(q, "is ") || strings.HasPrefix(q, "what") {
		return ComplexityModerate
	}

	// Default to moderate
	return ComplexityModerate
}

// detectIntent identifies the likely action type.
func (r *Router) detectIntent(query string) string {
	q := strings.ToLower(query)

	switch {
	case strings.Contains(q, "turn on") || strings.Contains(q, "turn off"):
		return "device_control"
	case strings.Contains(q, "lock") || strings.Contains(q, "unlock"):
		return "security"
	case strings.Contains(q, "temperature") || strings.Contains(q, "thermostat"):
		return "climate"
	case strings.Contains(q, "who") || strings.Contains(q, "where") || strings.Contains(q, "home"):
		return "presence"
	case strings.Contains(q, "when") || strings.Contains(q, "time") || strings.Contains(q, "last"):
		return "temporal"
	default:
		return "general"
	}
}

// selectModel picks the best model based on analysis.
func (r *Router) selectModel(cfg Config, req Request, decision *Decision) string {
	var rulesEvaluated, rulesMatched []string
	var reasoning strings.Builder
	rejected := make(map[string][]string)
	now := time.Now()

	// Find eligible models
	var candidates []Model
	for _, m := range cfg.Models {
		rulesEvaluated = append(rulesEvaluated, "check_"+m.Name)

		var reasons []string

		// Must support tools if needed
		if req.NeedsTools && !m.SupportsTools {
			if m.ProviderSupportsTools {
				reasons = append(reasons, "tool use disabled for this deployment")
			} else {
				reasons = append(reasons, "missing tool support")
			}
		}

		// Must support streaming when required by the caller.
		if req.NeedsStreaming && !m.SupportsStreaming {
			reasons = append(reasons, "missing streaming support")
		}

		// Must support image input when required by the caller.
		if req.NeedsImages && !m.SupportsImages {
			reasons = append(reasons, "missing image support")
		}

		// Must fit context
		if req.ContextSize > 0 && m.ContextWindow > 0 && req.ContextSize > m.ContextWindow {
			reasons = append(reasons, "context window too small")
		}

		if len(reasons) > 0 {
			rejected[m.Name] = reasons
			continue
		}

		candidates = append(candidates, m)
		rulesMatched = append(rulesMatched, "eligible_"+m.Name)
	}

	decision.RulesEvaluated = rulesEvaluated
	if len(rejected) > 0 {
		decision.RejectedModels = rejected
	}

	if len(candidates) == 0 {
		decision.NoEligible = true
		reasoning.WriteString("No eligible models, using default.")
		if summary := summarizeRejectedModels(rejected); summary != "" {
			reasoning.WriteString(" Rejected: " + summary + ".")
		}
		decision.RulesMatched = rulesMatched
		decision.Reasoning = reasoning.String()
		return cfg.DefaultModel
	}

	// Score candidates
	//
	// The scoring system implements the urgency×quality routing matrix:
	//   - Simple tasks should prefer fast/cheap models
	//   - Complex tasks earn expensive models
	//   - Cost is always a factor, never free
	// When local_only is explicitly "false", the caller wants the router
	// to consider cloud/paid models without local bias. This disables
	// free_model_bonus, local_first, and mission-based cheap-model
	// bonuses so quality-based scoring can dominate. Supervisor
	// iterations in the metacognitive loop depend on this to reach
	// frontier models.
	explicitlyNotLocal := req.Hints != nil && req.Hints[HintLocalOnly] == "false"

	scores := make(map[string]int)
	for _, m := range candidates {
		score := 0

		// --- Complexity matching ---
		if decision.Complexity >= m.MinComplexity {
			score += 20
		}
		if decision.Complexity == ComplexitySimple && m.Speed >= 7 {
			score += 15 // Prefer fast for simple
			rulesMatched = append(rulesMatched, "speed_bonus_"+m.Name)
		}
		if decision.Complexity == ComplexityComplex {
			// Scale bonus with quality: quality 8 = +16, quality 10 = +20
			if m.Quality >= 7 {
				score += m.Quality * 2
				rulesMatched = append(rulesMatched, "quality_bonus_"+m.Name)
			}
		}

		// --- Cost awareness ---
		// Expensive models must justify their cost. The penalty scales
		// inversely with complexity: simple tasks penalize heavily,
		// complex tasks penalize lightly.
		if m.CostTier > 0 {
			switch decision.Complexity {
			case ComplexitySimple:
				score -= m.CostTier * 15 // e.g. tier 3 = -45
				rulesMatched = append(rulesMatched, "cost_penalty_simple_"+m.Name)
			case ComplexityModerate:
				score -= m.CostTier * 8 // e.g. tier 3 = -24
				rulesMatched = append(rulesMatched, "cost_penalty_moderate_"+m.Name)
			case ComplexityComplex:
				score -= m.CostTier * 3 // e.g. tier 3 = -9
				rulesMatched = append(rulesMatched, "cost_penalty_complex_"+m.Name)
			}
		}

		// Free/local models get a bonus for non-complex tasks
		// (unless caller explicitly opted out of local preference)
		if m.CostTier == 0 && decision.Complexity < ComplexityComplex && !explicitlyNotLocal {
			score += 15
			rulesMatched = append(rulesMatched, "free_model_bonus_"+m.Name)
		}

		// --- Context size penalty for small models ---
		contextRatio := float64(req.ContextSize) / float64(m.ContextWindow)
		if contextRatio > 0.3 {
			if m.Quality < 7 {
				score -= 30
				rulesMatched = append(rulesMatched, "context_penalty_"+m.Name)
			}
		}
		if contextRatio > 0.5 && m.Quality >= 7 {
			score += 10
			rulesMatched = append(rulesMatched, "context_bonus_"+m.Name)
		}

		// --- Tool count consideration ---
		if req.ToolCount > 4 && m.Quality < 7 {
			score -= 20
			rulesMatched = append(rulesMatched, "tools_penalty_"+m.Name)
		}

		// --- Local preference ---
		// (unless caller explicitly opted out of local preference)
		if cfg.LocalFirst && m.CostTier == 0 && !explicitlyNotLocal {
			score += 10
			rulesMatched = append(rulesMatched, "local_first_"+m.Name)
		}

		// --- Interactive needs speed ---
		if req.Priority == PriorityInteractive && m.Speed >= 7 {
			score += 10
			rulesMatched = append(rulesMatched, "interactive_speed_"+m.Name)
		}

		// --- Hint-based adjustments ---
		if req.Hints != nil {
			// Channel hint: HA/voice channels prefer cheap+fast.
			// Note: openwebui channel no longer gets a quality bonus — the routing
			// profile (thane:thinking vs thane:latest) is the correct signal for
			// quality preference. See issue #107.
			switch req.Hints[HintChannel] {
			case "homeassistant", "voice":
				// HA/voice: strongly prefer fast and cheap
				if m.CostTier == 0 {
					score += 20
					rulesMatched = append(rulesMatched, "channel_ha_bonus_"+m.Name)
				}
				if m.Speed >= 7 {
					score += 10
					rulesMatched = append(rulesMatched, "channel_ha_speed_"+m.Name)
				}
			}

			// Quality floor: disqualify models below the requested minimum
			if floor, ok := req.Hints[HintQualityFloor]; ok {
				if floorInt, err := strconv.Atoi(floor); err == nil && m.Quality < floorInt {
					score -= 100 // effectively disqualify
					rulesMatched = append(rulesMatched, "below_quality_floor_"+m.Name)
				}
			}

			// Mission hint: background/metacognitive tasks prefer cheap models
			// (unless caller explicitly opted out of local preference).
			// Note: "conversation" mission no longer gets a quality bonus —
			// thane:thinking sets quality_floor for that purpose. See issue #107.
			if mission := req.Hints[HintMission]; (mission == "background" || mission == "metacognitive") && !explicitlyNotLocal {
				if m.CostTier == 0 {
					score += 20
					rulesMatched = append(rulesMatched, "mission_background_bonus_"+m.Name)
				}
			}

			// Model preference: soft boost for suggested model
			if pref, ok := req.Hints[HintModelPreference]; ok && pref == m.Name {
				score += 25
				rulesMatched = append(rulesMatched, "model_preference_"+m.Name)
			}

			// Local only: heavily penalize paid models
			if req.Hints[HintLocalOnly] == "true" && m.CostTier > 0 {
				score -= 200
				rulesMatched = append(rulesMatched, "local_only_penalty_"+m.Name)
			}

			// Speed preference: bonus for fast models when caller values
			// latency over maximum quality. Can be decisive among similarly
			// priced options.
			if req.Hints[HintPreferSpeed] == "true" && m.Speed >= 7 {
				score += 15
				rulesMatched = append(rulesMatched, "prefer_speed_bonus_"+m.Name)
			}
		}

		if until := r.resourceCooldownDeadline(m.ResourceID); !until.IsZero() && now.Before(until) {
			score -= 100
			rulesMatched = append(rulesMatched, "resource_timeout_cooldown_"+m.Name)
		}

		scores[m.Name] = score
	}

	decision.Scores = scores

	// Pick highest score; on tie, prefer cheaper model; on equal cost, prefer higher quality
	var best Model
	bestScore := -1 << 30 // effectively -infinity
	for _, m := range candidates {
		s := scores[m.Name]
		if s > bestScore ||
			(s == bestScore && m.CostTier < best.CostTier) ||
			(s == bestScore && m.CostTier == best.CostTier && m.Quality > best.Quality) {
			best = m
			bestScore = s
		}
	}

	reasoning.WriteString("Selected " + best.Name)
	reasoning.WriteString(" (score=" + strconv.Itoa(bestScore) + ")")
	reasoning.WriteString(" for " + decision.Complexity.String() + " " + decision.DetectedIntent + " query.")
	if req.NeedsTools && best.SupportsTools {
		reasoning.WriteString(" Tool-capable deployment required.")
	}
	if req.NeedsStreaming && best.SupportsStreaming {
		reasoning.WriteString(" Streaming-capable deployment required.")
	}
	if req.NeedsImages && best.SupportsImages {
		reasoning.WriteString(" Image-capable deployment required.")
	}

	if cfg.LocalFirst && best.CostTier == 0 {
		reasoning.WriteString(" Local-first preference applied.")
	}

	decision.RulesMatched = rulesMatched
	decision.Reasoning = reasoning.String()

	return best.Name
}

// RecordOutcome updates a decision with execution results.
func (r *Router) RecordOutcome(requestID string, latencyMs int64, tokensUsed int, success bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := len(r.auditLog) - 1; i >= 0; i-- {
		if r.auditLog[i].RequestID == requestID {
			r.auditLog[i].LatencyMs = latencyMs
			r.auditLog[i].TokensUsed = tokensUsed
			r.auditLog[i].Success = &success

			// Update stats
			model := r.auditLog[i].ModelSelected
			resource := r.auditLog[i].ResourceSelected
			meta := r.stats.DeploymentStats[model]
			if success {
				r.stats.SuccessCount++
				meta.Successes++
				if resource != "" {
					delete(r.resourceCooldownUntil, resource)
				}
			} else {
				r.stats.FailureCount++
				meta.Failures++
			}
			outcomes := meta.Successes + meta.Failures
			r.stats.AvgLatencyMs[model] = weightedAverage(r.stats.AvgLatencyMs[model], outcomes, latencyMs)
			meta.AvgLatencyMs = weightedAverage(meta.AvgLatencyMs, outcomes, latencyMs)
			meta.AvgTokensUsed = weightedAverage(meta.AvgTokensUsed, outcomes, int64(tokensUsed))
			r.stats.DeploymentStats[model] = meta
			break
		}
	}
}

// RecordFailure updates a failed routing outcome and optionally applies
// a temporary resource cooldown so automatic routing can avoid a runner
// that is timing out on real chat traffic.
func (r *Router) RecordFailure(requestID string, latencyMs int64, tokensUsed int, resourceTimeout bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := len(r.auditLog) - 1; i >= 0; i-- {
		if r.auditLog[i].RequestID == requestID {
			r.auditLog[i].LatencyMs = latencyMs
			r.auditLog[i].TokensUsed = tokensUsed
			success := false
			r.auditLog[i].Success = &success

			model := r.auditLog[i].ModelSelected
			resource := r.auditLog[i].ResourceSelected
			meta := r.stats.DeploymentStats[model]
			r.stats.FailureCount++
			meta.Failures++
			outcomes := meta.Successes + meta.Failures
			r.stats.AvgLatencyMs[model] = weightedAverage(r.stats.AvgLatencyMs[model], outcomes, latencyMs)
			meta.AvgLatencyMs = weightedAverage(meta.AvgLatencyMs, outcomes, latencyMs)
			meta.AvgTokensUsed = weightedAverage(meta.AvgTokensUsed, outcomes, int64(tokensUsed))
			r.stats.DeploymentStats[model] = meta

			if resourceTimeout && resource != "" {
				r.resourceCooldownUntil[resource] = time.Now().Add(resourceTimeoutCooldown)
			}
			break
		}
	}
}

// recordDecision adds a decision to the audit log.
func (r *Router) recordDecision(d Decision) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Trim if over capacity
	if len(r.auditLog) >= r.config.MaxAuditLog {
		r.auditLog = r.auditLog[1:]
	}

	r.auditLog = append(r.auditLog, d)

	// Update stats
	r.stats.TotalRequests++
	r.stats.ModelCounts[d.ModelSelected]++
	r.stats.ComplexityCounts[d.Complexity.String()]++
	if d.ProviderSelected != "" {
		r.stats.ProviderCounts[d.ProviderSelected]++
	}
	if d.ResourceSelected != "" {
		r.stats.ResourceCounts[d.ResourceSelected]++
	}
	meta := r.stats.DeploymentStats[d.ModelSelected]
	meta.Provider = d.ProviderSelected
	meta.Resource = d.ResourceSelected
	meta.UpstreamModel = d.UpstreamModelSelected
	meta.Requests++
	r.stats.DeploymentStats[d.ModelSelected] = meta
}

// GetAuditLog returns recent routing decisions.
func (r *Router) GetAuditLog(limit int) []Decision {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 || limit > len(r.auditLog) {
		limit = len(r.auditLog)
	}

	// Return most recent
	start := len(r.auditLog) - limit
	result := make([]Decision, limit)
	copy(result, r.auditLog[start:])
	return result
}

// GetStats returns routing statistics.
func (r *Router) GetStats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Stats{
		TotalRequests:    r.stats.TotalRequests,
		ModelCounts:      cloneInt64Map(r.stats.ModelCounts),
		AvgLatencyMs:     cloneInt64Map(r.stats.AvgLatencyMs),
		ComplexityCounts: cloneInt64Map(r.stats.ComplexityCounts),
		SuccessCount:     r.stats.SuccessCount,
		FailureCount:     r.stats.FailureCount,
		ProviderCounts:   cloneInt64Map(r.stats.ProviderCounts),
		ResourceCounts:   cloneInt64Map(r.stats.ResourceCounts),
		ResourceHealth:   activeResourceHealthSnapshot(r.resourceCooldownUntil, time.Now()),
		DeploymentStats:  cloneDeploymentStatsMap(r.stats.DeploymentStats),
	}
}

func (r *Router) resourceCooldownDeadline(resource string) time.Time {
	if strings.TrimSpace(resource) == "" {
		return time.Time{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.resourceCooldownUntil[resource]
}

func activeResourceHealthSnapshot(in map[string]time.Time, now time.Time) map[string]ResourceHealth {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ResourceHealth)
	for resource, until := range in {
		if until.IsZero() || !until.After(now) {
			continue
		}
		out[resource] = ResourceHealth{
			CooldownUntil:  until,
			CooldownReason: "recent timeout",
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// GetModels returns a copy of the configured model list. The returned
// slice is safe to mutate without affecting the router.
func (r *Router) GetModels() []Model {
	return cloneModels(r.configSnapshot().Models)
}

// DefaultModel returns the router's current fallback/default model.
func (r *Router) DefaultModel() string {
	return r.configSnapshot().DefaultModel
}

// Explain returns details about why a specific decision was made.
func (r *Router) Explain(requestID string) *Decision {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for i := len(r.auditLog) - 1; i >= 0; i-- {
		if r.auditLog[i].RequestID == requestID {
			d := r.auditLog[i]
			return &d
		}
	}
	return nil
}

// Helper functions

// generateRequestID creates a timestamp-based ID for log correlation.
func generateRequestID() string {
	return time.Now().Format("20060102-150405.000")
}

// priorityString returns the human-readable name of a priority level.
func priorityString(p Priority) string {
	if p == PriorityInteractive {
		return "interactive"
	}
	return "background"
}

func summarizeRejectedModels(rejected map[string][]string) string {
	if len(rejected) == 0 {
		return ""
	}
	names := make([]string, 0, len(rejected))
	for name := range rejected {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		reasons := rejected[name]
		if len(reasons) == 0 {
			continue
		}
		parts = append(parts, name+" ("+strings.Join(reasons, ", ")+")")
	}
	return strings.Join(parts, "; ")
}

func (r *Router) populateSelectionMetadata(cfg Config, decision *Decision, modelName string) {
	if decision == nil {
		return
	}
	for _, m := range cfg.Models {
		if m.Name != modelName {
			continue
		}
		decision.UpstreamModelSelected = m.UpstreamModel
		decision.ProviderSelected = m.Provider
		decision.ResourceSelected = m.ResourceID
		return
	}
}

// UpdateConfig swaps the router's live model configuration while
// preserving accumulated audit history and stats.
func (r *Router) UpdateConfig(cfg Config) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cfg.MaxAuditLog <= 0 {
		cfg.MaxAuditLog = r.config.MaxAuditLog
		if cfg.MaxAuditLog <= 0 {
			cfg.MaxAuditLog = 1000
		}
	}
	cfg.Models = cloneModels(cfg.Models)
	r.config = cfg
	if len(r.auditLog) > cfg.MaxAuditLog {
		r.auditLog = append([]Decision(nil), r.auditLog[len(r.auditLog)-cfg.MaxAuditLog:]...)
	}
}

func weightedAverage(currentAvg, samplesAfterUpdate, next int64) int64 {
	if samplesAfterUpdate <= 1 {
		return next
	}
	previousSamples := samplesAfterUpdate - 1
	return ((currentAvg * previousSamples) + next) / samplesAfterUpdate
}

func cloneInt64Map(in map[string]int64) map[string]int64 {
	if len(in) == 0 {
		return map[string]int64{}
	}
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneDeploymentStatsMap(in map[string]DeploymentStats) map[string]DeploymentStats {
	if len(in) == 0 {
		return map[string]DeploymentStats{}
	}
	out := make(map[string]DeploymentStats, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Package router handles intelligent model selection.
package router

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Request contains the information needed for routing decisions.
type Request struct {
	Query       string            // The user's input
	ContextSize int               // Estimated tokens of context (talents, history)
	NeedsTools  bool              // Whether tool calling is required
	ToolCount   int               // Number of tools available
	Priority    Priority          // Latency requirements
	Metadata    map[string]string // Additional hints
}

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
	Priority       string     `json:"priority"`
	DetectedIntent string     `json:"detected_intent,omitempty"`
	Complexity     Complexity `json:"complexity"`

	// Decision process
	RulesEvaluated []string       `json:"rules_evaluated"`
	RulesMatched   []string       `json:"rules_matched"`
	Scores         map[string]int `json:"scores,omitempty"`

	// Outcome
	ModelSelected string `json:"model_selected"`
	Reasoning     string `json:"reasoning"`

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
	Name          string     // Model identifier (e.g., "qwen3:4b")
	Provider      string     // "ollama" or "anthropic" etc
	SupportsTools bool       // Can do tool calling
	ContextWindow int        // Max tokens
	Speed         int        // Relative speed (1-10, 10=fastest)
	Quality       int        // Relative quality (1-10, 10=best)
	CostTier      int        // 0=free/local, 1=cheap, 2=moderate, 3=expensive
	MinComplexity Complexity // Don't use for simpler than this
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

	mu       sync.RWMutex
	auditLog []Decision
	stats    Stats
}

// Stats tracks routing statistics.
type Stats struct {
	TotalRequests    int64            `json:"total_requests"`
	ModelCounts      map[string]int64 `json:"model_counts"`
	AvgLatencyMs     map[string]int64 `json:"avg_latency_ms"`
	ComplexityCounts map[string]int64 `json:"complexity_counts"`
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
		},
	}
}

// Route selects a model for the given request.
func (r *Router) Route(ctx context.Context, req Request) (string, *Decision) {
	decision := &Decision{
		RequestID:   generateRequestID(),
		Timestamp:   time.Now(),
		QueryLength: len(req.Query),
		ContextSize: req.ContextSize,
		NeedsTools:  req.NeedsTools,
		Priority:    priorityString(req.Priority),
	}

	// Analyze complexity
	decision.Complexity = r.analyzeComplexity(req.Query)
	decision.DetectedIntent = r.detectIntent(req.Query)

	// Evaluate rules and select model
	model := r.selectModel(req, decision)
	decision.ModelSelected = model

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
func (r *Router) analyzeComplexity(query string) Complexity {
	q := strings.ToLower(query)

	// Complex indicators
	complexWords := []string{"explain", "why", "analyze", "compare", "history", "pattern", "trend", "recommend"}
	for _, w := range complexWords {
		if strings.Contains(q, w) {
			return ComplexityComplex
		}
	}

	// Simple indicators (direct commands)
	simplePatterns := []string{"turn on", "turn off", "set ", "lock", "unlock", "open", "close"}
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
func (r *Router) selectModel(req Request, decision *Decision) string {
	var rulesEvaluated, rulesMatched []string
	var reasoning strings.Builder

	// Find eligible models
	var candidates []Model
	for _, m := range r.config.Models {
		rulesEvaluated = append(rulesEvaluated, "check_"+m.Name)

		// Must support tools if needed
		if req.NeedsTools && !m.SupportsTools {
			continue
		}

		// Must fit context
		if req.ContextSize > 0 && m.ContextWindow > 0 && req.ContextSize > m.ContextWindow {
			continue
		}

		candidates = append(candidates, m)
		rulesMatched = append(rulesMatched, "eligible_"+m.Name)
	}

	decision.RulesEvaluated = rulesEvaluated

	if len(candidates) == 0 {
		reasoning.WriteString("No eligible models, using default. ")
		decision.RulesMatched = rulesMatched
		decision.Reasoning = reasoning.String()
		return r.config.DefaultModel
	}

	// Score candidates
	scores := make(map[string]int)
	for _, m := range candidates {
		score := 0

		// Complexity matching
		if decision.Complexity >= m.MinComplexity {
			score += 20
		}
		if decision.Complexity == ComplexitySimple && m.Speed >= 7 {
			score += 15 // Prefer fast for simple
		}
		if decision.Complexity == ComplexityComplex && m.Quality >= 7 {
			score += 15 // Prefer quality for complex
		}

		// Context size penalty for small models
		// Small context window models struggle with large prompts
		contextRatio := float64(req.ContextSize) / float64(m.ContextWindow)
		if contextRatio > 0.3 {
			// Using >30% of context window - penalize low-quality models
			if m.Quality < 7 {
				score -= 30 // Heavy penalty
				decision.RulesMatched = append(decision.RulesMatched, "context_penalty_"+m.Name)
			}
		}
		if contextRatio > 0.5 && m.Quality >= 7 {
			// High context but capable model - bonus
			score += 10
			decision.RulesMatched = append(decision.RulesMatched, "context_bonus_"+m.Name)
		}

		// Tool count consideration - more tools = need smarter model
		if req.ToolCount > 4 && m.Quality < 7 {
			score -= 20 // Penalty for many tools on weak model
			decision.RulesMatched = append(decision.RulesMatched, "tools_penalty_"+m.Name)
		}

		// Local preference
		if r.config.LocalFirst && m.CostTier == 0 {
			score += 10
		}

		// Interactive needs speed
		if req.Priority == PriorityInteractive && m.Speed >= 7 {
			score += 10
		}

		scores[m.Name] = score
	}

	decision.Scores = scores

	// Pick highest score
	var best Model
	bestScore := -1
	for _, m := range candidates {
		if scores[m.Name] > bestScore {
			best = m
			bestScore = scores[m.Name]
		}
	}

	reasoning.WriteString("Selected " + best.Name)
	reasoning.WriteString(" (score=" + strconv.Itoa(bestScore) + ")")
	reasoning.WriteString(" for " + decision.Complexity.String() + " " + decision.DetectedIntent + " query.")

	if r.config.LocalFirst && best.CostTier == 0 {
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
			r.stats.AvgLatencyMs[model] = (r.stats.AvgLatencyMs[model] + latencyMs) / 2
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
	return r.stats
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

func generateRequestID() string {
	return time.Now().Format("20060102-150405.000")
}

func priorityString(p Priority) string {
	if p == PriorityInteractive {
		return "interactive"
	}
	return "background"
}

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/anticipation"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// anticipationMatcher is the subset of anticipation.Store needed by the
// wake bridge. Using an interface keeps the bridge testable without a
// real database.
type anticipationMatcher interface {
	Match(ctx anticipation.WakeContext) ([]*anticipation.Anticipation, error)
}

// wakeContextSetter sets the wake context for system prompt injection.
// anticipation.Provider satisfies this interface.
type wakeContextSetter interface {
	SetWakeContext(ctx anticipation.WakeContext)
}

// wakeStateGetter fetches entity state from Home Assistant. Satisfied
// by homeassistant.Client.
type wakeStateGetter interface {
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
}

// WakeBridgeConfig holds configuration for creating a WakeBridge.
type WakeBridgeConfig struct {
	Store    anticipationMatcher
	Runner   agentRunner
	Provider wakeContextSetter
	Logger   *slog.Logger
	Ctx      context.Context
	Cooldown time.Duration   // per-anticipation cooldown; zero defaults to 5m
	HA       wakeStateGetter // optional; nil disables entity context injection
}

// WakeBridge connects state change events to the anticipation store and
// triggers agent wakes when an active anticipation matches. It enforces
// per-anticipation cooldowns to prevent rapid re-triggering from
// frequent state changes.
type WakeBridge struct {
	store    anticipationMatcher
	runner   agentRunner
	provider wakeContextSetter
	ha       wakeStateGetter
	logger   *slog.Logger
	ctx      context.Context

	cooldown time.Duration

	mu          sync.Mutex
	lastFire    map[string]time.Time // anticipation ID → last trigger time
	lastCleanup time.Time            // last time stale entries were evicted
}

// NewWakeBridge creates a wake bridge with the given configuration.
func NewWakeBridge(cfg WakeBridgeConfig) *WakeBridge {
	cooldown := cfg.Cooldown
	if cooldown == 0 {
		cooldown = 5 * time.Minute
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &WakeBridge{
		store:    cfg.Store,
		runner:   cfg.Runner,
		provider: cfg.Provider,
		ha:       cfg.HA,
		logger:   logger,
		ctx:      cfg.Ctx,
		cooldown: cooldown,
		lastFire: make(map[string]time.Time),
	}
}

// cleanupInterval controls how often stale cooldown entries are evicted.
const cleanupInterval = 10 * time.Minute

// HandleStateChange is a homeassistant.StateWatchHandler. It builds a
// WakeContext from the state change, updates the anticipation provider
// for system prompt injection, queries the anticipation store for
// matches, and fires async agent runs for each match not on cooldown.
func (b *WakeBridge) HandleStateChange(entityID, oldState, newState string) {
	// HA fires state_changed on attribute updates even when the state
	// itself hasn't changed. Skip these to avoid log noise and
	// unnecessary anticipation matching.
	if oldState == newState {
		return
	}

	// Periodically evict stale cooldown entries to prevent unbounded map growth.
	b.maybeCleanup()

	wakeCtx := anticipation.WakeContext{
		Time:        time.Now(),
		EventType:   "state_change",
		EntityID:    entityID,
		EntityState: newState,
	}

	// Update context provider for system prompt injection on user conversations.
	b.provider.SetWakeContext(wakeCtx)

	matched, err := b.store.Match(wakeCtx)
	if err != nil {
		b.logger.Error("anticipation match failed",
			"entity_id", entityID,
			"error", err,
		)
		return
	}

	if len(matched) == 0 {
		b.logger.Debug("state change, no anticipation match",
			"entity_id", entityID,
			"old_state", oldState,
			"new_state", newState,
		)
		return
	}

	for _, a := range matched {
		if b.onCooldown(a.ID) {
			b.logger.Debug("anticipation on cooldown, skipping",
				"anticipation_id", a.ID,
				"entity_id", entityID,
			)
			continue
		}
		b.markTriggered(a.ID)

		entityCtx := b.fetchEntityContext(a, entityID)
		msg := formatWakeMessage(a, entityID, oldState, newState, entityCtx)
		b.logger.Info("anticipation matched, triggering wake",
			"anticipation_id", a.ID,
			"anticipation", a.Description,
			"entity_id", entityID,
			"old_state", oldState,
			"new_state", newState,
		)

		// Run in a separate goroutine so the state watcher is not blocked.
		go b.runWake(a.ID, a.Description, msg)
	}
}

// wakeTimeout bounds how long a single anticipation wake may run.
const wakeTimeout = 5 * time.Minute

// runWake executes a single agent wake for a matched anticipation.
// Each wake runs with a bounded timeout so a stuck LLM call cannot
// leak a goroutine. Errors are logged but never propagated — the
// state watcher must not be disrupted by agent failures.
func (b *WakeBridge) runWake(anticipationID, description, message string) {
	ctx, cancel := context.WithTimeout(b.ctx, wakeTimeout)
	defer cancel()

	req := &agent.Request{
		Messages: []agent.Message{{Role: "user", Content: message}},
		Hints: map[string]string{
			"source":                "anticipation",
			"anticipation_id":       anticipationID,
			router.HintLocalOnly:    "true",
			router.HintQualityFloor: "5",
			router.HintMission:      "anticipation",
		},
	}

	resp, err := b.runner.Run(ctx, req, nil)
	if err != nil {
		b.logger.Error("anticipation wake failed",
			"anticipation_id", anticipationID,
			"error", err,
		)
		return
	}
	b.logger.Info("anticipation wake completed",
		"anticipation_id", anticipationID,
		"anticipation", description,
		"result_len", len(resp.Content),
	)
}

// onCooldown reports whether the anticipation has fired too recently.
func (b *WakeBridge) onCooldown(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	last, ok := b.lastFire[id]
	if !ok {
		return false
	}
	return time.Since(last) < b.cooldown
}

// markTriggered records the current time as the last trigger for the
// given anticipation ID.
func (b *WakeBridge) markTriggered(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastFire[id] = time.Now()
}

// maybeCleanup evicts stale cooldown entries if enough time has passed
// since the last cleanup. Called on every HandleStateChange to bound
// map growth without requiring a separate goroutine.
func (b *WakeBridge) maybeCleanup() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if time.Since(b.lastCleanup) < cleanupInterval {
		return
	}
	b.lastCleanup = time.Now()

	threshold := 2 * b.cooldown
	for id, t := range b.lastFire {
		if time.Since(t) > threshold {
			delete(b.lastFire, id)
		}
	}
}

// fetchEntityContext fetches and formats the states of entities listed
// in the anticipation's ContextEntities plus the triggering entity. It
// is best-effort: fetch failures are logged but do not prevent the wake.
func (b *WakeBridge) fetchEntityContext(a *anticipation.Anticipation, triggerEntityID string) string {
	if b.ha == nil {
		return ""
	}

	// Build deduplicated entity list: context entities + trigger entity.
	// Empty/whitespace-only IDs are skipped defensively.
	seen := make(map[string]bool, len(a.ContextEntities)+1)
	var entities []string
	for _, id := range a.ContextEntities {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		entities = append(entities, id)
	}
	triggerEntityID = strings.TrimSpace(triggerEntityID)
	if triggerEntityID != "" && !seen[triggerEntityID] {
		entities = append(entities, triggerEntityID)
	}

	if len(entities) == 0 {
		return ""
	}

	fetchCtx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
	defer cancel()

	var sb strings.Builder
	for _, id := range entities {
		state, err := b.ha.GetState(fetchCtx, id)
		if err != nil {
			b.logger.Warn("anticipation entity context fetch failed",
				"entity_id", id, "error", err)
			fmt.Fprintf(&sb, "Entity: %s\nState: (fetch failed)\n\n", id)
			continue
		}
		sb.WriteString(tools.FormatEntityState(state))
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatWakeMessage builds the user-facing message for an anticipation
// wake. It includes the anticipation description, its stored context
// (instructions for the agent), the entity state change details, and
// optional entity state context.
func formatWakeMessage(a *anticipation.Anticipation, entityID, oldState, newState, entityContext string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Anticipation matched: %q\n\n", a.Description))
	sb.WriteString(fmt.Sprintf("Entity %s changed from %q to %q.\n\n", entityID, oldState, newState))
	if a.Context != "" {
		sb.WriteString("Instructions you left for yourself:\n")
		sb.WriteString(a.Context)
		sb.WriteString("\n")
	}
	if entityContext != "" {
		sb.WriteString("\nRelevant entity states:\n")
		sb.WriteString(entityContext)
	}
	return sb.String()
}

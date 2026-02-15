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
	"github.com/nugget/thane-ai-agent/internal/router"
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

// WakeBridgeConfig holds configuration for creating a WakeBridge.
type WakeBridgeConfig struct {
	Store    anticipationMatcher
	Runner   agentRunner
	Provider wakeContextSetter
	Logger   *slog.Logger
	Ctx      context.Context
	Cooldown time.Duration // per-anticipation cooldown; zero defaults to 5m
}

// WakeBridge connects state change events to the anticipation store and
// triggers agent wakes when an active anticipation matches. It enforces
// per-anticipation cooldowns to prevent rapid re-triggering from
// frequent state changes.
type WakeBridge struct {
	store    anticipationMatcher
	runner   agentRunner
	provider wakeContextSetter
	logger   *slog.Logger
	ctx      context.Context

	cooldown time.Duration

	mu       sync.Mutex
	lastFire map[string]time.Time // anticipation ID → last trigger time
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
		logger:   logger,
		ctx:      cfg.Ctx,
		cooldown: cooldown,
		lastFire: make(map[string]time.Time),
	}
}

// HandleStateChange is a homeassistant.StateWatchHandler. It builds a
// WakeContext from the state change, updates the anticipation provider
// for system prompt injection, queries the anticipation store for
// matches, and fires async agent runs for each match not on cooldown.
func (b *WakeBridge) HandleStateChange(entityID, oldState, newState string) {
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

		msg := formatWakeMessage(a, entityID, oldState, newState)
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

// runWake executes a single agent wake for a matched anticipation.
// Errors are logged but never propagated — the state watcher must
// not be disrupted by agent failures.
func (b *WakeBridge) runWake(anticipationID, description, message string) {
	req := &agent.Request{
		Messages: []agent.Message{{Role: "user", Content: message}},
		Hints: map[string]string{
			"source":                "anticipation",
			"anticipation_id":       anticipationID,
			router.HintLocalOnly:    "true",
			router.HintQualityFloor: "1",
			router.HintMission:      "anticipation",
		},
	}

	resp, err := b.runner.Run(b.ctx, req, nil)
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

// formatWakeMessage builds the user-facing message for an anticipation
// wake. It includes the anticipation description, its stored context
// (instructions for the agent), and the entity state change details.
func formatWakeMessage(a *anticipation.Anticipation, entityID, oldState, newState string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Anticipation matched: %q\n\n", a.Description))
	sb.WriteString(fmt.Sprintf("Entity %s changed from %q to %q.\n\n", entityID, oldState, newState))
	if a.Context != "" {
		sb.WriteString("Instructions you left for yourself:\n")
		sb.WriteString(a.Context)
		sb.WriteString("\n")
	}
	return sb.String()
}

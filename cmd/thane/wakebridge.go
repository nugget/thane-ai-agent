package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	OnCooldown(id string, globalDefault time.Duration) (bool, error)
	MarkFired(id string) error
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

// wakeResolver resolves (marks as handled) an anticipation by ID.
// Satisfied by anticipation.Store.
type wakeResolver interface {
	Resolve(id string) error
}

// WakeBridgeConfig holds configuration for creating a WakeBridge.
type WakeBridgeConfig struct {
	Store    anticipationMatcher
	Resolver wakeResolver // used to auto-resolve one-shot anticipations
	Runner   agentRunner
	Provider wakeContextSetter
	Logger   *slog.Logger
	Ctx      context.Context
	Cooldown time.Duration   // global default cooldown; per-anticipation overrides in DB; zero defaults to 5m
	HA       wakeStateGetter // optional; nil disables entity context injection
}

// WakeBridge connects state change events to the anticipation store and
// triggers agent wakes when an active anticipation matches. Per-anticipation
// cooldowns are stored in SQLite and checked via the store interface.
type WakeBridge struct {
	store    anticipationMatcher
	resolver wakeResolver
	runner   agentRunner
	provider wakeContextSetter
	ha       wakeStateGetter
	logger   *slog.Logger
	ctx      context.Context

	cooldown time.Duration // global default when per-anticipation cooldown is 0
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
		resolver: cfg.Resolver,
		runner:   cfg.Runner,
		provider: cfg.Provider,
		ha:       cfg.HA,
		logger:   logger,
		ctx:      cfg.Ctx,
		cooldown: cooldown,
	}
}

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

	// Ignore transitions to/from "unavailable" — these are HA restarts
	// or entity connectivity issues, not meaningful state changes.
	if oldState == "unavailable" || newState == "unavailable" {
		return
	}

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
		onCooldown, err := b.store.OnCooldown(a.ID, b.cooldown)
		if err != nil {
			b.logger.Error("failed to check anticipation cooldown, skipping",
				"anticipation_id", a.ID,
				"error", err,
			)
			continue
		}
		if onCooldown {
			b.logger.Debug("anticipation on cooldown, skipping",
				"anticipation_id", a.ID,
				"entity_id", entityID,
			)
			continue
		}
		if err := b.store.MarkFired(a.ID); err != nil {
			b.logger.Error("failed to mark anticipation fired, skipping",
				"anticipation_id", a.ID,
				"error", err,
			)
			continue
		}

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
		go b.runWake(a, msg)
	}
}

// wakeTimeout bounds how long a single anticipation wake may run.
const wakeTimeout = 5 * time.Minute

// wakeLifecycleTools are the anticipation lifecycle tools excluded from
// recurring wake runs. The creating model decides recurring vs one-shot;
// the wake model should focus on executing the action.
var wakeLifecycleTools = []string{"resolve_anticipation", "cancel_anticipation"}

// runWake executes a single agent wake for a matched anticipation.
// Each wake runs with a bounded timeout so a stuck LLM call cannot
// leak a goroutine. Errors are logged but never propagated — the
// state watcher must not be disrupted by agent failures.
//
// Lifecycle: recurring anticipations keep firing; one-shot anticipations
// are auto-resolved after a successful wake.
func (b *WakeBridge) runWake(a *anticipation.Anticipation, message string) {
	ctx, cancel := context.WithTimeout(b.ctx, wakeTimeout)
	defer cancel()

	req := &agent.Request{
		// Each anticipation gets its own conversation so wake history
		// is isolated from interactive chat (prevents context bloat).
		ConversationID: fmt.Sprintf("wake-%s", a.ID),
		Messages:       []agent.Message{{Role: "user", Content: message}},
		Hints: map[string]string{
			"source":                    "anticipation",
			"anticipation_id":           a.ID,
			router.HintLocalOnly:        "true",
			router.HintQualityFloor:     "6", // floor is inclusive; excludes quality≤5 models
			router.HintMission:          "anticipation",
			router.HintDelegationGating: "disabled", // full tool access, no delegation indirection
		},
	}

	// Recurring wakes should not have lifecycle tools — the creating
	// model decided this anticipation should persist across wakes.
	if a.Recurring {
		req.ExcludeTools = wakeLifecycleTools
	}

	resp, err := b.runner.Run(ctx, req, nil)
	if err != nil {
		b.logger.Error("anticipation wake failed",
			"anticipation_id", a.ID,
			"error", err,
		)
		return
	}
	b.logger.Info("anticipation wake completed",
		"anticipation_id", a.ID,
		"anticipation", a.Description,
		"recurring", a.Recurring,
		"result_len", len(resp.Content),
	)

	// Auto-resolve one-shot anticipations after successful wake.
	if !a.Recurring && b.resolver != nil {
		if err := b.resolver.Resolve(a.ID); err != nil {
			b.logger.Warn("failed to auto-resolve anticipation",
				"anticipation_id", a.ID, "error", err)
		} else {
			b.logger.Info("anticipation auto-resolved",
				"anticipation_id", a.ID, "anticipation", a.Description)
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
	if a.Recurring {
		sb.WriteString("\nThis is a recurring anticipation — it will continue to fire on future matches. Focus on executing the action, not managing the anticipation.\n")
	}
	return sb.String()
}

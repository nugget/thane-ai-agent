package awareness

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// WatchlistTools is the [tools.Provider] for the add_entity_subscription,
// list_entity_subscriptions, and remove_entity_subscription tools. It
// owns the watchlist store the handlers read/write and an optional HA
// registry client used to preview what a glob or area/label/floor target
// expands to at the moment it is authored — so the model learns
// immediately that area:office matches three entities, or that a typo'd
// area:atrium matches none.
type WatchlistTools struct {
	store          *WatchlistStore
	registry       HARegistryClient
	onIngestChange func()
	logger         *slog.Logger
}

// WatchlistToolsConfig captures the dependencies for [NewWatchlistTools].
type WatchlistToolsConfig struct {
	// Store is the persistent watchlist store. Required.
	Store *WatchlistStore
	// Registry is the HA client used to preview a target's current
	// membership when a subscription is authored or listed. Optional:
	// when nil, subscriptions still work but carry no expansion preview.
	Registry HARegistryClient
	// OnIngestChange is invoked after any mutation that may affect the
	// state-watcher ingestion filter (an ingest/both-mode add, or any
	// removal), so the wiring can rebuild the filter from the registry.
	// Optional.
	OnIngestChange func()
	// Logger defaults to slog.Default when nil.
	Logger *slog.Logger
}

// NewWatchlistTools constructs the provider. Panics if Store is nil
// so misconfiguration is caught at wiring time rather than the first
// tool invocation.
func NewWatchlistTools(cfg WatchlistToolsConfig) *WatchlistTools {
	if cfg.Store == nil {
		panic("awareness: WatchlistTools requires a non-nil Store")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &WatchlistTools{
		store:          cfg.Store,
		registry:       cfg.Registry,
		onIngestChange: cfg.OnIngestChange,
		logger:         logger,
	}
}

// Name implements [tools.Provider].
func (w *WatchlistTools) Name() string { return "awareness.watchlist" }

// Tools implements [tools.Provider]. Returns the three watchlist
// management tools with handlers bound to w's store.
func (w *WatchlistTools) Tools() []*tools.Tool {
	return []*tools.Tool{
		{
			Name: "add_entity_subscription",
			Description: "Subscribe to a Home Assistant entity so its live state is injected into the model's context. " +
				"This tool adds always-visible subscriptions: the entity appears on every turn regardless of which loop or capability tags are active — this is how you, or a conversation, watch an entity in your own field of view. " +
				"For a specific named loop's view use update_entity_subscriptions; from inside a loop's own turn use watch_entity. " +
				"Optional tags carry lens-style classifiers on the subscription itself for future filtering; they no longer act as a scope binding. " +
				"Use ttl_seconds for subscriptions that should expire after a bounded task. Use history to include historical state snapshots at specific intervals. Use forecast for weather entities when future weather context is needed. " +
				"When Home Assistant is connected, a glob or area/label/floor target reports how many entities it currently matches (with a sample) — and flags a zero-member expansion, which almost always means a typo'd id or an empty group.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id": map[string]any{
						"type":        "string",
						"description": "What to subscribe to. Any of: a concrete entity ID (sensor.office_temperature); a glob (binary_sensor.*door*, *_temperature); or an organizational target — area:<area_id>, label:<label_id>, floor:<floor_id> (e.g. area:office) — which watches that group's current members, re-resolved live each turn so membership follows the home as devices move (capped per turn like globs). Use ha_registry_search to find area/label/floor IDs.",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional lens-style classifiers attached to the subscription. Not used to bind it to any loop — for that, use the loop-scoped tools.",
					},
					"history": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "Historical context windows in seconds. When set, watched-entity context includes a compact summary for each window. E.g., [600, 3600, 86400] adds recent summaries for 10min, 1hr, and 1day windows.",
					},
					"forecast": map[string]any{
						"type":        "string",
						"enum":        []string{"daily", "hourly", "twice_daily", "none"},
						"description": "For weather.* entities, fetch this Home Assistant weather.get_forecasts type each turn and include the compact forecast in watched-entity context. Use none to clear forecast fetching.",
					},
					"ttl_seconds": map[string]any{
						"type":        "integer",
						"description": "Optional expiration in seconds. After this TTL elapses, the subscription is automatically removed from future context injection.",
					},
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{"render", "ingest", "both"},
						"description": "What the subscription feeds. render (default): inject live state into context each turn. ingest: feed the recent-state-changes window only — the entity's transitions appear there without per-turn state injection. both: do both. ingest/both accept entity ids and globs, not area/label/floor targets.",
					},
					"include": tools.EntityMetadataIncludeParameter(),
				},
				"required": []string{"entity_id"},
			},
			Handler: w.handleAddEntitySubscription,
		},
		{
			Name:        "list_entity_subscriptions",
			Description: "List always-visible entity subscriptions used for live context injection — entities that are surfaced on every turn regardless of which loop or capability tags are active. When Home Assistant is connected, each glob or area/label/floor subscription carries an expansion object with its current member count and a sample, so a subscription that currently matches nothing is visible at a glance. For per-loop subscriptions, call loop_definition_get and read the spec's subscriptions field; effective inherited subscriptions are surfaced there too.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tag": map[string]any{
						"type":        "string",
						"description": "Optional lens-tag filter. Only matches subscriptions that carry the given lens tag.",
					},
					"entity_id": map[string]any{
						"type":        "string",
						"description": "Optional exact entity_id filter.",
					},
				},
			},
			Handler: w.handleListEntitySubscriptions,
		},
		{
			Name:        "remove_entity_subscription",
			Description: "Remove an always-visible entity subscription. Touches only always-on rows; per-loop subscriptions are not affected (use unwatch_entity inside the loop, or update_entity_subscriptions by name).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id": map[string]any{
						"type":        "string",
						"description": "The Home Assistant entity ID to unsubscribe from.",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional lens-tag filter — when present, only removes always-visible rows that also carry one of these tags.",
					},
				},
				"required": []string{"entity_id"},
			},
			Handler: w.handleRemoveEntitySubscription,
		},
	}
}

func (w *WatchlistTools) handleAddEntitySubscription(ctx context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}
	if err := homeassistant.ValidateEntityTarget(entityID); err != nil {
		return "", fmt.Errorf("entity_id %q: invalid glob pattern: %w", entityID, err)
	}

	tags, err := parseWatchlistTagArgs(args["tags"])
	if err != nil {
		return "", err
	}
	history, err := parseWatchlistHistoryArg(args["history"])
	if err != nil {
		return "", err
	}
	ttlSeconds, err := watchlistIntArg(args["ttl_seconds"], "ttl_seconds")
	if err != nil {
		return "", err
	}
	if ttlSeconds < 0 {
		return "", fmt.Errorf("ttl_seconds must be >= 0")
	}
	rawForecast, forecastSet := args["forecast"]
	forecast, err := parseWatchlistForecastArg(rawForecast)
	if err != nil {
		return "", err
	}
	if forecast != "" && !strings.HasPrefix(entityID, "weather.") {
		return "", fmt.Errorf("forecast can only be set for weather.* entities; got %s", entityID)
	}
	include, err := tools.ParseEntityMetadataIncludesArg(args["include"], "include")
	if err != nil {
		return "", err
	}
	rawMode, _ := args["mode"].(string)
	mode := normalizeSubscriptionMode(rawMode)
	if mode == "" {
		return "", fmt.Errorf("mode must be one of render, ingest, or both; got %q", rawMode)
	}
	if mode != SubscriptionModeRender {
		// The ingestion filter speaks the EntityFilter's native
		// vocabulary: concrete ids and globs. Registry targets would be
		// silently approximated, so they are rejected outright (#1192).
		if ParseSubscriptionTarget(entityID).IsRegistryTarget() {
			return "", fmt.Errorf("mode %q accepts entity ids and globs only — area/label/floor targets cannot feed the ingestion filter; subscribe the target with mode render, or list its member entities", mode)
		}
		// The ingestion filter reads only always-visible rows; a
		// tag-scoped ingest subscription would sit in the store doing
		// nothing. Reject rather than silently no-op.
		if len(tags) > 0 {
			return "", fmt.Errorf("mode %q subscriptions are always-visible and cannot carry tags — drop the tags, or use mode render for a tag-scoped subscription", mode)
		}
		globs, err := w.store.IngestGlobs(time.Now())
		if err != nil {
			return "", fmt.Errorf("count ingest entries: %w", err)
		}
		// Re-adding an existing entry updates it in place and doesn't
		// grow the registry; the cap gates only genuinely new entries.
		exists := false
		for _, g := range globs {
			if g == entityID {
				exists = true
				break
			}
		}
		if !exists && len(globs) >= maxIngestEntries {
			return "", fmt.Errorf("ingest registry is at its cap (%d entries) — remove entries before adding more; a broad glob covers more for less", maxIngestEntries)
		}
	}

	if mode == SubscriptionModeRender && len(tags) == 0 && len(history) == 0 && ttlSeconds == 0 && forecast == "" && !forecastSet && !include.Any() {
		if err := w.store.Add(entityID); err != nil {
			return "", fmt.Errorf("add to watchlist: %w", err)
		}
	} else {
		if err := w.store.AddWithOptions(entityID, tags, history, ttlSeconds, forecast, mode, include); err != nil {
			return "", fmt.Errorf("add to watchlist: %w", err)
		}
	}
	if mode != SubscriptionModeRender && w.onIngestChange != nil {
		w.onIngestChange()
	}

	msg := fmt.Sprintf("Now watching %s", entityID)
	switch mode {
	case SubscriptionModeIngest:
		msg += " (mode: ingest — transitions feed the recent-state-changes window; no per-turn state render)"
	case SubscriptionModeBoth:
		msg += " (mode: both — transitions feed the recent-state-changes window and live state renders each turn)"
	}
	if len(tags) > 0 {
		msg += fmt.Sprintf(" (scoped to tags: %v)", tags)
	}
	if len(history) > 0 {
		parts := make([]string, len(history))
		for i, h := range history {
			parts[i] = fmt.Sprintf("%ds", h)
		}
		msg += fmt.Sprintf(" (history windows: %s)", strings.Join(parts, ", "))
	}
	if forecast != "" {
		msg += fmt.Sprintf(" (forecast: %s)", forecast)
	} else if forecastSet {
		msg += " (forecast: none)"
	}
	if ttlSeconds > 0 {
		msg += fmt.Sprintf(" (expires in %ds)", ttlSeconds)
	}
	if include.Any() {
		msg += " (includes HA metadata)"
	}
	msg += "."

	// Author-time expansion preview: for a glob or area/label/floor
	// target, report what it matches right now so the model isn't left
	// guessing — and a zero-member expansion is flagged loudly, since it
	// is almost always a typo'd id that would otherwise inject nothing
	// forever. A concrete entity_id is its own membership and needs none.
	if target := ParseSubscriptionTarget(entityID); target.Kind != TargetEntity {
		exp, perr := previewTargetExpansion(newRenderRegistries(ctx, w.registry), target)
		switch {
		case perr != nil:
			// The preview couldn't run (transient registry read failure).
			// Say so rather than returning a bare "Now watching …" — an
			// unspoken failure would read as a clean, validated subscribe,
			// the exact silent-accept this preview exists to prevent.
			w.logger.Warn("subscription target expansion preview failed",
				"entity_id", entityID, "error", perr)
			msg += " Note: couldn't preview its current expansion this turn" +
				" (registry read failed), so the member count is unverified."
		case exp == nil:
			// No registry client wired — subscription stands without a preview.
		case exp.Count == 0:
			msg += " Note: this target matches no entities right now —" +
				" check the id (a likely typo or an empty group); it will" +
				" inject nothing until it has members."
		default:
			msg += fmt.Sprintf(" Currently matches %d %s: %s%s.",
				exp.Count, entityNoun(exp.Count), strings.Join(exp.Sample, ", "),
				moreMembersSuffix(exp.Count, len(exp.Sample)))
		}
	}

	w.logger.Info("entity subscription added",
		"entity_id", entityID, "tags", tags, "history", history, "forecast", forecast, "include", include, "ttl_seconds", ttlSeconds)
	return msg, nil
}

func (w *WatchlistTools) handleListEntitySubscriptions(ctx context.Context, args map[string]any) (string, error) {
	tag, _ := args["tag"].(string)
	entityID, _ := args["entity_id"].(string)

	subs, err := w.store.ListSubscriptions(strings.TrimSpace(tag))
	if err != nil {
		return "", fmt.Errorf("list watchlist subscriptions: %w", err)
	}

	now := time.Now()
	// One registries bundle shared across the whole list: it fetches each
	// registry once and every glob/registry target's expansion reuses it.
	registries := newRenderRegistries(ctx, w.registry)
	items := make([]map[string]any, 0, len(subs))
	for _, sub := range subs {
		if entityID != "" && sub.EntityID != entityID {
			continue
		}
		item := map[string]any{
			"entity_id":      sub.EntityID,
			"scope":          sub.Scope,
			"mode":           sub.Mode,
			"always_visible": sub.Scope == "",
		}
		if len(sub.History) > 0 {
			item["history"] = append([]int(nil), sub.History...)
		}
		if sub.Forecast != "" {
			item["forecast"] = sub.Forecast
		}
		if sub.Include != nil && sub.Include.Any() {
			item["include"] = sub.Include
		}
		if sub.ExpiresAt != nil {
			item["expires_delta"] = promptfmt.FormatDeltaOnly(*sub.ExpiresAt, now)
		}
		// Show each glob/registry target's current expansion so a
		// silently-empty subscription is visible at a glance.
		if target := ParseSubscriptionTarget(sub.EntityID); target.Kind != TargetEntity {
			if exp, err := previewTargetExpansion(registries, target); err != nil {
				// A failed read is marked, not omitted — an absent
				// expansion would read as "not a registry target" rather
				// than "couldn't resolve it this turn."
				w.logger.Warn("subscription target expansion preview failed",
					"entity_id", sub.EntityID, "error", err)
				item["expansion"] = map[string]any{
					"unavailable": true,
					"note":        "registry read failed this turn; membership unverified",
				}
			} else if exp != nil {
				expObj := map[string]any{"count": exp.Count}
				if len(exp.Sample) > 0 {
					expObj["sample"] = exp.Sample
				}
				if exp.Count == 0 {
					expObj["note"] = "matches no entities right now — likely a typo'd id or an empty group"
				}
				item["expansion"] = expObj
			}
		}
		items = append(items, item)
	}

	payload, err := json.Marshal(map[string]any{
		"count": len(items),
		"items": items,
	})
	if err != nil {
		return "", fmt.Errorf("marshal watchlist subscriptions: %w", err)
	}
	return string(payload), nil
}

func (w *WatchlistTools) handleRemoveEntitySubscription(_ context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	tags, err := parseWatchlistTagArgs(args["tags"])
	if err != nil {
		return "", err
	}

	if len(tags) > 0 {
		err = w.store.RemoveWithScopes(entityID, tags)
	} else {
		err = w.store.Remove(entityID)
	}
	if err != nil {
		return "", fmt.Errorf("remove from watchlist: %w", err)
	}

	if w.onIngestChange != nil {
		w.onIngestChange()
	}
	w.logger.Info("entity subscription removed", "entity_id", entityID, "tags", tags)
	if len(tags) > 0 {
		return fmt.Sprintf("Stopped watching %s in scopes %v.", entityID, tags), nil
	}
	return fmt.Sprintf("Stopped watching %s.", entityID), nil
}

func parseWatchlistTagArgs(raw any) ([]string, error) {
	rawTags, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	var tags []string
	seen := make(map[string]bool)
	for _, rt := range rawTags {
		s, ok := rt.(string)
		if !ok || s == "" {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(s, ",") {
			return nil, fmt.Errorf("tag %q must not contain commas", s)
		}
		if !seen[s] {
			seen[s] = true
			tags = append(tags, s)
		}
	}
	return tags, nil
}

func parseWatchlistForecastArg(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("forecast must be a string")
	}
	return normalizeForecastType(value)
}

func parseWatchlistHistoryArg(raw any) ([]int, error) {
	rawHist, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	var history []int
	for i, rh := range rawHist {
		switch v := rh.(type) {
		case float64:
			if v != float64(int(v)) {
				return nil, fmt.Errorf("history[%d]: must be a whole number of seconds, got %v", i, v)
			}
			iv := int(v)
			if iv <= 0 {
				return nil, fmt.Errorf("history[%d]: must be positive, got %d", i, iv)
			}
			history = append(history, iv)
		case int:
			if v <= 0 {
				return nil, fmt.Errorf("history[%d]: must be positive, got %d", i, v)
			}
			history = append(history, v)
		default:
			return nil, fmt.Errorf("history[%d]: expected integer seconds, got %T", i, rh)
		}
	}
	return history, nil
}

func watchlistIntArg(raw any, field string) (int, error) {
	switch v := raw.(type) {
	case nil:
		return 0, nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("%s must be a whole number, got %v", field, v)
		}
		return int(v), nil
	case int:
		return v, nil
	default:
		return 0, fmt.Errorf("%s must be an integer, got %T", field, raw)
	}
}

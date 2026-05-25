package app

import (
	"context"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// hydrateLoopFocusTools generates the in-loop entity-subscription
// mutation tools — watch_entity and unwatch_entity — whenever a spec
// carries a scope_tag in Metadata. The scope tag is captured in the
// closure at hydration time, so the model running an iteration never
// sees or types it; the tool surface is the inside-the-loop cognitive
// frame ("watch this entity") rather than the outside-the-loop one
// ("update loop X's subscriptions").
//
// No-op when the spec has no scope tag or when the watchlist store is
// not configured. Errors only when the store is missing AND the spec
// declares a scope tag (a misconfiguration worth surfacing).
// [looppkg.SpecScopeTag] handles the read fallback to the legacy
// "focus_tag" key for specs persisted before the rename.
func (a *App) hydrateLoopFocusTools(spec looppkg.Spec) (looppkg.Spec, error) {
	scopeTag := looppkg.SpecScopeTag(spec)
	if scopeTag == "" {
		return spec, nil
	}
	if a == nil || a.watchlistStore == nil {
		return looppkg.Spec{}, fmt.Errorf("loop %q declares scope_tag %q but the entity watchlist store is not configured", spec.Name, scopeTag)
	}
	spec.RuntimeTools = append(spec.RuntimeTools, buildLoopFocusTools(a.watchlistStore, scopeTag)...)
	return spec, nil
}

// loopFocusWatchStore is the narrow store contract these runtime tools
// need. Matches the methods on awareness.WatchlistStore.
type loopFocusWatchStore interface {
	AddWithOptions(entityID string, tags []string, history []int, ttlSeconds int, forecast string) error
	RemoveWithScopes(entityID string, scopes []string) error
}

func buildLoopFocusTools(store loopFocusWatchStore, scopeTag string) []looppkg.RuntimeTool {
	if store == nil || scopeTag == "" {
		return nil
	}
	return []looppkg.RuntimeTool{
		{
			Name:               "watch_entity",
			Description:        "Add a Home Assistant entity to this loop's watched set. Its live state is injected into your context every iteration. Use history for compact rolling-window summaries, forecast for weather entities, ttl_seconds for time-bounded watches. The subscription is scoped to this loop only — other loops and conversations are unaffected.",
			SkipContentResolve: true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id": map[string]any{
						"type":        "string",
						"description": "The Home Assistant entity ID to watch (e.g., sensor.upstairs_temperature, weather.home).",
					},
					"history": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "Optional historical context windows in seconds (e.g., [3600, 86400] for 1h and 1d summaries).",
					},
					"forecast": map[string]any{
						"type":        "string",
						"enum":        []string{"daily", "hourly", "twice_daily", "none"},
						"description": "For weather.* entities, the HA forecast type to fetch each turn.",
					},
					"ttl_seconds": map[string]any{
						"type":        "integer",
						"description": "Optional auto-expiration in seconds. Use for bounded research tasks where the watch should fall off automatically.",
					},
				},
				"required": []string{"entity_id"},
			},
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				entityID := strings.TrimSpace(stringMapValue(args, "entity_id"))
				if entityID == "" {
					return "", fmt.Errorf("entity_id is required")
				}
				history, err := intSliceFromMap(args, "history")
				if err != nil {
					return "", fmt.Errorf("history: %w", err)
				}
				ttlSeconds, err := intFromMap(args, "ttl_seconds")
				if err != nil {
					return "", fmt.Errorf("ttl_seconds: %w", err)
				}
				if ttlSeconds < 0 {
					return "", fmt.Errorf("ttl_seconds must be >= 0")
				}
				forecast := strings.TrimSpace(stringMapValue(args, "forecast"))
				if err := store.AddWithOptions(entityID, []string{scopeTag}, history, ttlSeconds, forecast); err != nil {
					return "", err
				}
				return fmt.Sprintf(`{"status":"ok","entity_id":%q}`, entityID), nil
			},
		},
		{
			Name:               "unwatch_entity",
			Description:        "Remove an entity from this loop's watched set. Removes only this loop's scoped subscription; if the same entity is also tagged elsewhere (another loop, an always-on watch), those rows are left alone.",
			SkipContentResolve: true,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id": map[string]any{
						"type":        "string",
						"description": "The Home Assistant entity ID to stop watching.",
					},
				},
				"required": []string{"entity_id"},
			},
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				entityID := strings.TrimSpace(stringMapValue(args, "entity_id"))
				if entityID == "" {
					return "", fmt.Errorf("entity_id is required")
				}
				if err := store.RemoveWithScopes(entityID, []string{scopeTag}); err != nil {
					return "", err
				}
				return fmt.Sprintf(`{"status":"ok","entity_id":%q}`, entityID), nil
			},
		},
	}
}

// stringMapValue safely reads a string field from a map[string]any.
func stringMapValue(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// intFromMap accepts the same flavors of integer JSON encoding the
// thane_curate handler does (int, int64, integral float64) and rejects
// fractional float values rather than truncating them silently.
func intFromMap(m map[string]any, key string) (int, error) {
	raw, present := m[key]
	if !present || raw == nil {
		return 0, nil
	}
	switch n := raw.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("must be an integer, got fractional value %v", n)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("must be an integer, got %T", raw)
	}
}

func intSliceFromMap(m map[string]any, key string) ([]int, error) {
	raw, present := m[key]
	if !present || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("must be an array of integers")
	}
	out := make([]int, 0, len(list))
	for i, v := range list {
		n, err := intFromMap(map[string]any{"_": v}, "_")
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("[%d]: window seconds must be > 0", i)
		}
		out = append(out, n)
	}
	return out, nil
}

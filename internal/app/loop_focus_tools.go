package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// hydrateLoopFocusTools generates the in-loop entity-subscription
// mutation tools — watch_entity and unwatch_entity — for every spec.
// The loop's own name is captured in the closure at hydration time,
// so the model running an iteration never sees or types it; the tool
// surface is the inside-the-loop cognitive frame ("watch this
// entity") rather than the outside-the-loop one ("update loop X's
// subscriptions").
//
// Mutations write through to the persisted spec via the definition
// registry and reflect immediately on the live loop, so the next
// iteration sees the change and a subsequent restart preserves it.
func (a *App) hydrateLoopFocusTools(spec looppkg.Spec) (looppkg.Spec, error) {
	if a == nil || a.loopDefinitionRegistry == nil {
		return spec, nil
	}
	if strings.TrimSpace(spec.Name) == "" {
		return spec, nil
	}
	spec.RuntimeTools = append(spec.RuntimeTools, a.buildLoopFocusTools(spec.Name)...)
	return spec, nil
}

// subscriptionMutator is the narrow contract the in-loop entity tools
// need from the surrounding runtime: take the loop's name and a
// transformer for its current subscriptions, return the new effective
// list. The app-level implementation persists + propagates to the live
// loop; tests pass a thin in-memory variant so the tool surface can be
// exercised without standing up a full app.
type subscriptionMutator func(ctx context.Context, loopName string, mutate func([]looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error)) ([]looppkg.EntitySubscription, error)

func (a *App) buildLoopFocusTools(loopName string) []looppkg.RuntimeTool {
	return buildLoopFocusToolsWithMutator(loopName, a.mutateLoopSubscriptions)
}

func buildLoopFocusToolsWithMutator(loopName string, mutator subscriptionMutator) []looppkg.RuntimeTool {
	return []looppkg.RuntimeTool{
		{
			Name:               "watch_entity",
			Description:        "Add a Home Assistant entity to this loop's watched set. Its live state is injected into your context every iteration. Use history for compact rolling-window summaries, forecast for weather entities, ttl_seconds for time-bounded watches. The subscription is scoped to this loop only — other loops and conversations are unaffected. Changes persist across restart.",
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
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
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
				now := time.Now().UTC()
				_, err = mutator(ctx, loopName, func(current []looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error) {
					return upsertSubscription(current, looppkg.EntitySubscription{
						EntityID:   entityID,
						History:    history,
						Forecast:   forecast,
						TTLSeconds: ttlSeconds,
						AddedAt:    now,
					}), nil
				})
				if err != nil {
					return "", err
				}
				return fmt.Sprintf(`{"status":"ok","entity_id":%q}`, entityID), nil
			},
		},
		{
			Name:               "unwatch_entity",
			Description:        "Remove an entity from this loop's watched set. Removes only this loop's own subscription; inherited subscriptions from container ancestors are left alone (override locally by re-declaring with new options if needed).",
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
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				entityID := strings.TrimSpace(stringMapValue(args, "entity_id"))
				if entityID == "" {
					return "", fmt.Errorf("entity_id is required")
				}
				_, err := mutator(ctx, loopName, func(current []looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error) {
					return dropSubscription(current, entityID), nil
				})
				if err != nil {
					return "", err
				}
				return fmt.Sprintf(`{"status":"ok","entity_id":%q}`, entityID), nil
			},
		},
	}
}

// mutateLoopSubscriptions is the app-side counterpart to the tools-
// package helper of the same shape. Both routes converge on the same
// persistence model: read the persisted spec, apply the mutation,
// write back, and patch the live loop. Two call sites exist because
// hydration runs in the app package (which has the definition
// registry handle directly) and the model-facing tool-family handlers
// live in the tools package, and Go's package layering forbids the
// cycle that a single helper would create.
func (a *App) mutateLoopSubscriptions(ctx context.Context, loopName string, mutate func([]looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error)) ([]looppkg.EntitySubscription, error) {
	if a == nil || a.loopDefinitionRegistry == nil {
		return nil, fmt.Errorf("loop definition registry not configured")
	}
	snap := a.loopDefinitionRegistry.Snapshot()
	var existing looppkg.DefinitionSnapshot
	found := false
	if snap != nil {
		for _, def := range snap.Definitions {
			if def.Name == loopName {
				existing = def
				found = true
				break
			}
		}
	}
	if !found {
		return nil, &looppkg.UnknownDefinitionError{Name: loopName}
	}
	if existing.Source == looppkg.DefinitionSourceConfig {
		return nil, &looppkg.ImmutableDefinitionError{Name: loopName}
	}

	next, err := mutate(existing.Spec.Subscriptions)
	if err != nil {
		return nil, err
	}

	newSpec := existing.Spec
	newSpec.Subscriptions = next
	updatedAt := time.Now().UTC()
	if err := a.persistLoopDefinition(newSpec, updatedAt); err != nil {
		return nil, fmt.Errorf("persist loop definition: %w", err)
	}
	if err := a.loopDefinitionRegistry.Upsert(newSpec, updatedAt); err != nil {
		return nil, err
	}
	if a.loopRegistry != nil {
		if live := a.loopRegistry.GetByName(loopName); live != nil {
			live.SetSubscriptions(next)
		}
	}
	_ = ctx
	return next, nil
}

// upsertSubscription replaces any existing entry for sub.EntityID
// with the new one (preserving caller-supplied options exactly) and
// appends fresh ones at the end. Used by watch_entity so calling it
// twice for the same entity replaces options rather than duplicating.
func upsertSubscription(current []looppkg.EntitySubscription, sub looppkg.EntitySubscription) []looppkg.EntitySubscription {
	out := make([]looppkg.EntitySubscription, 0, len(current)+1)
	for _, c := range current {
		if c.EntityID == sub.EntityID {
			continue
		}
		out = append(out, c)
	}
	out = append(out, sub)
	return out
}

// dropSubscription returns the current list minus the entry matching
// entityID. Missing IDs are silently a no-op; tools can surface
// "not watched" themselves if they want louder behavior.
func dropSubscription(current []looppkg.EntitySubscription, entityID string) []looppkg.EntitySubscription {
	out := make([]looppkg.EntitySubscription, 0, len(current))
	for _, c := range current {
		if c.EntityID == entityID {
			continue
		}
		out = append(out, c)
	}
	return out
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

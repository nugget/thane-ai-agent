package tools

import (
	"context"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// registerLoopUpdateEntitySubscriptions wires the external CRUD tool
// for adjusting a named loop's entity-subscription watch set
// post-launch. Loop creation already accepts an entities parameter via
// thane_curate; this tool is for the model managing an existing,
// running loop without re-launching it.
//
// The internal counterpart — watch_entity / unwatch_entity hydrated
// as runtime tools alongside the loop's scoped output tools — lives in
// internal/app/loop_focus_tools.go. That surface targets the model
// running an iteration and has no name parameter (the scope tag is
// baked in at hydration). Two surfaces, two cognitive frames.
func (r *Registry) registerLoopUpdateEntitySubscriptions() {
	r.Register(&Tool{
		Name: "loop_update_entity_subscriptions",
		Description: "Adjust an existing loop's entity-subscription watch set by name. " +
			"Use this from outside a loop (a conversation, a supervisor, a peer loop) when the model already has a target loop in mind. " +
			"From inside a running loop's own iteration, prefer the scoped watch_entity / unwatch_entity tools surfaced on that loop's tool list — those don't require the loop name. " +
			"Both add and remove are optional and may be combined in one call; at least one must contain entries. " +
			"Add items use the same shape as the entities parameter on thane_curate (entity_id with optional history, forecast, ttl_seconds). " +
			"Remove items are bare entity_id strings. Removes are applied before adds, so re-adding the same entity with new options is a single round-trip. " +
			"Returns the loop's scope_tag and the counts of added and removed subscriptions.",
		ContentResolveExempt: []string{"name", "add", "remove"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Loop definition name to operate on. The loop must have been created with thane_curate (or otherwise carry a scope_tag in Spec.Metadata; legacy persisted definitions with the older focus_tag key are accepted during the rename fallback window).",
				},
				"add": map[string]any{
					"type":        "array",
					"description": "Entity subscriptions to add. Same shape as thane_curate's entities parameter.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"entity_id": map[string]any{
								"type":        "string",
								"description": "The Home Assistant entity ID to watch.",
							},
							"history": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "integer"},
								"description": "Optional historical context windows in seconds.",
							},
							"forecast": map[string]any{
								"type":        "string",
								"enum":        []string{"daily", "hourly", "twice_daily", "none"},
								"description": "For weather.* entities, fetch this HA forecast type each turn.",
							},
							"ttl_seconds": map[string]any{
								"type":        "integer",
								"description": "Optional auto-expiration in seconds.",
							},
						},
						"required": []string{"entity_id"},
					},
				},
				"remove": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Entity IDs to unsubscribe from this loop. Removes only the row scoped to this loop's scope_tag; other scopes for the same entity are left alone.",
				},
			},
			"required": []string{"name"},
		},
		Handler: r.handleLoopUpdateEntitySubscriptions,
	})
}

func (r *Registry) handleLoopUpdateEntitySubscriptions(_ context.Context, args map[string]any) (string, error) {
	deps := r.loopIntentDeps
	if deps.Registry == nil || deps.WatchlistStore == nil {
		return "", fmt.Errorf("loop_update_entity_subscriptions not configured: requires loop registry and watchlist store")
	}

	name := strings.TrimSpace(ldStringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	snap := deps.Registry.Snapshot()
	existing, ok := findLoopDefinition(snap, name)
	if !ok {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}
	scopeTag := looppkg.SpecScopeTag(existing.Spec)
	if scopeTag == "" {
		return "", fmt.Errorf("loop %q has no scope_tag; only loops created with thane_curate (or another tool that mints a scope_tag) support entity subscriptions", name)
	}

	addList, err := parseEntityList("add", args["add"])
	if err != nil {
		return "", err
	}
	removeList, err := parseRemoveEntityIDs(args["remove"])
	if err != nil {
		return "", err
	}
	if len(addList) == 0 && len(removeList) == 0 {
		return "", fmt.Errorf("at least one of add or remove must contain entries")
	}

	// Removes first so re-adding the same entity with different options
	// (e.g. swap history windows) is a single round-trip and lands as
	// an insert rather than a no-op upsert with stale state.
	for _, eid := range removeList {
		if err := deps.WatchlistStore.RemoveWithScopes(eid, []string{scopeTag}); err != nil {
			return "", fmt.Errorf("remove %q: %w", eid, err)
		}
	}
	for _, e := range addList {
		if err := deps.WatchlistStore.AddWithOptions(e.EntityID, []string{scopeTag}, e.History, e.TTLSeconds, e.Forecast); err != nil {
			return "", fmt.Errorf("add %q: %w", e.EntityID, err)
		}
	}

	return ldMarshalToolJSON(map[string]any{
		"status":               "ok",
		"loop_definition_name": name,
		"scope_tag":            scopeTag,
		"added":                len(addList),
		"removed":              len(removeList),
	})
}

// parseRemoveEntityIDs validates the remove[] parameter shape and
// rejects duplicates so a model that lists the same entity twice
// learns of it rather than silently double-deleting.
func parseRemoveEntityIDs(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("remove must be an array of entity_id strings")
	}
	if len(list) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("remove[%d]: must be a string entity_id, got %T", i, item)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("remove[%d]: entity_id is required", i)
		}
		if seen[s] {
			return nil, fmt.Errorf("remove[%d] duplicates entity_id %q; each entity may appear at most once", i, s)
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

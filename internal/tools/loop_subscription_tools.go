package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// registerUpdateEntitySubscriptions wires the external CRUD tool for
// adjusting a named loop's entity-subscription set post-launch. Loop
// creation already accepts an entities parameter via thane_loop_create;
// this tool is for the model managing an existing loop without
// re-launching it. Changes write through to the persisted spec and
// land on the live loop on the next iteration.
//
// The internal counterpart — watch_entity / unwatch_entity hydrated
// as runtime tools on every running loop — lives in
// internal/app/loop_focus_tools.go. That surface targets the model
// running an iteration and has no name parameter (the loop name is
// baked in at hydration). Two surfaces, two cognitive frames.
func (r *Registry) registerUpdateEntitySubscriptions() {
	r.Register(&Tool{
		Name: "update_entity_subscriptions",
		Description: "Add or remove Home Assistant entities from a specific named loop's subscription set. " +
			"Use this to make a peer loop start (or stop) seeing specific entities in its context every iteration — for example, when the core loop learns that a curate loop should also watch a newly-relevant sensor. " +
			"Scope: this targets a NAMED loop only. To watch an entity in your own always-visible context (what a conversation uses for itself), use add_entity_subscription instead — a conversation is not a loop and cannot be named here. " +
			"From inside the running loop's own iteration, prefer the scoped watch_entity / unwatch_entity tools surfaced on that loop's tool list; those don't need the loop name because it is baked in. " +
			"Both add and remove are optional and may be combined in one call; at least one must carry entries. Removes are applied before adds, so re-adding the same entity with new options is a single round-trip. " +
			"Add items mirror thane_loop_create.entities (entity_id with optional history, forecast, ttl_seconds). Remove items are bare entity_id strings. " +
			"To inspect what a loop currently watches, call loop_definition_get on its name — Spec.Subscriptions is the source of truth, plus any inherited entries from container ancestors. " +
			"Returns counts of added and removed entries and the resulting subscription_count.",
		ContentResolveExempt: []string{"name", "add", "remove"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Loop definition name to operate on. Any overlay-source loop is eligible — the structural Spec.Subscriptions list is updated directly.",
				},
				"add": map[string]any{
					"type":        "array",
					"description": "Entity subscriptions to add. Same shape as thane_loop_create's entities parameter.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"entity_id": map[string]any{
								"type":        "string",
								"description": "The Home Assistant entity ID to watch, or a glob pattern (e.g., binary_sensor.*door*) to watch every matching entity, re-expanded live each turn (capped per turn).",
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
							"include": EntityMetadataIncludeParameter(),
						},
						"required": []string{"entity_id"},
					},
				},
				"remove": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Entity IDs to unsubscribe from this loop. Removes only the loop's own entries; inherited subscriptions from container ancestors are left alone (override locally by re-declaring with new options if needed).",
				},
			},
			"required": []string{"name"},
		},
		Handler: r.handleUpdateEntitySubscriptions,
	})
}

func (r *Registry) handleUpdateEntitySubscriptions(ctx context.Context, args map[string]any) (string, error) {
	deps := r.loopIntentDeps
	if deps.Registry == nil {
		return "", fmt.Errorf("update_entity_subscriptions not configured: requires loop definition registry")
	}

	name := toolargs.TrimmedString(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
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

	next, err := r.mutateLoopSubscriptions(ctx, name, func(current []looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error) {
		return applySubscriptionDelta(current, addList, removeList, time.Now().UTC()), nil
	})
	if err != nil {
		// Teach the next move (docs/model-facing-tools.md §4): the most
		// common miss is aiming this loop-scoped tool at a conversation
		// (passing a conversation/session id as name). Point at the
		// always-visible tool for own-context watches and the loop lister
		// for targeting a real loop, instead of a bare "unknown definition".
		var unknown *looppkg.UnknownDefinitionError
		if errors.As(err, &unknown) {
			// Wrap the source error with %w so the causal chain survives for
			// any future errors.As/Is, while the message still teaches the
			// next move. The wrapped error leads — it already names the loop
			// (`loop: unknown definition "X"`) — so the guidance follows
			// without duplicating it.
			return "", fmt.Errorf("%w; update_entity_subscriptions targets a specific named loop's watch set, not your own context (a conversation is not a loop); for an always-visible subscription that follows you every turn use add_entity_subscription, or pick a real loop name from loop_definition_list", err)
		}
		return "", err
	}

	return ldMarshalToolJSON(map[string]any{
		"status":               "ok",
		"loop_definition_name": name,
		"added":                len(addList),
		"removed":              len(removeList),
		"subscription_count":   len(next),
	})
}

// applySubscriptionDelta returns the updated subscription list after
// removing every entry in removeIDs and (re-)adding every entry in
// addList. Removes run first so re-adding the same entity with new
// options lands as a fresh insert rather than colliding with the
// stale row. Existing subscriptions for an entity in addList are
// replaced wholesale by the new options; previously-set fields not
// repeated in the add carry no precedence. addedAt is stamped on
// every newly-added entry so TTL countdown starts from the mutation.
func applySubscriptionDelta(current []looppkg.EntitySubscription, addList []curateEntity, removeIDs []string, addedAt time.Time) []looppkg.EntitySubscription {
	skip := make(map[string]struct{}, len(removeIDs)+len(addList))
	for _, eid := range removeIDs {
		skip[eid] = struct{}{}
	}
	for _, e := range addList {
		skip[e.EntityID] = struct{}{}
	}
	kept := make([]looppkg.EntitySubscription, 0, len(current))
	for _, sub := range current {
		if _, drop := skip[sub.EntityID]; drop {
			continue
		}
		kept = append(kept, sub)
	}
	for _, e := range addList {
		kept = append(kept, looppkg.EntitySubscription{
			EntityID:   e.EntityID,
			History:    append([]int(nil), e.History...),
			Forecast:   e.Forecast,
			Include:    EntityMetadataIncludesPointer(e.Include),
			TTLSeconds: e.TTLSeconds,
			AddedAt:    addedAt,
		})
	}
	return kept
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

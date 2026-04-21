package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/awareness"
)

// SetWatchlistStore adds the add_context_entity, list_context_entities, and
// remove_context_entity tools to the registry.
func (r *Registry) SetWatchlistStore(store *awareness.WatchlistStore) {
	r.watchlistStore = store
	r.registerWatchlistTools()
}

// OnWatchlistTagAdded configures a callback that is invoked whenever new
// scoped entity subscriptions introduce tags that need live context providers.
func (r *Registry) OnWatchlistTagAdded(fn func(tag string)) {
	r.watchlistTagRegistrar = fn
}

func (r *Registry) registerWatchlistTools() {
	if r.watchlistStore == nil {
		return
	}

	r.Register(&Tool{
		Name: "add_context_entity",
		Description: "Add a Home Assistant entity to the watched list. " +
			"Watched entities have their live state injected into your context every turn, " +
			"eliminating the need for repeated get_state calls. " +
			"Rich domains (weather, climate, light, person) automatically include relevant attributes. " +
			"Use tags to scope entity context to specific capabilities or loop-owned focus tags (only visible when that tag is active). " +
			"Subscriptions are additive: the same entity can appear in multiple scoped contexts. " +
			"Use ttl_seconds when the watch should expire automatically after a bounded task ends. " +
			"Use history to include historical state snapshots at specific intervals.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entity_id": map[string]any{
					"type":        "string",
					"description": "The Home Assistant entity ID to watch (e.g., sensor.office_temperature, weather.home)",
				},
				"tags": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Capability tags to scope this entity to. When set, the entity's context only appears when one of these tags is active. Omit for always-visible entities.",
				},
				"history": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "integer"},
					"description": "Historical context windows in seconds. When set, watched-entity context includes a compact summary for each window. E.g., [600, 3600, 86400] adds recent summaries for 10min, 1hr, and 1day windows.",
				},
				"ttl_seconds": map[string]any{
					"type":        "integer",
					"description": "Optional expiration in seconds. After this TTL elapses, the subscription is automatically removed from future context injection.",
				},
			},
			"required": []string{"entity_id"},
		},
		Handler: r.handleAddContextEntity,
	})

	r.Register(&Tool{
		Name:        "list_context_entities",
		Description: "List watched entity subscriptions used for live context injection. Returns one row per subscription scope so you can inspect always-on entities, tag-scoped entities, TTLs, and stored history options before changing them.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag": map[string]any{
					"type":        "string",
					"description": "Optional scope tag to filter by. Omit to list all active subscriptions.",
				},
				"entity_id": map[string]any{
					"type":        "string",
					"description": "Optional exact entity_id filter.",
				},
			},
		},
		Handler: r.handleListContextEntities,
	})

	r.Register(&Tool{
		Name: "remove_context_entity",
		Description: "Remove a Home Assistant entity from the watched list. " +
			"By default this removes every subscription for the entity. " +
			"Use tags to remove only specific scoped subscriptions while leaving other loops or always-on contexts intact.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entity_id": map[string]any{
					"type":        "string",
					"description": "The Home Assistant entity ID to stop watching",
				},
				"tags": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional scope tags to remove. Omit to remove every subscription for this entity.",
				},
			},
			"required": []string{"entity_id"},
		},
		Handler: r.handleRemoveContextEntity,
	})
}

func (r *Registry) handleAddContextEntity(_ context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	tags, err := parseTagArgs(args["tags"])
	if err != nil {
		return "", err
	}
	history, err := parseHistoryArg(args["history"])
	if err != nil {
		return "", err
	}
	ttlSeconds, err := intArg(args["ttl_seconds"], "ttl_seconds")
	if err != nil {
		return "", err
	}
	if ttlSeconds < 0 {
		return "", fmt.Errorf("ttl_seconds must be >= 0")
	}

	if len(tags) > 0 || len(history) > 0 || ttlSeconds > 0 {
		if err := r.watchlistStore.AddWithOptions(entityID, tags, history, ttlSeconds); err != nil {
			return "", fmt.Errorf("add to watchlist: %w", err)
		}
	} else {
		if err := r.watchlistStore.Add(entityID); err != nil {
			return "", fmt.Errorf("add to watchlist: %w", err)
		}
	}

	msg := fmt.Sprintf("Now watching %s", entityID)
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
	if ttlSeconds > 0 {
		msg += fmt.Sprintf(" (expires in %ds)", ttlSeconds)
	}
	msg += "."

	slog.Info("context entity added",
		"entity_id", entityID, "tags", tags, "history", history, "ttl_seconds", ttlSeconds)
	if r.watchlistTagRegistrar != nil {
		for _, tag := range tags {
			r.watchlistTagRegistrar(tag)
		}
	}
	return msg, nil
}

func (r *Registry) handleListContextEntities(_ context.Context, args map[string]any) (string, error) {
	tag, _ := args["tag"].(string)
	entityID, _ := args["entity_id"].(string)

	subs, err := r.watchlistStore.ListSubscriptions(strings.TrimSpace(tag))
	if err != nil {
		return "", fmt.Errorf("list watchlist subscriptions: %w", err)
	}

	items := make([]map[string]any, 0, len(subs))
	for _, sub := range subs {
		if entityID != "" && sub.EntityID != entityID {
			continue
		}
		item := map[string]any{
			"entity_id":      sub.EntityID,
			"scope":          sub.Scope,
			"always_visible": sub.Scope == "",
		}
		if len(sub.History) > 0 {
			item["history"] = append([]int(nil), sub.History...)
		}
		if sub.ExpiresAt != nil {
			item["expires_at"] = sub.ExpiresAt.Format(time.RFC3339)
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

func (r *Registry) handleRemoveContextEntity(_ context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	tags, err := parseTagArgs(args["tags"])
	if err != nil {
		return "", err
	}

	if len(tags) > 0 {
		err = r.watchlistStore.RemoveWithScopes(entityID, tags)
	} else {
		err = r.watchlistStore.Remove(entityID)
	}
	if err != nil {
		return "", fmt.Errorf("remove from watchlist: %w", err)
	}

	slog.Info("context entity removed", "entity_id", entityID, "tags", tags)
	if len(tags) > 0 {
		return fmt.Sprintf("Stopped watching %s in scopes %v.", entityID, tags), nil
	}
	return fmt.Sprintf("Stopped watching %s.", entityID), nil
}

func parseTagArgs(raw any) ([]string, error) {
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

func parseHistoryArg(raw any) ([]int, error) {
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

func intArg(raw any, field string) (int, error) {
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

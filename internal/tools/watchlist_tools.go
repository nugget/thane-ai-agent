package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/awareness"
)

// SetWatchlistStore adds the add_context_entity and remove_context_entity
// tools to the registry.
func (r *Registry) SetWatchlistStore(store *awareness.WatchlistStore) {
	r.watchlistStore = store
	r.registerWatchlistTools()
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
			"Use tags to scope entity context to specific capabilities (only visible when that tag is active). " +
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
					"description": "Historical snapshot offsets in seconds. Include state values from these many seconds ago. E.g., [600, 3600, 86400] includes snapshots from 10min, 1hr, and 1day ago.",
				},
			},
			"required": []string{"entity_id"},
		},
		Handler: r.handleAddContextEntity,
	})

	r.Register(&Tool{
		Name: "remove_context_entity",
		Description: "Remove a Home Assistant entity from the watched list. " +
			"The entity's state will no longer appear in your context each turn.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entity_id": map[string]any{
					"type":        "string",
					"description": "The Home Assistant entity ID to stop watching",
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

	var tags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, rt := range rawTags {
			if s, ok := rt.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
	}

	var history []int
	if rawHist, ok := args["history"].([]any); ok {
		for _, rh := range rawHist {
			switch v := rh.(type) {
			case float64:
				history = append(history, int(v))
			case int:
				history = append(history, v)
			}
		}
	}

	if len(tags) > 0 || len(history) > 0 {
		if err := r.watchlistStore.AddWithOptions(entityID, tags, history); err != nil {
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
		msg += fmt.Sprintf(" (history: %vs ago)", history)
	}
	msg += "."

	slog.Info("context entity added",
		"entity_id", entityID, "tags", tags, "history", history)
	return msg, nil
}

func (r *Registry) handleRemoveContextEntity(_ context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	if err := r.watchlistStore.Remove(entityID); err != nil {
		return "", fmt.Errorf("remove from watchlist: %w", err)
	}

	slog.Info("context entity removed", "entity_id", entityID)
	return fmt.Sprintf("Stopped watching %s.", entityID), nil
}

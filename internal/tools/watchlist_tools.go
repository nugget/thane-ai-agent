package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/watchlist"
)

// SetWatchlistStore adds the add_context_entity and remove_context_entity
// tools to the registry.
func (r *Registry) SetWatchlistStore(store *watchlist.Store) {
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
			"Use this to monitor sensors, doors, batteries, or any entity you want to keep an eye on.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entity_id": map[string]any{
					"type":        "string",
					"description": "The Home Assistant entity ID to watch (e.g., sensor.office_temperature)",
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

	if err := r.watchlistStore.Add(entityID); err != nil {
		return "", fmt.Errorf("add to watchlist: %w", err)
	}

	slog.Info("context entity added", "entity_id", entityID)
	return fmt.Sprintf("Now watching %s — its state will appear in your context each turn.", entityID), nil
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

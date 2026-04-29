package awareness

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// WatchlistTools is the [tools.Provider] for the add_context_entity,
// list_context_entities, and remove_context_entity tools. It owns the
// watchlist store the handlers read/write and an optional callback
// invoked when new scoped tags are introduced (so the caller can wire
// up tag-specific context providers on demand).
type WatchlistTools struct {
	store        *WatchlistStore
	tagRegistrar func(tag string)
	logger       *slog.Logger
}

// WatchlistToolsConfig captures the dependencies for [NewWatchlistTools].
type WatchlistToolsConfig struct {
	// Store is the persistent watchlist store. Required.
	Store *WatchlistStore
	// TagRegistrar is invoked when add_context_entity introduces a
	// new tag; typical callers use it to register a tag-scoped
	// context provider. Optional.
	TagRegistrar func(tag string)
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
		store:        cfg.Store,
		tagRegistrar: cfg.TagRegistrar,
		logger:       logger,
	}
}

// Name implements [tools.Provider].
func (w *WatchlistTools) Name() string { return "awareness.watchlist" }

// Tools implements [tools.Provider]. Returns the three watchlist
// management tools with handlers bound to w's store.
func (w *WatchlistTools) Tools() []*tools.Tool {
	return []*tools.Tool{
		{
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
			Handler: w.handleAddContextEntity,
		},
		{
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
			Handler: w.handleListContextEntities,
		},
		{
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
			Handler: w.handleRemoveContextEntity,
		},
	}
}

func (w *WatchlistTools) handleAddContextEntity(_ context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
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

	if len(tags) > 0 || len(history) > 0 || ttlSeconds > 0 {
		if err := w.store.AddWithOptions(entityID, tags, history, ttlSeconds); err != nil {
			return "", fmt.Errorf("add to watchlist: %w", err)
		}
	} else {
		if err := w.store.Add(entityID); err != nil {
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

	w.logger.Info("context entity added",
		"entity_id", entityID, "tags", tags, "history", history, "ttl_seconds", ttlSeconds)
	if w.tagRegistrar != nil {
		for _, tag := range tags {
			w.tagRegistrar(tag)
		}
	}
	return msg, nil
}

func (w *WatchlistTools) handleListContextEntities(_ context.Context, args map[string]any) (string, error) {
	tag, _ := args["tag"].(string)
	entityID, _ := args["entity_id"].(string)

	subs, err := w.store.ListSubscriptions(strings.TrimSpace(tag))
	if err != nil {
		return "", fmt.Errorf("list watchlist subscriptions: %w", err)
	}

	now := time.Now()
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
			item["expires_delta"] = promptfmt.FormatDeltaOnly(*sub.ExpiresAt, now)
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

func (w *WatchlistTools) handleRemoveContextEntity(_ context.Context, args map[string]any) (string, error) {
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

	w.logger.Info("context entity removed", "entity_id", entityID, "tags", tags)
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

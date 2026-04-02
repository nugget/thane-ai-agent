package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/router"
)

// Tools provides MQTT subscription management tool handlers for the
// agent's tool registry. Each handler follows the standard tool
// signature: func(ctx, args) (string, error).
type Tools struct {
	store *SubscriptionStore
}

// NewTools creates MQTT tools backed by the given subscription store.
func NewTools(store *SubscriptionStore) *Tools {
	return &Tools{store: store}
}

// HandleListWakeSubscriptions returns a JSON list of all wake
// subscriptions (config + runtime).
func (t *Tools) HandleListWakeSubscriptions(_ context.Context, _ map[string]any) (string, error) {
	subs := t.store.List()
	if len(subs) == 0 {
		return "No MQTT wake subscriptions configured.", nil
	}

	type entry struct {
		ID           string `json:"id"`
		Topic        string `json:"topic"`
		Source       string `json:"source"`
		Mission      string `json:"mission,omitempty"`
		QualityFloor string `json:"quality_floor,omitempty"`
		Model        string `json:"model,omitempty"`
		Instructions string `json:"instructions,omitempty"`
	}

	entries := make([]entry, len(subs))
	for i, ws := range subs {
		entries[i] = entry{
			ID:           ws.ID,
			Topic:        ws.Topic,
			Source:       ws.Source,
			Mission:      ws.Seed.Mission,
			QualityFloor: ws.Seed.QualityFloor,
			Model:        ws.Seed.Model,
			Instructions: ws.Seed.Instructions,
		}
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal subscriptions: %w", err)
	}
	return string(data), nil
}

// HandleAddWakeSubscription creates a new runtime wake subscription.
// Required args: "topic". Optional: all LoopSeed fields.
func (t *Tools) HandleAddWakeSubscription(_ context.Context, args map[string]any) (string, error) {
	topic, _ := args["topic"].(string)
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return "", fmt.Errorf("topic is required")
	}

	seed := router.LoopSeed{
		Model:            stringArg(args, "model"),
		QualityFloor:     stringArg(args, "quality_floor"),
		Mission:          stringArg(args, "mission"),
		LocalOnly:        stringArg(args, "local_only"),
		DelegationGating: stringArg(args, "delegation_gating"),
		PreferSpeed:      stringArg(args, "prefer_speed"),
		Instructions:     stringArg(args, "instructions"),
	}

	if raw, ok := args["exclude_tools"]; ok {
		seed.ExcludeTools = toStringSlice(raw)
	}
	if raw, ok := args["seed_tags"]; ok {
		seed.SeedTags = toStringSlice(raw)
	}

	ws, err := t.store.Add(topic, seed)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Wake subscription added (id=%s, topic=%s). Subscription is active immediately.", ws.ID, ws.Topic), nil
}

// HandleRemoveWakeSubscription removes a runtime subscription by ID.
// Required args: "id".
func (t *Tools) HandleRemoveWakeSubscription(_ context.Context, args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}

	if err := t.store.Remove(id); err != nil {
		return "", err
	}

	return fmt.Sprintf("Wake subscription %q removed.", id), nil
}

// stringArg extracts a string from an args map, returning "" if absent
// or not a string.
func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// toStringSlice converts an any value to []string. Handles both
// []any (from JSON tool args) and []string.
func toStringSlice(v any) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		result := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

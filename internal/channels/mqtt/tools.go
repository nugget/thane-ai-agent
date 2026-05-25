package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// Tools provides MQTT subscription management tool handlers for the
// agent's tool registry. Each handler follows the standard tool
// signature: func(ctx, args) (string, error).
type Tools struct {
	store        *SubscriptionStore
	loopResolver messages.LoopResolver
}

// NewTools creates MQTT tools backed by the given subscription store.
// loopResolver, when non-nil, is used to verify wake_loop targets at
// subscription-add time so a typo'd loop name fails loud rather than
// producing a permanent silent-drop on every matching message.
func NewTools(store *SubscriptionStore, loopResolver messages.LoopResolver) *Tools {
	return &Tools{store: store, loopResolver: loopResolver}
}

// listEntry is the JSON shape emitted by HandleListWakeSubscriptions.
// One entry per subscription; the WakeLoop field is populated for
// trigger-style subscriptions, the Profile fields for legacy
// spawn-style subscriptions, so the model can tell at a glance which
// dispatch shape each entry uses.
type listEntry struct {
	SubscriptionID string                   `json:"subscription_id"`
	Topic          string                   `json:"topic"`
	Source         string                   `json:"source"`
	WakeLoop       *messages.LoopWakeTarget `json:"wake_loop,omitempty"`
	Mission        string                   `json:"mission,omitempty"`
	QualityFloor   int                      `json:"quality_floor,omitempty"`
	Model          string                   `json:"model,omitempty"`
	Instructions   string                   `json:"instructions,omitempty"`
}

// HandleListWakeSubscriptions returns a JSON list of all wake
// subscriptions (config + runtime).
func (t *Tools) HandleListWakeSubscriptions(_ context.Context, _ map[string]any) (string, error) {
	subs := t.store.List()
	entries := make([]listEntry, 0, len(subs))
	for _, ws := range subs {
		entry := listEntry{
			SubscriptionID: ws.ID,
			Topic:          ws.Topic,
			Source:         ws.Source,
		}
		if ws.HasWakeTarget() {
			target := ws.WakeTarget
			entry.WakeLoop = &target
		} else {
			entry.Mission = ws.Profile.Mission
			entry.QualityFloor = ws.Profile.QualityFloor
			entry.Model = ws.Profile.Model
			entry.Instructions = ws.Profile.Instructions
		}
		entries = append(entries, entry)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal subscriptions: %w", err)
	}
	return string(data), nil
}

// addResponse is the JSON shape returned by HandleAddWakeSubscription.
// Uniform success-payload pattern matches forge_repo_follow /
// media_follow.
type addResponse struct {
	Status         string                   `json:"status"`
	SubscriptionID string                   `json:"subscription_id"`
	Topic          string                   `json:"topic"`
	WakeLoop       *messages.LoopWakeTarget `json:"wake_loop,omitempty"`
	Note           string                   `json:"note,omitempty"`
}

// HandleAddWakeSubscription creates a new runtime wake subscription.
// Required args: "topic" plus one of (a) "wake_loop" naming an
// existing target loop or (b) any of the legacy spawn-profile fields
// (mission, model, quality_floor, etc.).
func (t *Tools) HandleAddWakeSubscription(_ context.Context, args map[string]any) (string, error) {
	topic, _ := args["topic"].(string)
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return "", fmt.Errorf("topic is required")
	}

	wakeTarget, wakeConfigured, err := messages.ParseLoopWakeTarget(args["wake_loop"])
	if err != nil {
		return "", err
	}
	if wakeConfigured {
		if err := messages.VerifyLoopWakeTarget(wakeTarget, t.loopResolver); err != nil {
			return "", err
		}
	}

	profile := router.LoopProfile{
		Model:            stringArg(args, "model"),
		QualityFloor:     intArg(args, "quality_floor"),
		Mission:          stringArg(args, "mission"),
		LocalOnly:        stringArg(args, "local_only"),
		DelegationGating: stringArg(args, "delegation_gating"),
		PreferSpeed:      stringArg(args, "prefer_speed"),
		Instructions:     stringArg(args, "instructions"),
	}

	if raw, ok := args["exclude_tools"]; ok {
		profile.ExcludeTools = toStringSlice(raw)
	}

	var initialTags []string
	if raw, ok := args["initial_tags"]; ok {
		initialTags = toStringSlice(raw)
	}

	// Validate the profile only when we'll actually use it (no
	// WakeTarget set). With wake_loop dispatch the legacy spawn-
	// profile fields are documented as ignored — so a stray
	// invalid quality_floor=99 left over from copy-paste
	// shouldn't block adding a valid wake_loop subscription.
	if !wakeConfigured {
		if err := profile.Validate(); err != nil {
			return "", fmt.Errorf("invalid profile: %w", err)
		}
	}

	ws, err := t.store.Add(AddRequest{
		Topic:       topic,
		WakeTarget:  wakeTarget,
		Profile:     profile,
		InitialTags: initialTags,
	})
	if err != nil {
		return "", err
	}

	resp := addResponse{
		Status:         "ok",
		SubscriptionID: ws.ID,
		Topic:          ws.Topic,
		Note:           "Live subscribe attempted; activates on next reconnect if broker is not currently connected.",
	}
	if ws.HasWakeTarget() {
		target := ws.WakeTarget
		resp.WakeLoop = &target
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(data), nil
}

// HandleRemoveWakeSubscription removes a runtime subscription by ID.
// `subscription_id` is the cross-family canonical parameter name,
// matching forge_repo_unfollow and media_unfollow.
func (t *Tools) HandleRemoveWakeSubscription(_ context.Context, args map[string]any) (string, error) {
	id := strings.TrimSpace(stringArg(args, "subscription_id"))
	if id == "" {
		return "", fmt.Errorf("subscription_id is required")
	}

	if err := t.store.Remove(id); err != nil {
		return "", err
	}

	data, err := json.Marshal(map[string]string{
		"status":          "ok",
		"subscription_id": id,
	})
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(data), nil
}

// stringArg extracts a string from an args map, returning "" if absent
// or not a string.
func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// intArg extracts an int from an args map, accepting both numeric
// (int, float64 from JSON) and string-of-int forms. Returns 0 when
// the arg is absent or unparseable — matches the
// [router.LoopProfile.QualityFloor] "zero means unset" convention.
func intArg(args map[string]any, key string) int {
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0
		}
		return parsed
	}
	return 0
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

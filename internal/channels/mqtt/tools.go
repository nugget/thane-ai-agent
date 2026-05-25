package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
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
// One entry per subscription; every active subscription routes through
// a wake_loop target after the trigger-unification work, so WakeLoop is
// always populated.
type listEntry struct {
	SubscriptionID string                  `json:"subscription_id"`
	Topic          string                  `json:"topic"`
	Source         string                  `json:"source"`
	WakeLoop       messages.LoopWakeTarget `json:"wake_loop"`
}

// HandleListWakeSubscriptions returns a JSON list of all wake
// subscriptions (config + runtime).
func (t *Tools) HandleListWakeSubscriptions(_ context.Context, _ map[string]any) (string, error) {
	subs := t.store.List()
	entries := make([]listEntry, 0, len(subs))
	for _, ws := range subs {
		entries = append(entries, listEntry{
			SubscriptionID: ws.ID,
			Topic:          ws.Topic,
			Source:         ws.Source,
			WakeLoop:       ws.WakeTarget,
		})
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
	Status         string                  `json:"status"`
	SubscriptionID string                  `json:"subscription_id"`
	Topic          string                  `json:"topic"`
	WakeLoop       messages.LoopWakeTarget `json:"wake_loop"`
	Note           string                  `json:"note,omitempty"`
}

// HandleAddWakeSubscription creates a new runtime wake subscription.
// Required args: "topic" and "wake_loop" (naming an existing target
// loop). The legacy inline-Profile spawn path was retired in the
// trigger-unification work; operators who want bespoke handling
// create their own event-driven loop and point wake_loop at it.
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
	if !wakeConfigured {
		return "", fmt.Errorf("wake_loop is required (provide loop_id or name)")
	}
	if err := messages.VerifyLoopWakeTarget(wakeTarget, t.loopResolver); err != nil {
		return "", err
	}

	ws, err := t.store.Add(AddRequest{
		Topic:      topic,
		WakeTarget: wakeTarget,
	})
	if err != nil {
		return "", err
	}

	resp := addResponse{
		Status:         "ok",
		SubscriptionID: ws.ID,
		Topic:          ws.Topic,
		WakeLoop:       ws.WakeTarget,
		Note:           "Live subscribe attempted; activates on next reconnect if broker is not currently connected.",
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

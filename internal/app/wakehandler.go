package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/knowledge"
	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// wakeTimeout bounds how long a single MQTT wake may run.
const mqttWakeTimeout = 5 * time.Minute

// wakeFireRecorder records a fire event for a wake subscription.
// Satisfied by [homeassistant.WakeStore].
type wakeFireRecorder interface {
	RecordFire(id string) error
}

// wakeTopicMatcher finds active wake subscriptions for a given topic.
// Satisfied by [homeassistant.WakeStore].
type wakeTopicMatcher interface {
	ActiveByTopic(topic string) ([]*homeassistant.WakeSubscription, error)
}

// WakeHandlerConfig holds dependencies for the MQTT wake handler.
type WakeHandlerConfig struct {
	Store  wakeTopicMatcher
	Fire   wakeFireRecorder
	HA     wakeStateGetter // optional; nil disables entity context injection
	Runner agentRunner
	Logger *slog.Logger
}

// WakeHandler processes MQTT messages and triggers agent wakes for
// matching wake subscriptions. It replaces the v1 WakeBridge by
// receiving messages from any MQTT source rather than only HA
// WebSocket state_changed events.
type WakeHandler struct {
	store  wakeTopicMatcher
	fire   wakeFireRecorder
	ha     wakeStateGetter
	runner agentRunner
	logger *slog.Logger
}

// NewWakeHandler creates a wake handler with the given configuration.
func NewWakeHandler(cfg WakeHandlerConfig) *WakeHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &WakeHandler{
		store:  cfg.Store,
		fire:   cfg.Fire,
		ha:     cfg.HA,
		runner: cfg.Runner,
		logger: logger,
	}
}

// HandleMessage is an mqtt.MessageHandler-compatible function. It
// matches the incoming topic against active wake subscriptions and
// triggers an agent wake for each match.
func (h *WakeHandler) HandleMessage(topic string, payload []byte) {
	subs, err := h.store.ActiveByTopic(topic)
	if err != nil {
		h.logger.Error("wake subscription lookup failed",
			"topic", topic, "error", err)
		return
	}

	if len(subs) == 0 {
		return
	}

	for _, sub := range subs {
		if err := h.fire.RecordFire(sub.ID); err != nil {
			h.logger.Error("failed to record wake fire",
				"subscription_id", sub.ID, "error", err)
		}

		msg := formatWakeHandlerMessage(sub, topic, payload)
		h.logger.Info("wake subscription matched, triggering wake",
			"subscription_id", sub.ID,
			"subscription", sub.Name,
			"topic", topic,
		)

		go h.runWake(sub, msg)
	}
}

// runWake executes a single agent wake for a matched subscription.
func (h *WakeHandler) runWake(sub *homeassistant.WakeSubscription, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), mqttWakeTimeout)
	defer cancel()

	log := h.logger.With(
		"subsystem", logging.SubsystemScheduler,
		"subscription_id", sub.ID,
		"topic", sub.Topic,
	)
	ctx = logging.WithLogger(ctx, log)

	// Inject entity subjects for context pre-warming.
	if len(sub.Seed.ContextEntities) > 0 {
		subjects := make([]string, 0, len(sub.Seed.ContextEntities))
		for _, ce := range sub.Seed.ContextEntities {
			subjects = append(subjects, "entity:"+ce)
		}
		ctx = knowledge.WithSubjects(ctx, subjects)
	}

	// Append entity context to the message if available.
	if entityCtx := h.fetchEntityContext(ctx, sub.Seed.ContextEntities); entityCtx != "" {
		message += "\nRelevant entity states:\n" + entityCtx
	}

	// Build the request from the LoopSeed.
	hints := sub.Seed.Hints()
	hints["subscription_id"] = sub.ID

	req := &agent.Request{
		ConversationID: fmt.Sprintf("wake-%s", sub.ID),
		Messages:       []agent.Message{{Role: "user", Content: message}},
		Model:          sub.Seed.Model,
		Hints:          hints,
		ExcludeTools:   sub.Seed.ExcludeTools,
		SeedTags:       sub.Seed.SeedTags,
	}

	resp, err := h.runner.Run(ctx, req, nil)
	if err != nil {
		log.Error("wake failed",
			"subscription_id", sub.ID,
			"error", err,
		)
		return
	}
	log.Info("wake completed",
		"subscription_id", sub.ID,
		"subscription", sub.Name,
		"result_len", len(resp.Content),
	)
}

// fetchEntityContext fetches and formats entity states for the wake message.
func (h *WakeHandler) fetchEntityContext(ctx context.Context, entities []string) string {
	if h.ha == nil || len(entities) == 0 {
		return ""
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var sb strings.Builder
	for _, id := range entities {
		state, err := h.ha.GetState(fetchCtx, id)
		if err != nil {
			h.logger.Warn("wake entity context fetch failed",
				"entity_id", id, "error", err)
			fmt.Fprintf(&sb, "Entity: %s\nState: (fetch failed)\n\n", id)
			continue
		}
		sb.WriteString(tools.FormatEntityState(state))
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatWakeHandlerMessage builds the user-facing message for a wake.
func formatWakeHandlerMessage(sub *homeassistant.WakeSubscription, topic string, payload []byte) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Wake subscription matched: %q\n\n", sub.Name)
	fmt.Fprintf(&sb, "MQTT topic: %s\n", topic)

	// Include payload summary if it's JSON.
	if len(payload) > 0 && len(payload) < 4096 {
		if json.Valid(payload) {
			fmt.Fprintf(&sb, "Payload:\n```json\n%s\n```\n", payload)
		} else {
			fmt.Fprintf(&sb, "Payload: %s\n", payload)
		}
	}

	if sub.KBRef != "" {
		fmt.Fprintf(&sb, "\nKnowledge reference: %s\n", sub.KBRef)
	}

	if sub.Seed.Context != "" {
		sb.WriteString("\nInstructions you left for yourself:\n")
		sb.WriteString(sub.Seed.Context)
		sb.WriteString("\n")
	} else if sub.Context != "" {
		sb.WriteString("\nInstructions you left for yourself:\n")
		sb.WriteString(sub.Context)
		sb.WriteString("\n")
	}

	return sb.String()
}

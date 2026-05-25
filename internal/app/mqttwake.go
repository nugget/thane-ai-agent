package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	mqtt "github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// maxWakePayloadBytes bounds the MQTT payload size included in the
// event-source envelope summary. Payloads exceeding this are truncated
// on a valid UTF-8 boundary with a marker so a chatty topic can't
// blow up downstream prompt rendering.
const maxWakePayloadBytes = 32 * 1024

// mqttWakeDeliveryTimeout bounds the bus.Send call for envelope
// delivery. Each MQTT message arrives in its own goroutine; without
// this bound, a stuck downstream route handler could leak goroutines
// under steady MQTT traffic. The actual agent work happens in the
// target loop's next iteration on its own clock — this only bounds
// the envelope-handoff hop.
const mqttWakeDeliveryTimeout = 30 * time.Second

// mqttWakeDeps wires the wake handler to the message bus and event
// bus. parentID is unused in the post-retirement dispatch shape but
// retained so the wiring in new_servers.go stays compatible during
// the transition.
type mqttWakeDeps struct {
	registry   *looppkg.Registry
	messageBus *messages.Bus
	eventBus   *events.Bus
	parentID   *atomic.Value
}

// mqttWakeHandler returns a MessageHandler that delivers MQTT messages
// to existing event-driven loops via the message bus. Every active
// subscription routes through a wake_loop target after the trigger-
// unification work; legacy inline-Profile subscriptions are migrated
// onto [mqtt.DefaultHandlerLoopName] at config load / DB hydrate time.
//
// Messages on topics without a wake subscription are passed through to
// the fallback handler. When the message bus is nil (operator
// configuration error) matching messages are logged and dropped.
func mqttWakeHandler(
	store *mqtt.SubscriptionStore,
	fallback mqtt.MessageHandler,
	logger *slog.Logger,
	deps mqttWakeDeps,
) mqtt.MessageHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(topic string, payload []byte) {
		matches := store.Matches(topic)
		if len(matches) == 0 {
			if fallback != nil {
				fallback(topic, payload)
			}
			return
		}

		if deps.messageBus == nil {
			logger.Error("mqtt wake has no message bus, dropping message",
				"topic", topic,
				"matches", len(matches),
			)
			return
		}

		// Fan-out: dispatch one envelope per matching subscription so
		// multiple subscribers (e.g. owner-attention plus security
		// monitoring) on the same topic each see the same event in
		// their own loop's iteration context.
		for _, ws := range matches {
			ws := ws
			go dispatchViaWakeTarget(deps, ws, topic, payload, logger)
		}
	}
}

// dispatchViaWakeTarget delivers an MQTT message to the subscription's
// wake target as an event-source notification. Builds a
// [messages.LoopEventPayload], wraps it in a
// [messages.NewEventSourceEnvelope], and publishes via the bus with a
// bounded delivery context. No new loop is spawned — the target loop
// (a custom event-driven loop or the built-in default handler) sees
// the event in its pending notifications and runs under its own
// Spec.Profile / container cascade.
func dispatchViaWakeTarget(deps mqttWakeDeps, ws mqtt.WakeSubscription, topic string, payload []byte, logger *slog.Logger) {
	event := messages.LoopEventPayload{
		Source:     "mqtt_wake",
		Type:       "message",
		ID:         fmt.Sprintf("mqtt-%s-%d", ws.ID, time.Now().UnixMilli()),
		Title:      topic,
		Summary:    sanitizePayload(payload),
		ObservedAt: time.Now(),
		Metadata: map[string]string{
			"topic":           topic,
			"subscription_id": ws.ID,
		},
	}

	env, err := messages.NewEventSourceEnvelope(
		messages.Identity{Kind: messages.IdentitySystem, Name: "mqtt_wake"},
		ws.WakeTarget,
		"mqtt_wake",
		[]messages.LoopEventPayload{event},
	)
	if err != nil {
		logger.Warn("mqtt wake envelope construction failed, dropping message",
			"subscription_id", ws.ID,
			"topic", topic,
			"error", err,
		)
		return
	}

	deliveryCtx, cancel := context.WithTimeout(context.Background(), mqttWakeDeliveryTimeout)
	defer cancel()
	if _, err := deps.messageBus.Send(deliveryCtx, env); err != nil {
		logger.Warn("mqtt wake envelope delivery failed, dropping message",
			"subscription_id", ws.ID,
			"topic", topic,
			"target_loop_id", ws.WakeTarget.LoopID,
			"target_loop_name", ws.WakeTarget.Name,
			"error", err,
		)
		return
	}

	logger.Info("mqtt wake delivered to target loop",
		"subscription_id", ws.ID,
		"topic", topic,
		"target_loop_id", ws.WakeTarget.LoopID,
		"target_loop_name", ws.WakeTarget.Name,
		"payload_size", len(payload),
	)
}

// sanitizePayload converts raw MQTT bytes to a valid UTF-8 string,
// replacing invalid sequences and truncating to [maxWakePayloadBytes]
// on a rune boundary. The byte slice is cropped before any string
// conversion so a multi-megabyte payload from a chatty topic doesn't
// force an allocation of the full body just to throw most of it away.
// A small UTF-8 slack (4 bytes — the maximum encoding length for a
// single rune) is included in the crop so a multi-byte rune
// straddling the limit can be discarded cleanly by the rune-boundary
// trim below.
func sanitizePayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	totalBytes := len(payload)
	truncated := totalBytes > maxWakePayloadBytes
	if truncated {
		const utf8Slack = utf8.UTFMax
		payload = payload[:maxWakePayloadBytes+utf8Slack]
	}
	s := string(payload)
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if !truncated {
		return s
	}
	if len(s) > maxWakePayloadBytes {
		s = s[:maxWakePayloadBytes]
	}
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + fmt.Sprintf("\n\n[Truncated: %d bytes total, showing first %d bytes]", totalBytes, maxWakePayloadBytes)
}

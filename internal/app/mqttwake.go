package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	mqtt "github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// mqttWakeTimeout bounds how long a single MQTT-triggered agent
// conversation may run before being cancelled.
const mqttWakeTimeout = 5 * time.Minute

// mqttWakeHandler returns a MessageHandler that dispatches agent
// conversations when MQTT messages arrive on wake-configured topics.
// Messages on topics without a wake subscription are passed through
// to the fallback handler.
func mqttWakeHandler(
	store *mqtt.SubscriptionStore,
	runner agentRunner,
	fallback mqtt.MessageHandler,
	logger *slog.Logger,
) mqtt.MessageHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(topic string, payload []byte) {
		ws, ok := store.Match(topic)
		if !ok {
			if fallback != nil {
				fallback(topic, payload)
			}
			return
		}

		// Dispatch in a goroutine so the MQTT message handler does not
		// block the inbound message loop.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), mqttWakeTimeout)
			defer cancel()

			convID := fmt.Sprintf("mqtt-wake-%d", time.Now().UnixMilli())
			msg := buildWakeMessage(topic, payload, ws.Seed.Instructions)

			req := &agent.Request{
				ConversationID: convID,
				Messages:       []agent.Message{{Role: "user", Content: msg}},
			}
			applyLoopSeed(&ws.Seed, req)

			// Always tag the source so tools and logging can identify
			// MQTT-triggered conversations.
			if req.Hints == nil {
				req.Hints = make(map[string]string)
			}
			req.Hints["source"] = "mqtt_wake"
			req.Hints["mqtt_topic"] = topic

			logger.Info("mqtt wake dispatching agent",
				"conv_id", convID,
				"topic", topic,
				"payload_size", len(payload),
			)

			resp, err := runner.Run(ctx, req, nil)
			if err != nil {
				logger.Error("mqtt wake agent dispatch failed",
					"conv_id", convID,
					"topic", topic,
					"error", err,
				)
				return
			}

			logger.Info("mqtt wake complete",
				"conv_id", convID,
				"topic", topic,
				"result_len", len(resp.Content),
			)
		}()
	}
}

// buildWakeMessage constructs the user message for an MQTT wake. When
// instructions are provided, the payload is wrapped with context about
// the topic and instructions. Otherwise the raw payload is used.
func buildWakeMessage(topic string, payload []byte, instructions string) string {
	payloadStr := string(payload)

	if instructions == "" {
		if payloadStr == "" {
			return fmt.Sprintf("MQTT message received on topic: %s", topic)
		}
		return fmt.Sprintf("MQTT message received on topic: %s\n\n%s", topic, payloadStr)
	}

	if payloadStr == "" {
		return fmt.Sprintf("Instructions: %s\n\nMQTT topic: %s\n(no payload)", instructions, topic)
	}
	return fmt.Sprintf("Instructions: %s\n\nMQTT topic: %s\nPayload:\n%s", instructions, topic, payloadStr)
}

// applyLoopSeed applies a LoopSeed's configuration to an agent.Request.
// It sets the model, merges routing hints, and copies tool exclusions
// and seed tags. This function lives in the app package rather than on
// LoopSeed itself to avoid a circular import between router and agent.
func applyLoopSeed(seed *router.LoopSeed, req *agent.Request) {
	if seed.Model != "" {
		req.Model = seed.Model
	}

	hints := seed.Hints()
	if len(hints) > 0 {
		if req.Hints == nil {
			req.Hints = make(map[string]string, len(hints))
		}
		for k, v := range hints {
			req.Hints[k] = v
		}
	}

	if len(seed.ExcludeTools) > 0 {
		req.ExcludeTools = append(req.ExcludeTools, seed.ExcludeTools...)
	}
	if len(seed.SeedTags) > 0 {
		req.SeedTags = append(req.SeedTags, seed.SeedTags...)
	}
}

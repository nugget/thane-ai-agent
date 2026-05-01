package app

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	mqtt "github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// maxWakePayloadBytes is the maximum MQTT payload size included in the
// user message. Payloads exceeding this are truncated on a valid UTF-8
// boundary with a marker.
const maxWakePayloadBytes = 32 * 1024

// mqttWakeTimeout bounds how long a single MQTT-triggered agent
// conversation may run before being cancelled.
const mqttWakeTimeout = 5 * time.Minute

// mqttWakeDeps holds optional dependencies for dashboard integration.
// When registry is non-nil, each wake conversation is spawned as a
// child loop under parentID so it appears on the dashboard.
type mqttWakeDeps struct {
	registry *looppkg.Registry
	eventBus *events.Bus
	parentID *atomic.Value // stores string; populated lazily from the mqtt service loop
}

// mqttWakeHandler returns a MessageHandler that dispatches agent
// conversations when MQTT messages arrive on wake-configured topics.
// Messages on topics without a wake subscription are passed through
// to the fallback handler.
//
// When deps.registry is non-nil, each wake conversation is registered
// as a child loop under the MQTT parent so it appears on the dashboard.
func mqttWakeHandler(
	store *mqtt.SubscriptionStore,
	runner agentRunner,
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

		if deps.registry == nil {
			logger.Error("mqtt wake has no loop registry, dropping message",
				"topic", topic,
				"matches", len(matches),
			)
			return
		}

		// Fan-out: dispatch one agent conversation per matching
		// subscription. Each gets its own goroutine so the MQTT
		// message handler does not block the inbound message loop.
		for _, ws := range matches {
			ws := ws // capture loop variable
			go func() {
				convID := fmt.Sprintf("mqtt-wake-%s-%d", ws.ID, time.Now().UnixMilli())
				msg := buildWakeMessage(topic, payload, ws.Profile.Instructions)

				req := &agent.Request{
					ConversationID: convID,
					Messages:       []agent.Message{{Role: "user", Content: msg}},
				}
				applyLoopProfile(&ws.Profile, req)
				if len(ws.InitialTags) > 0 {
					req.InitialTags = append(req.InitialTags, ws.InitialTags...)
				}

				// Always tag the source so tools and logging can identify
				// MQTT-triggered conversations.
				if req.Hints == nil {
					req.Hints = make(map[string]string)
				}
				req.Hints["source"] = "mqtt_wake"
				req.Hints["mqtt_topic"] = topic
				req.Hints["mqtt_subscription_id"] = ws.ID

				logger.Info("mqtt wake dispatching agent",
					"conv_id", convID,
					"subscription_id", ws.ID,
					"topic", topic,
					"payload_size", len(payload),
				)

				// Use a short topic suffix for the loop name (last path segment).
				loopName := "mqtt/" + path.Base(topic)

				dispatchViaLoop(deps, runner, req, loopName, topic, convID, logger)
			}()
		}
	}
}

// dispatchViaLoop spawns a one-shot child loop under the MQTT parent
// so the wake conversation appears on the dashboard. The loop manages
// its own lifecycle via MaxDuration — no external context timeout is
// needed (SpawnLoop is non-blocking, so the caller must not use a
// short-lived context that would cancel the child loop).
func dispatchViaLoop(
	deps mqttWakeDeps,
	runner agentRunner,
	req *agent.Request,
	loopName, topic, convID string,
	logger *slog.Logger,
) {
	var parentID string
	if deps.parentID != nil {
		if v, ok := deps.parentID.Load().(string); ok {
			parentID = v
		}
	}
	if parentID == "" && deps.registry != nil {
		if parent := deps.registry.GetByName(mqttPublisherDefinitionName); parent != nil {
			parentID = parent.ID()
			if deps.parentID != nil {
				deps.parentID.Store(parentID)
			}
		}
	}

	// A wake loop without a parentID would be registered as a top-level
	// loop, which means it won't be filtered from MQTT telemetry and
	// could leak conversation content into topic names. Drop the message
	// rather than create an unparented loop.
	if parentID == "" {
		logger.Warn("mqtt wake parent loop not yet registered, dropping message",
			"conv_id", convID,
			"topic", topic,
		)
		return
	}

	// Use a background context: SpawnLoop is non-blocking (starts a
	// goroutine and returns immediately), so a timeout context here
	// would cancel the child loop as soon as defer fires. The loop's
	// MaxDuration enforces the wall-clock bound instead.
	//
	// Sleep values are set to 1ms so the initial sleep is effectively
	// zero — this is a one-shot loop that should execute immediately.
	const immediate = time.Millisecond
	_, err := deps.registry.SpawnLoop(context.Background(), looppkg.Config{
		Name:         loopName,
		MaxIter:      1,
		MaxDuration:  mqttWakeTimeout,
		SleepMin:     immediate,
		SleepMax:     immediate,
		SleepDefault: immediate,
		Jitter:       looppkg.Float64Ptr(0),
		ParentID:     parentID,
		Handler: func(hCtx context.Context, _ any) error {
			stream := agent.BuildProgressStream(looppkg.ProgressFunc(hCtx))
			resp, err := runner.Run(hCtx, req, stream)
			if err != nil {
				logger.Error("mqtt wake agent failed",
					"conv_id", convID,
					"topic", topic,
					"error", err,
				)
				return fmt.Errorf("mqtt wake %s on %s: %w", convID, topic, err)
			}

			looppkg.ReportAgentRun(hCtx, looppkg.AgentRunSummary{
				RequestID:          resp.RequestID,
				Model:              resp.Model,
				InputTokens:        resp.InputTokens,
				OutputTokens:       resp.OutputTokens,
				ContextWindow:      resp.ContextWindow,
				ToolsUsed:          resp.ToolsUsed,
				ActiveTags:         append([]string(nil), resp.ActiveTags...),
				EffectiveTools:     append([]string(nil), resp.EffectiveTools...),
				LoadedCapabilities: append([]toolcatalog.LoadedCapabilityEntry(nil), resp.LoadedCapabilities...),
			})

			logger.Info("mqtt wake complete",
				"conv_id", convID,
				"topic", topic,
				"result_len", len(resp.Content),
			)
			return nil
		},
		Metadata: map[string]string{
			"subsystem":       "mqtt",
			"category":        "wake",
			"mqtt_topic":      topic,
			"conversation_id": convID,
		},
	}, looppkg.Deps{
		Logger:   logger,
		EventBus: deps.eventBus,
	})
	if err != nil {
		logger.Error("mqtt wake loop spawn failed, message dropped",
			"conv_id", convID,
			"topic", topic,
			"error", err,
		)
	}
}

// buildWakeMessage constructs the user message for an MQTT wake. When
// instructions are provided, the payload is wrapped with context about
// the topic and instructions. Otherwise the raw payload is used.
//
// Payloads are sanitised to valid UTF-8 and truncated to
// [maxWakePayloadBytes] to bound prompt size and cost.
func buildWakeMessage(topic string, payload []byte, instructions string) string {
	payloadStr := sanitizePayload(payload)

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

// sanitizePayload converts raw MQTT bytes to a valid UTF-8 string,
// replacing invalid sequences and truncating to maxWakePayloadBytes
// on a rune boundary.
func sanitizePayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}

	// Ensure valid UTF-8 — replace invalid bytes.
	s := string(payload)
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}

	if len(s) <= maxWakePayloadBytes {
		return s
	}

	// Truncate on a rune boundary.
	truncated := s[:maxWakePayloadBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + fmt.Sprintf("\n\n[Truncated: %d bytes total, showing first %d bytes]", len(s), maxWakePayloadBytes)
}

// applyLoopProfile applies a LoopProfile's routing configuration to an
// agent.Request. It sets the model, merges routing hints, and copies tool
// exclusions. Capability tags are not part of the routing profile and
// are applied separately by the caller. This function lives in the app
// package rather than on LoopProfile itself to avoid a circular import
// between router and agent.
func applyLoopProfile(profile *router.LoopProfile, req *agent.Request) {
	opts := profile.RequestOptions()

	if opts.Model != "" {
		req.Model = opts.Model
	}

	if len(opts.Hints) > 0 {
		if req.Hints == nil {
			req.Hints = make(map[string]string, len(opts.Hints))
		}
		for k, v := range opts.Hints {
			req.Hints[k] = v
		}
	}

	if len(opts.ExcludeTools) > 0 {
		req.ExcludeTools = append(req.ExcludeTools, opts.ExcludeTools...)
	}
}

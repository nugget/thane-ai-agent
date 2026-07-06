package app

import (
	"context"
	"encoding/json"
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
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// maxWakePayloadBytes bounds the MQTT payload size included in the
// event-source envelope summary. Payloads exceeding this are truncated
// on a valid UTF-8 boundary with a marker so a chatty topic can't
// blow up downstream prompt rendering.
const maxWakePayloadBytes = 32 * 1024

// mqttWakePartitionPrefix namespaces the loopqueue partitions the MQTT
// wake dispatcher owns. Partition strings are internal routing keys —
// deliberately NOT bare loop names, so a wake target that also happens
// to be a model-facing queue consumer (archivist-style queue_pull)
// never finds MQTT plumbing records in its own partition.
const mqttWakePartitionPrefix = "mqtt-wake:"

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

// mqttWakeDispatcher is the MQTT ingress over the shared
// [queuedWakeDispatcher] chassis: matched messages enqueue durably
// into per-target partitions (deduped per subscription+topic, latest
// payload wins) and the debounced drain replays them onto the message
// bus exactly the way the old direct dispatch did.
type mqttWakeDispatcher struct {
	dispatch *queuedWakeDispatcher
	deps     mqttWakeDeps
	logger   *slog.Logger

	// debounce/maxWait override the loopqueue wake defaults; zero
	// values defer to [loopqueue.DefaultWakeDebounce] /
	// [loopqueue.DefaultWakeMaxWait]. Tests shrink them.
	debounce time.Duration
	maxWait  time.Duration
}

func newMQTTWakeDispatcher(queue *loopqueue.Store, deps mqttWakeDeps, logger *slog.Logger) *mqttWakeDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &mqttWakeDispatcher{
		dispatch: newQueuedWakeDispatcher(queue, deps.messageBus, mqttWakePartitionPrefix, "mqtt_wake", nil, logger),
		deps:     deps,
		logger:   logger,
	}
}

// mqttWakeHandler returns a MessageHandler that routes MQTT messages
// on wake topics onto the loopqueue dispatcher. Every active
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
	dispatcher *mqttWakeDispatcher,
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

		if dispatcher == nil || dispatcher.deps.messageBus == nil {
			logger.Error("mqtt wake has no dispatcher/message bus, dropping message",
				"topic", topic,
				"matches", len(matches),
			)
			return
		}

		// Fan-out: one queue record per matching subscription so
		// multiple subscribers (e.g. owner-attention plus security
		// monitoring) on the same topic each see the same event in
		// their own loop's iteration context.
		for _, ws := range matches {
			ws := ws
			go dispatcher.ingress(ws, topic, payload)
		}
	}
}

// ingress converts one matched MQTT message into a durable queue
// record: resolve the effective wake target (honoring payload
// self-addressing), build the event, and enqueue it into the target's
// partition — arming the debounced drain. Coalescing: a second
// message on the same subscription+topic inside the debounce window
// replaces the pending payload (latest wins) rather than appending.
func (d *mqttWakeDispatcher) ingress(ws mqtt.WakeSubscription, topic string, payload []byte) {
	target := d.resolveTarget(ws, topic, payload)

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
	if target.Name != ws.WakeTarget.Name {
		event.Metadata["target_loop"] = target.Name
	}

	partition := mqttWakePartition(target)
	d.dispatch.register(partition, d.debounce, d.maxWait)

	dedupKey := fmt.Sprintf("mqtt:%s:%s", ws.ID, topic)
	if err := d.dispatch.enqueue(partition, dedupKey, targetPriorityRank(target), queuedWakeRecord{Target: target, Event: event}); err != nil {
		d.logger.Warn("mqtt wake enqueue failed, dropping message",
			"subscription_id", ws.ID, "topic", topic, "partition", partition, "error", err)
	}
}

// resolveTarget returns the subscription's wake target, overridden by
// a payload-declared target_loop when present and resolvable (#1033
// self-addressing: a general-purpose HA automation publishes
// {"target_loop": "<definition name>", ...} to a shared wake topic and
// wakes that loop with no pre-registered per-topic subscription). An
// unresolvable self-address falls back to the subscription's static
// target with a warning — the message still lands somewhere a model
// can triage it rather than vanishing.
func (d *mqttWakeDispatcher) resolveTarget(ws mqtt.WakeSubscription, topic string, payload []byte) messages.LoopWakeTarget {
	var probe struct {
		TargetLoop string `json:"target_loop"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return ws.WakeTarget
	}
	name := strings.TrimSpace(probe.TargetLoop)
	if name == "" {
		return ws.WakeTarget
	}
	if d.deps.registry == nil || !d.deps.registry.LoopExistsByName(name) {
		d.logger.Warn("mqtt payload target_loop does not resolve; falling back to subscription target",
			"subscription_id", ws.ID, "topic", topic,
			"target_loop", name,
			"fallback", ws.WakeTarget.Name,
		)
		return ws.WakeTarget
	}
	// The subscription's presentation fields (instructions, tags,
	// priority, supervisor forcing) ride along; only the destination
	// is re-addressed.
	target := ws.WakeTarget
	target.LoopID = ""
	target.Name = name
	return target
}

// Sweep drains any mqtt-wake partitions left pending by a crash while
// their debounce was armed. Call after the loop registry has hydrated
// so targets resolve.
func (d *mqttWakeDispatcher) Sweep(ctx context.Context) {
	d.dispatch.Sweep(ctx)
}

// mqttWakePartition derives the queue partition for a wake target.
// Keyed by destination so per-target coalescing works and two targets
// never share a burst window.
func mqttWakePartition(target messages.LoopWakeTarget) string {
	if target.LoopID != "" {
		return mqttWakePartitionPrefix + "id:" + target.LoopID
	}
	return mqttWakePartitionPrefix + "name:" + target.Name
}

// targetPriorityRank maps a wake target's delivery priority onto the
// queue's integer priority so urgent wakes drain ahead of normal ones
// within a partition.
func targetPriorityRank(target messages.LoopWakeTarget) int {
	switch target.Priority {
	case messages.PriorityUrgent:
		return 2
	case messages.PriorityLow:
		return 0
	default:
		return 1
	}
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

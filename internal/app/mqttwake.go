package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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

// mqttWakeDeliveryTimeout bounds the bus.Send call for envelope
// delivery. Each MQTT message arrives in its own goroutine; without
// this bound, a stuck downstream route handler could leak goroutines
// under steady MQTT traffic. The actual agent work happens in the
// target loop's next iteration on its own clock — this only bounds
// the envelope-handoff hop.
const mqttWakeDeliveryTimeout = 30 * time.Second

// mqttWakePartitionPrefix namespaces the loopqueue partitions the MQTT
// wake dispatcher owns. Partition strings are internal routing keys —
// deliberately NOT bare loop names, so a wake target that also happens
// to be a model-facing queue consumer (archivist-style queue_pull)
// never finds MQTT plumbing records in its own partition.
const mqttWakePartitionPrefix = "mqtt-wake:"

// mqttWakeDrainBatch bounds one Peek pass during a drain. The drain
// loops until the partition is empty, so this is a memory bound per
// pass, not a delivery cap.
const mqttWakeDrainBatch = 50

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

// mqttQueuedWake is the loopqueue payload for one matched MQTT
// message: the resolved wake target plus the event to deliver. Stored
// durably at ingress and replayed by the dispatcher's drain, so a
// crash between broker receipt and loop delivery no longer loses the
// message, and a burst on one topic coalesces (latest payload wins
// per subscription+topic) instead of amplifying into a wake per
// message (#1024/#1033).
type mqttQueuedWake struct {
	Target messages.LoopWakeTarget   `json:"target"`
	Event  messages.LoopEventPayload `json:"event"`
}

// mqttWakeDispatcher moves MQTT wake delivery onto the shared
// loopqueue chassis: ingress enqueues into a per-target partition and
// arms the WakeOnEnqueue debounce; the debounced fire drains the
// coalesced batch and hands each record to the message bus exactly
// the way the direct path used to. Trigger rate is decoupled from
// wake rate; the downstream envelope contract is unchanged.
type mqttWakeDispatcher struct {
	queue  *loopqueue.Store
	deps   mqttWakeDeps
	logger *slog.Logger

	// debounce/maxWait override the loopqueue wake defaults; zero
	// values defer to [loopqueue.DefaultWakeDebounce] /
	// [loopqueue.DefaultWakeMaxWait]. Tests shrink them.
	debounce time.Duration
	maxWait  time.Duration

	mu         sync.Mutex
	registered map[string]bool
	// draining serializes drains per partition: Peek does not claim
	// items, so a debounce fire racing a boot Sweep (or a second
	// fire) over the same partition would deliver the same records
	// twice. One drain runs at a time; a follower blocks, then finds
	// whatever the first left behind (usually nothing).
	draining map[string]*sync.Mutex
}

func newMQTTWakeDispatcher(queue *loopqueue.Store, deps mqttWakeDeps, logger *slog.Logger) *mqttWakeDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &mqttWakeDispatcher{
		queue:      queue,
		deps:       deps,
		logger:     logger,
		registered: make(map[string]bool),
		draining:   make(map[string]*sync.Mutex),
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

	record, err := json.Marshal(mqttQueuedWake{Target: target, Event: event})
	if err != nil {
		d.logger.Warn("mqtt wake record marshal failed, dropping message",
			"subscription_id", ws.ID, "topic", topic, "error", err)
		return
	}

	partition := mqttWakePartition(target)
	d.ensureRegistered(partition)

	enqCtx, cancel := context.WithTimeout(context.Background(), mqttWakeDeliveryTimeout)
	defer cancel()
	dedupKey := fmt.Sprintf("mqtt:%s:%s", ws.ID, topic)
	if err := d.queue.Enqueue(enqCtx, partition, dedupKey, int(targetPriorityRank(target)), record); err != nil {
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

// ensureRegistered lazily attaches the WakeOnEnqueue debounce for a
// partition the first time traffic addresses it. Package defaults for
// debounce/maxWait: coalesce a burst, never starve under chatter. The
// fire callback must not block (loopqueue contract), so it hands off
// to a goroutine that runs the serialized drain.
func (d *mqttWakeDispatcher) ensureRegistered(partition string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.registered[partition] {
		return
	}
	d.registered[partition] = true
	d.queue.SetWakeOnEnqueue(partition, d.debounce, d.maxWait, func() {
		go d.drainSerialized(partition)
	})
}

// partitionLock returns the per-partition drain mutex, creating it on
// first use.
func (d *mqttWakeDispatcher) partitionLock(partition string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	lock, ok := d.draining[partition]
	if !ok {
		lock = &sync.Mutex{}
		d.draining[partition] = lock
	}
	return lock
}

// drainSerialized runs drain under the partition's mutex so
// concurrent triggers (debounce fire, boot sweep) cannot replay the
// same un-acked records twice.
func (d *mqttWakeDispatcher) drainSerialized(partition string) {
	lock := d.partitionLock(partition)
	lock.Lock()
	defer lock.Unlock()
	d.drain(partition)
}

// drain delivers a partition's pending records to the message bus in
// drain order and acks each attempt. Delivery failures are logged and
// acked anyway — parity with the pre-queue direct path, where a
// failed bus send dropped the message; the queue adds crash
// durability and burst coalescing, not retry semantics. A record that
// no longer unmarshals is acked and skipped for the same reason.
func (d *mqttWakeDispatcher) drain(partition string) {
	ctx := context.Background()
	for {
		items, err := d.queue.Peek(ctx, partition, mqttWakeDrainBatch)
		if err != nil {
			d.logger.Warn("mqtt wake drain peek failed", "partition", partition, "error", err)
			return
		}
		if len(items) == 0 {
			return
		}
		for _, item := range items {
			d.deliver(partition, item)
		}
		if len(items) < mqttWakeDrainBatch {
			return
		}
	}
}

// deliver replays one queued record through the same envelope path
// the direct dispatch used, then acks it.
func (d *mqttWakeDispatcher) deliver(partition string, item loopqueue.Item) {
	ack := func() {
		if err := d.queue.Ack(context.Background(), partition, item.DedupKey); err != nil {
			d.logger.Warn("mqtt wake ack failed", "partition", partition, "dedup_key", item.DedupKey, "error", err)
		}
	}

	var record mqttQueuedWake
	if err := json.Unmarshal(item.Payload, &record); err != nil {
		d.logger.Warn("mqtt wake record unmarshal failed, dropping",
			"partition", partition, "dedup_key", item.DedupKey, "error", err)
		ack()
		return
	}

	env, err := messages.NewEventSourceEnvelope(
		messages.Identity{Kind: messages.IdentitySystem, Name: "mqtt_wake"},
		record.Target,
		"mqtt_wake",
		[]messages.LoopEventPayload{record.Event},
	)
	if err != nil {
		d.logger.Warn("mqtt wake envelope construction failed, dropping message",
			"partition", partition, "dedup_key", item.DedupKey, "error", err)
		ack()
		return
	}

	deliveryCtx, cancel := context.WithTimeout(context.Background(), mqttWakeDeliveryTimeout)
	defer cancel()
	if _, err := d.deps.messageBus.Send(deliveryCtx, env); err != nil {
		d.logger.Warn("mqtt wake envelope delivery failed, dropping message",
			"partition", partition,
			"dedup_key", item.DedupKey,
			"target_loop_id", record.Target.LoopID,
			"target_loop_name", record.Target.Name,
			"error", err,
		)
		ack()
		return
	}
	ack()

	d.logger.Info("mqtt wake delivered to target loop",
		"partition", partition,
		"dedup_key", item.DedupKey,
		"target_loop_id", record.Target.LoopID,
		"target_loop_name", record.Target.Name,
	)
}

// Sweep drains every mqtt-wake partition that still holds pending
// records — the boot-time recovery for enqueues whose debounce was
// pending when the process died. Call after the loop registry has
// hydrated so targets resolve. Partitions touched here are also
// registered so subsequent traffic debounces normally.
func (d *mqttWakeDispatcher) Sweep(ctx context.Context) {
	partitions, err := d.queue.Consumers(ctx, mqttWakePartitionPrefix)
	if err != nil {
		d.logger.Warn("mqtt wake boot sweep failed to enumerate partitions", "error", err)
		return
	}
	for _, partition := range partitions {
		d.ensureRegistered(partition)
		d.drainSerialized(partition)
	}
	if len(partitions) > 0 {
		d.logger.Info("mqtt wake boot sweep drained pending partitions", "partitions", len(partitions))
	}
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

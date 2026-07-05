package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// queueWakeDeliveryTimeout bounds each of the dispatcher's queue
// hops: the durable Enqueue at ingress and each bus.Send during a
// drain. The actual agent work happens in the target loop's next
// iteration on its own clock — this only bounds the handoff hops.
const queueWakeDeliveryTimeout = 30 * time.Second

// queueWakeDrainBatch bounds one Peek pass during a drain. The drain
// loops until the partition is empty, so this is a memory bound per
// pass, not a delivery cap.
const queueWakeDrainBatch = 50

// queuedWakeRecord is the shared loopqueue payload for one pending
// wake: the resolved target plus the event to deliver. Stored durably
// at ingress and replayed at drain, so a crash between trigger and
// loop delivery doesn't lose the wake, and a burst coalesces via the
// queue's dedup instead of amplifying into a wake per trigger.
type queuedWakeRecord struct {
	Target messages.LoopWakeTarget   `json:"target"`
	Event  messages.LoopEventPayload `json:"event"`
}

// queuedWakeDispatcher is the shared trigger→enqueue→debounced-drain→
// bus chassis (#1033): MQTT wakes and subscription state-change wakes
// (#1211) are both instances, differing only in partition prefix,
// ingress logic, and an optional per-delivery decorate hook. Drains
// are serialized per partition because loopqueue.Peek does not claim
// items — a debounce fire racing a boot Sweep would otherwise replay
// the same records twice.
type queuedWakeDispatcher struct {
	queue  *loopqueue.Store
	bus    *messages.Bus
	logger *slog.Logger

	// prefix namespaces this instance's partitions. Partition strings
	// are internal routing keys — deliberately NOT bare loop names,
	// so a wake target that is also a model-facing queue consumer
	// (archivist-style queue_pull) never finds plumbing records in
	// its own partition.
	prefix string

	// source names the event source on constructed envelopes
	// ("mqtt_wake", "subscription_wake").
	source string

	// decorate, when non-nil, runs on each event at delivery time —
	// the seam for delivery-relative fields like the wake payload's
	// "ago" delta, which would go stale if rendered at ingress.
	decorate func(*messages.LoopEventPayload)

	mu         sync.Mutex
	registered map[string]registeredWake
	draining   map[string]*sync.Mutex
}

// registeredWake remembers a partition's current debounce shape so
// re-registration only happens when the values actually change.
type registeredWake struct {
	debounce time.Duration
	maxWait  time.Duration
}

func newQueuedWakeDispatcher(queue *loopqueue.Store, bus *messages.Bus, prefix, source string, decorate func(*messages.LoopEventPayload), logger *slog.Logger) *queuedWakeDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &queuedWakeDispatcher{
		queue:      queue,
		bus:        bus,
		logger:     logger,
		prefix:     prefix,
		source:     source,
		decorate:   decorate,
		registered: make(map[string]registeredWake),
		draining:   make(map[string]*sync.Mutex),
	}
}

// register attaches (or retunes) the partition's WakeOnEnqueue
// debounce. The fire callback must not block (loopqueue contract), so
// it hands off to a goroutine that runs the serialized drain.
func (d *queuedWakeDispatcher) register(partition string, debounce, maxWait time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.registered[partition]; ok && cur.debounce == debounce && cur.maxWait == maxWait {
		return
	}
	d.registered[partition] = registeredWake{debounce: debounce, maxWait: maxWait}
	d.queue.SetWakeOnEnqueue(partition, debounce, maxWait, func() {
		go d.drainSerialized(partition)
	})
}

// deregister removes a partition's wake registration (its pending
// items, if any, remain durable and are found by the next Sweep).
func (d *queuedWakeDispatcher) deregister(partition string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.registered[partition]; !ok {
		return
	}
	delete(d.registered, partition)
	d.queue.SetWakeOnEnqueue(partition, 0, 0, nil)
}

// registeredPartitions snapshots the currently registered partitions.
func (d *queuedWakeDispatcher) registeredPartitions() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.registered))
	for p := range d.registered {
		out = append(out, p)
	}
	return out
}

// enqueue stores one record durably, arming the partition's debounce.
func (d *queuedWakeDispatcher) enqueue(partition, dedupKey string, priority int, record queuedWakeRecord) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), queueWakeDeliveryTimeout)
	defer cancel()
	return d.queue.Enqueue(ctx, partition, dedupKey, priority, payload)
}

// partitionLock returns the per-partition drain mutex, creating it on
// first use.
func (d *queuedWakeDispatcher) partitionLock(partition string) *sync.Mutex {
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
func (d *queuedWakeDispatcher) drainSerialized(partition string) {
	lock := d.partitionLock(partition)
	lock.Lock()
	defer lock.Unlock()
	d.drain(partition)
}

// drain delivers a partition's pending records to the message bus in
// drain order and acks each attempt. Delivery failures are logged and
// acked anyway — parity with the pre-queue direct dispatch, where a
// failed bus send dropped the message; the queue adds crash
// durability and burst coalescing, not retry semantics. A record that
// no longer unmarshals is acked and skipped for the same reason.
func (d *queuedWakeDispatcher) drain(partition string) {
	ctx := context.Background()
	for {
		items, err := d.queue.Peek(ctx, partition, queueWakeDrainBatch)
		if err != nil {
			d.logger.Warn("queued wake drain peek failed", "partition", partition, "error", err)
			return
		}
		if len(items) == 0 {
			return
		}
		for _, item := range items {
			d.deliver(partition, item)
		}
		if len(items) < queueWakeDrainBatch {
			return
		}
	}
}

// deliver replays one queued record onto the message bus, then acks it.
func (d *queuedWakeDispatcher) deliver(partition string, item loopqueue.Item) {
	ack := func() {
		if err := d.queue.Ack(context.Background(), partition, item.DedupKey); err != nil {
			d.logger.Warn("queued wake ack failed", "partition", partition, "dedup_key", item.DedupKey, "error", err)
		}
	}

	var record queuedWakeRecord
	if err := json.Unmarshal(item.Payload, &record); err != nil {
		d.logger.Warn("queued wake record unmarshal failed, dropping",
			"partition", partition, "dedup_key", item.DedupKey, "error", err)
		ack()
		return
	}
	if d.decorate != nil {
		d.decorate(&record.Event)
	}

	env, err := messages.NewEventSourceEnvelope(
		messages.Identity{Kind: messages.IdentitySystem, Name: d.source},
		record.Target,
		d.source,
		[]messages.LoopEventPayload{record.Event},
	)
	if err != nil {
		d.logger.Warn("queued wake envelope construction failed, dropping",
			"partition", partition, "dedup_key", item.DedupKey, "error", err)
		ack()
		return
	}

	deliveryCtx, cancel := context.WithTimeout(context.Background(), queueWakeDeliveryTimeout)
	defer cancel()
	if _, err := d.bus.Send(deliveryCtx, env); err != nil {
		d.logger.Warn("queued wake delivery failed, dropping",
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

	d.logger.Info("queued wake delivered to target loop",
		"partition", partition,
		"dedup_key", item.DedupKey,
		"source", d.source,
		"target_loop_id", record.Target.LoopID,
		"target_loop_name", record.Target.Name,
	)
}

// Sweep drains every partition under this dispatcher's prefix that
// still holds pending records — the boot-time recovery for enqueues
// whose debounce was pending when the process died. Call after the
// loop registry has hydrated so targets resolve.
func (d *queuedWakeDispatcher) Sweep(ctx context.Context) {
	partitions, err := d.queue.Consumers(ctx, d.prefix)
	if err != nil {
		d.logger.Warn("queued wake boot sweep failed to enumerate partitions", "prefix", d.prefix, "error", err)
		return
	}
	for _, partition := range partitions {
		d.drainSerialized(partition)
	}
	if len(partitions) > 0 {
		d.logger.Info("queued wake boot sweep drained pending partitions", "prefix", d.prefix, "partitions", len(partitions))
	}
}

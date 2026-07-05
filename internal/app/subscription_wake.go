package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/awareness"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// subWakePartitionPrefix namespaces the subscription wake feed's
// loopqueue partitions (#1211), disjoint from mqtt-wake: so the two
// chassis instances never cross-drain.
const subWakePartitionPrefix = "sub-wake:"

// wakeWatch is one compiled wake-feed entry: a subscription row that
// declared wake, resolved to the loop it wakes and the debounce it
// asked for. Glob targets match at event time via the shared glob
// primitive.
type wakeWatch struct {
	owner    string
	target   string // entity id or glob (registry targets are rejected upstream)
	isGlob   bool
	debounce time.Duration
}

// subscriptionWakeFeeder implements the #1211 wake feed: state
// changes on wake-subscribed entities enqueue durable, entity-deduped
// records into the owning loop's sub-wake partition, and the shared
// queued-wake dispatcher's debounced drain wakes the loop with a
// coalesced batch. Simple native change triggers only — HA-side
// derivation (compound conditions, zone dwell, templates) remains
// #1183's automation→MQTT→wake pipeline; both drain the same
// loopqueue chassis.
type subscriptionWakeFeeder struct {
	dispatch *queuedWakeDispatcher
	store    *awareness.WatchlistStore
	loops    *looppkg.Registry
	// translate renders raw states into the class-aware vocabulary at
	// ingress (contextfmt.SemanticState in production wiring), so the
	// wake payload reads closed→open like every other surface.
	translate func(domain, deviceClass, state string) string
	logger    *slog.Logger

	// defaultDebounce is the window used for wake subscriptions that
	// don't ask for one; zero falls back to
	// [loopqueue.DefaultWakeDebounce]. Tests shrink it.
	defaultDebounce time.Duration

	mu      sync.RWMutex
	watches []wakeWatch
}

func newSubscriptionWakeFeeder(
	queue *loopqueue.Store,
	bus *messages.Bus,
	store *awareness.WatchlistStore,
	loops *looppkg.Registry,
	translate func(domain, deviceClass, state string) string,
	logger *slog.Logger,
) *subscriptionWakeFeeder {
	if logger == nil {
		logger = slog.Default()
	}
	f := &subscriptionWakeFeeder{
		store:     store,
		loops:     loops,
		translate: translate,
		logger:    logger,
	}
	f.dispatch = newQueuedWakeDispatcher(queue, bus, subWakePartitionPrefix, "subscription_wake", decorateWakeEvent, logger)
	return f
}

// decorateWakeEvent renders the wake payload's delivery-relative
// summary: {entity, from, to, ago} in the class-aware vocabulary,
// with ago computed at delivery time so a record that waited out a
// debounce (or a crash) reports how stale it actually is.
func decorateWakeEvent(event *messages.LoopEventPayload) {
	event.Summary = promptfmt.MarshalCompact(map[string]any{
		"entity": event.Metadata["entity"],
		"from":   event.Metadata["from"],
		"to":     event.Metadata["to"],
		"ago":    promptfmt.FormatDeltaOnly(event.ObservedAt, time.Now()),
	})
}

// Rebuild recompiles the wake index from the subscription registry:
// wake-declaring rows owned by loops, with per-consumer debounce
// derived as the minimum positive ask among that loop's wake
// subscriptions (one wake drains everything pending, so the
// twitchiest subscription governs the shared cadence). Registered
// partitions are retuned in place; partitions whose loop no longer
// has wake subscriptions are deregistered (their pending items stay
// durable for the boot sweep). Called from the same hook that
// rebuilds the ingestion filter, so the index tracks every registry
// mutation.
func (f *subscriptionWakeFeeder) Rebuild() {
	rows, err := f.store.ListAll()
	if err != nil {
		f.logger.Warn("subscription wake index rebuild failed; keeping previous index", "error", err)
		return
	}

	var watches []wakeWatch
	debounces := make(map[string]time.Duration)
	for _, row := range rows {
		if !row.Wake {
			continue
		}
		owner := strings.TrimSpace(row.Owner)
		if owner == "" || owner == awareness.OwnerSystem {
			// Nobody to wake: the tool boundaries reject these, this
			// is the backstop for rows arriving by other routes.
			continue
		}
		if awareness.ParseSubscriptionTarget(row.EntityID).IsRegistryTarget() {
			continue
		}
		if live := f.loops.GetByName(owner); live != nil && live.Operation() == looppkg.OperationContainer {
			f.logger.Warn("wake subscription owned by a container — containers never iterate, skipping",
				"owner", owner, "entity_id", row.EntityID)
			continue
		}
		debounce := time.Duration(row.WakeDebounceSeconds) * time.Second
		watches = append(watches, wakeWatch{
			owner:    owner,
			target:   row.EntityID,
			isGlob:   homeassistant.IsEntityGlob(row.EntityID),
			debounce: debounce,
		})
		effective := debounce
		if effective <= 0 {
			effective = f.defaultDebounce
		}
		if effective <= 0 {
			effective = loopqueue.DefaultWakeDebounce
		}
		if cur, ok := debounces[owner]; !ok || effective < cur {
			debounces[owner] = effective
		}
	}

	// Retune live partitions and drop registrations whose loop no
	// longer declares any wake subscription.
	current := make(map[string]bool, len(debounces))
	for owner, debounce := range debounces {
		partition := subWakePartition(owner)
		current[partition] = true
		maxWait := loopqueue.DefaultWakeMaxWait
		if capped := 4 * debounce; capped > maxWait {
			maxWait = capped
		}
		f.dispatch.register(partition, debounce, maxWait)
	}
	for _, partition := range f.dispatch.registeredPartitions() {
		if !current[partition] {
			f.dispatch.deregister(partition)
		}
	}

	f.mu.Lock()
	f.watches = watches
	f.mu.Unlock()
}

// HandleStateChange is the state-watcher tap: called for every change
// that passed the ingestion filter and rate limiter (wake
// subscriptions derive their entities into that filter, so their
// changes are guaranteed to arrive here). Each matching wake
// subscription enqueues one entity-deduped record for its owner —
// latest change wins while a wake is pending, and the partition's
// debounced drain does the rest.
func (f *subscriptionWakeFeeder) HandleStateChange(entityID, oldState, newState, deviceClass string) {
	if oldState == newState {
		return
	}
	f.mu.RLock()
	watches := f.watches
	f.mu.RUnlock()
	if len(watches) == 0 {
		return
	}

	now := time.Now()
	var from, to string
	translated := false
	for i := range watches {
		w := &watches[i]
		if w.isGlob {
			if ok, _ := homeassistant.MatchEntityGlob(w.target, entityID); !ok {
				continue
			}
		} else if w.target != entityID {
			continue
		}

		if !translated {
			from, to = oldState, newState
			if f.translate != nil {
				domain, _, _ := strings.Cut(entityID, ".")
				from = f.translate(domain, deviceClass, from)
				to = f.translate(domain, deviceClass, to)
			}
			translated = true
		}

		event := messages.LoopEventPayload{
			Source:     "subscription_wake",
			Type:       "state_change",
			ID:         fmt.Sprintf("subwake-%s-%d", entityID, now.UnixMilli()),
			Title:      entityID,
			ObservedAt: now,
			Metadata: map[string]string{
				"entity": entityID,
				"from":   from,
				"to":     to,
			},
		}
		record := queuedWakeRecord{
			Target: messages.LoopWakeTarget{Name: w.owner},
			Event:  event,
		}
		partition := subWakePartition(w.owner)
		if err := f.dispatch.enqueue(partition, "wake:"+entityID, 1, record); err != nil {
			f.logger.Warn("subscription wake enqueue failed, dropping change",
				"owner", w.owner, "entity_id", entityID, "error", err)
		}
	}
}

// Sweep drains sub-wake partitions left pending by a crash while
// their debounce was armed. Call after the loop registry has hydrated
// so targets resolve.
func (f *subscriptionWakeFeeder) Sweep(ctx context.Context) {
	f.dispatch.Sweep(ctx)
}

// subWakePartition derives the queue partition for a wake-owning loop.
func subWakePartition(owner string) string {
	return subWakePartitionPrefix + "name:" + owner
}

package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/awareness"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"

	_ "modernc.org/sqlite"
)

// newTestWakeFeeder wires a feeder over in-memory stores with a fast
// debounce and a garage-door translation so tests exercise the whole
// change → enqueue → debounced drain → bus path.
func newTestWakeFeeder(t *testing.T, bus *messages.Bus) (*subscriptionWakeFeeder, *awareness.WatchlistStore, *looppkg.Registry) {
	t.Helper()
	qdb, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open queue db: %v", err)
	}
	t.Cleanup(func() { qdb.Close() })
	queue, err := loopqueue.NewStore(qdb, nil)
	if err != nil {
		t.Fatalf("new queue: %v", err)
	}

	sdb, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open store db: %v", err)
	}
	t.Cleanup(func() { sdb.Close() })
	store, err := awareness.NewWatchlistStore(sdb, nil)
	if err != nil {
		t.Fatalf("new watchlist store: %v", err)
	}

	registry := looppkg.NewRegistry()
	translate := func(domain, deviceClass, state string) string {
		if domain == "binary_sensor" && deviceClass == "garage_door" {
			switch state {
			case "on":
				return "open"
			case "off":
				return "closed"
			}
		}
		return state
	}
	f := newSubscriptionWakeFeeder(queue, bus, store, registry, translate, nil)
	f.defaultDebounce = 30 * time.Millisecond
	return f, store, registry
}

// TestSubscriptionWakeFeedEndToEnd covers the #1211 payoff: a wake
// subscription's entity changes, the owning loop receives one
// debounced event-source envelope whose payload speaks the class-aware
// {entity, from, to, ago} vocabulary, and the partition drains clean.
func TestSubscriptionWakeFeedEndToEnd(t *testing.T) {
	bus, captured := captureBus()
	f, store, _ := newTestWakeFeeder(t, bus)

	if err := store.Upsert("ranch_climate_watch", looppkg.EntitySubscription{
		EntityID: "binary_sensor.garage_bay_3",
		Wake:     true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	f.Rebuild()

	f.HandleStateChange("binary_sensor.garage_bay_3", "off", "on", "garage_door")

	got := waitFor(t, captured, 1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 wake envelope, got %d", len(got))
	}
	env := got[0]
	if env.To.Target != "ranch_climate_watch" {
		t.Errorf("target = %q, want the owning loop", env.To.Target)
	}
	payload, ok := env.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload.Kind != "event_source" || len(payload.Events) != 1 {
		t.Fatalf("payload = %+v, want one event_source event", payload)
	}
	event := payload.Events[0]
	if event.Source != "subscription_wake" || event.Type != "state_change" {
		t.Errorf("event source/type = %s/%s", event.Source, event.Type)
	}
	// Class-aware vocabulary with a delivery-time ago delta.
	for _, want := range []string{`"entity":"binary_sensor.garage_bay_3"`, `"from":"closed"`, `"to":"open"`, `"ago":`} {
		if !strings.Contains(event.Summary, want) {
			t.Errorf("summary = %q, missing %s", event.Summary, want)
		}
	}

	pending, err := f.dispatch.queue.PendingCount(context.Background(), subWakePartition("ranch_climate_watch"))
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending = %d after delivery, want drained partition", pending)
	}
}

// TestSubscriptionWakeCoalescesBurst pins the wakestorm discipline: a
// chattering entity produces ONE wake per debounce window, carrying
// the latest transition.
func TestSubscriptionWakeCoalescesBurst(t *testing.T) {
	bus, captured := captureBus()
	f, store, _ := newTestWakeFeeder(t, bus)

	if err := store.Upsert("watcher", looppkg.EntitySubscription{
		EntityID: "sensor.chatty",
		Wake:     true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	f.Rebuild()

	for i := 0; i < 6; i++ {
		f.HandleStateChange("sensor.chatty", "a", string(rune('b'+i)), "")
		time.Sleep(3 * time.Millisecond)
	}

	waitFor(t, captured, 1, 2*time.Second)
	time.Sleep(150 * time.Millisecond)
	got := captured()
	if len(got) != 1 {
		t.Fatalf("burst delivered %d wakes, want 1 coalesced", len(got))
	}
	event := got[0].Payload.(messages.LoopNotifyPayload).Events[0]
	if !strings.Contains(event.Summary, `"to":"g"`) {
		t.Errorf("summary = %q, want latest transition to win", event.Summary)
	}
}

// TestSubscriptionWakeGlobAndIndexGuards covers glob matching plus the
// index backstops: global/system rows and container owners never wake.
func TestSubscriptionWakeGlobAndIndexGuards(t *testing.T) {
	bus, captured := captureBus()
	f, store, registry := newTestWakeFeeder(t, bus)

	container, err := looppkg.New(looppkg.Config{
		Name:      "ranch_container",
		Operation: looppkg.OperationContainer,
	}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := registry.Register(container); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Glob wake for a real loop owner.
	if err := store.Upsert("door_watcher", looppkg.EntitySubscription{
		EntityID: "binary_sensor.*door*",
		Wake:     true,
	}); err != nil {
		t.Fatalf("upsert glob: %v", err)
	}
	// Backstop rows that must be ignored: global, system, container-owned.
	if err := store.Upsert(awareness.OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.global", Wake: true}); err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	if err := store.Upsert(awareness.OwnerSystem, looppkg.EntitySubscription{EntityID: "person.alice", Wake: true, Mode: looppkg.SubscriptionModeIngest}); err != nil {
		t.Fatalf("upsert system: %v", err)
	}
	if err := store.Upsert("ranch_container", looppkg.EntitySubscription{EntityID: "sensor.container_owned", Wake: true}); err != nil {
		t.Fatalf("upsert container-owned: %v", err)
	}
	f.Rebuild()

	f.HandleStateChange("binary_sensor.front_door", "off", "on", "")
	f.HandleStateChange("sensor.global", "1", "2", "")
	f.HandleStateChange("sensor.container_owned", "1", "2", "")

	waitFor(t, captured, 1, 2*time.Second)
	time.Sleep(150 * time.Millisecond)
	got := captured()
	if len(got) != 1 {
		t.Fatalf("delivered %d wakes, want only the glob match", len(got))
	}
	if got[0].To.Target != "door_watcher" {
		t.Errorf("target = %q, want door_watcher", got[0].To.Target)
	}
}

// TestSubscriptionWakeDebounceDerivation pins the per-consumer window
// math: the twitchiest wake subscription governs a loop's partition
// debounce, and loops that stop declaring wake are deregistered.
func TestSubscriptionWakeDebounceDerivation(t *testing.T) {
	bus, _ := captureBus()
	f, store, _ := newTestWakeFeeder(t, bus)

	if err := store.Upsert("watcher", looppkg.EntitySubscription{
		EntityID: "sensor.slow", Wake: true, WakeDebounceSeconds: 60,
	}); err != nil {
		t.Fatalf("upsert slow: %v", err)
	}
	if err := store.Upsert("watcher", looppkg.EntitySubscription{
		EntityID: "sensor.fast", Wake: true, WakeDebounceSeconds: 5,
	}); err != nil {
		t.Fatalf("upsert fast: %v", err)
	}
	f.Rebuild()

	partition := subWakePartition("watcher")
	f.dispatch.mu.Lock()
	reg, ok := f.dispatch.registered[partition]
	f.dispatch.mu.Unlock()
	if !ok {
		t.Fatal("watcher partition not registered")
	}
	if reg.debounce != 5*time.Second {
		t.Errorf("debounce = %v, want the twitchiest ask (5s)", reg.debounce)
	}
	if reg.maxWait != 4*5*time.Second {
		t.Errorf("maxWait = %v, want 4× the derived debounce", reg.maxWait)
	}

	// Dropping the wake flag deregisters the partition.
	if err := store.Remove("watcher", "sensor.slow"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := store.Remove("watcher", "sensor.fast"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	f.Rebuild()
	f.dispatch.mu.Lock()
	_, still := f.dispatch.registered[partition]
	f.dispatch.mu.Unlock()
	if still {
		t.Error("partition still registered after its wake subscriptions were removed")
	}
}

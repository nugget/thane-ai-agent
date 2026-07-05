package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"

	_ "modernc.org/sqlite"
)

func newTestWakeStore(t *testing.T) *mqtt.SubscriptionStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := mqtt.NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("new subscription store: %v", err)
	}
	return store
}

// captureBus stands in for a wired message bus during dispatch tests:
// it records every envelope it accepts so the test can assert on
// shape without needing a live route handler. Stubs out the loop
// destination so [messages.Bus.Send] doesn't fail on unrouted loops.
func captureBus() (*messages.Bus, func() []messages.Envelope) {
	bus := messages.NewBus(nil)
	var (
		mu       sync.Mutex
		captured []messages.Envelope
	)
	bus.AddAuditFunc(func(_ context.Context, env messages.Envelope, _ *messages.DeliveryResult, _ error) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, env)
	})
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		return messages.DeliveryResult{Envelope: env, Status: messages.DeliveryDelivered}, nil
	})
	return bus, func() []messages.Envelope {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]messages.Envelope, len(captured))
		copy(cp, captured)
		return cp
	}
}

// newTestDispatcher builds a dispatcher over an in-memory loopqueue
// with a fast debounce so tests observe the full enqueue → debounced
// drain → bus path in tens of milliseconds.
func newTestDispatcher(t *testing.T, bus *messages.Bus, registry *looppkg.Registry) *mqttWakeDispatcher {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	queue, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new loopqueue store: %v", err)
	}
	if registry == nil {
		registry = looppkg.NewRegistry()
	}
	d := newMQTTWakeDispatcher(queue, mqttWakeDeps{
		registry:   registry,
		messageBus: bus,
		eventBus:   events.New(),
	}, nil)
	d.debounce = 30 * time.Millisecond
	d.maxWait = 300 * time.Millisecond
	return d
}

func waitFor(t *testing.T, snapshot func() []messages.Envelope, n int, timeout time.Duration) []messages.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := snapshot(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return snapshot()
}

func TestMQTTWakeHandlerDispatchesViaWakeTarget(t *testing.T) {
	store := newTestWakeStore(t)

	target := messages.LoopWakeTarget{Name: "home_security", Priority: messages.PriorityNormal}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "frigate/+/events", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	bus, captured := captureBus()
	dispatcher := newTestDispatcher(t, bus, nil)

	handler := mqttWakeHandler(store, nil, nil, dispatcher)
	handler("frigate/front/events", []byte(`{"event":"motion"}`))

	got := waitFor(t, captured, 1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 envelope delivered, got %d", len(got))
	}
	env := got[0]
	if env.To.Target != "home_security" {
		t.Errorf("envelope target = %q, want home_security", env.To.Target)
	}
	if env.Type != messages.TypeSignal {
		t.Errorf("envelope type = %q, want signal", env.Type)
	}
	payload, ok := env.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", env.Payload)
	}
	if payload.Kind != "event_source" {
		t.Errorf("payload.Kind = %q, want event_source", payload.Kind)
	}
	if len(payload.Events) != 1 {
		t.Fatalf("payload.Events len = %d, want 1", len(payload.Events))
	}
	event := payload.Events[0]
	if event.Source != "mqtt_wake" {
		t.Errorf("event.Source = %q, want mqtt_wake", event.Source)
	}
	if event.Title != "frigate/front/events" {
		t.Errorf("event.Title = %q, want the topic", event.Title)
	}

	// The queue partition drains fully — nothing left pending.
	pending, err := dispatcher.queue.PendingCount(context.Background(), mqttWakePartition(target))
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending = %d after delivery, want acked-empty partition", pending)
	}
}

func TestMQTTWakeHandlerFanOutDelivers(t *testing.T) {
	store := newTestWakeStore(t)

	tA := messages.LoopWakeTarget{Name: "handler_a"}
	tB := messages.LoopWakeTarget{Name: "handler_b"}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "sensors/+/reading", WakeLoop: &tA},
		{Topic: "sensors/+/reading", WakeLoop: &tB},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	bus, captured := captureBus()
	dispatcher := newTestDispatcher(t, bus, nil)

	handler := mqttWakeHandler(store, nil, nil, dispatcher)
	handler("sensors/temp/reading", []byte(`{"value":22}`))

	got := waitFor(t, captured, 2, 2*time.Second)
	if len(got) != 2 {
		t.Fatalf("expected 2 fan-out envelopes, got %d", len(got))
	}
	targets := map[string]bool{got[0].To.Target: true, got[1].To.Target: true}
	if !targets["handler_a"] || !targets["handler_b"] {
		t.Errorf("envelope targets = %v, want both handler_a and handler_b", targets)
	}
}

// TestMQTTWakeBurstCoalesces pins the #1033 point: a burst on one
// subscription+topic inside the debounce window collapses into ONE
// delivered envelope carrying the latest payload, instead of one wake
// per message.
func TestMQTTWakeBurstCoalesces(t *testing.T) {
	store := newTestWakeStore(t)

	target := messages.LoopWakeTarget{Name: "burst_handler"}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "chatty/topic", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	bus, captured := captureBus()
	dispatcher := newTestDispatcher(t, bus, nil)
	handler := mqttWakeHandler(store, nil, nil, dispatcher)

	for i := 0; i < 5; i++ {
		handler("chatty/topic", []byte(`{"seq":`+string(rune('0'+i))+`}`))
		time.Sleep(3 * time.Millisecond)
	}

	waitFor(t, captured, 1, 2*time.Second)
	// Allow the debounce to fully settle, then confirm no extra wakes.
	time.Sleep(150 * time.Millisecond)
	got := captured()
	if len(got) != 1 {
		t.Fatalf("burst delivered %d envelopes, want 1 coalesced", len(got))
	}
	payload := got[0].Payload.(messages.LoopNotifyPayload)
	if len(payload.Events) != 1 || !strings.Contains(payload.Events[0].Summary, `"seq":4`) {
		t.Errorf("coalesced event = %+v, want latest payload to win", payload.Events)
	}
}

// TestMQTTWakeSelfAddressing covers the payload target_loop override:
// a resolvable self-address re-routes the wake, an unresolvable one
// falls back to the subscription's static target.
func TestMQTTWakeSelfAddressing(t *testing.T) {
	store := newTestWakeStore(t)

	target := messages.LoopWakeTarget{Name: "shared_wake_triage", Instructions: "triage it"}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "thane/wake", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	registry := looppkg.NewRegistry()
	ranch, err := looppkg.New(looppkg.Config{
		Name:      "ranch_climate_watch",
		Operation: looppkg.OperationContainer,
	}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new loop: %v", err)
	}
	if err := registry.Register(ranch); err != nil {
		t.Fatalf("register: %v", err)
	}

	bus, captured := captureBus()
	dispatcher := newTestDispatcher(t, bus, registry)
	handler := mqttWakeHandler(store, nil, nil, dispatcher)

	handler("thane/wake", []byte(`{"target_loop":"ranch_climate_watch","note":"tank low"}`))
	got := waitFor(t, captured, 1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(got))
	}
	if got[0].To.Target != "ranch_climate_watch" {
		t.Errorf("self-addressed target = %q, want ranch_climate_watch", got[0].To.Target)
	}
	payload := got[0].Payload.(messages.LoopNotifyPayload)
	if len(payload.Events) != 1 || payload.Events[0].Metadata["target_loop"] != "ranch_climate_watch" {
		t.Errorf("event metadata = %+v, want target_loop recorded", payload.Events)
	}

	// Unresolvable self-address falls back to the subscription target.
	handler("thane/wake", []byte(`{"target_loop":"no_such_loop"}`))
	got = waitFor(t, captured, 2, 2*time.Second)
	if len(got) != 2 {
		t.Fatalf("expected fallback envelope, got %d total", len(got))
	}
	if got[1].To.Target != "shared_wake_triage" {
		t.Errorf("fallback target = %q, want shared_wake_triage", got[1].To.Target)
	}
}

// TestMQTTWakeBootSweep covers crash recovery: records enqueued with
// no live debounce (the process died before the wake fired) are
// delivered by Sweep once targets resolve.
func TestMQTTWakeBootSweep(t *testing.T) {
	bus, captured := captureBus()
	dispatcher := newTestDispatcher(t, bus, nil)

	target := messages.LoopWakeTarget{Name: "recovered_handler"}
	record, err := json.Marshal(mqttQueuedWake{
		Target: target,
		Event: messages.LoopEventPayload{
			Source: "mqtt_wake", Type: "message",
			ID: "mqtt-crashed-1", Title: "a/topic", Summary: "left behind",
		},
	})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	// Enqueue directly — simulating a pre-crash write whose debounce
	// state died with the process (no registration on this store yet).
	if err := dispatcher.queue.Enqueue(context.Background(), mqttWakePartition(target), "mqtt:crashed:a/topic", 1, record); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	dispatcher.Sweep(context.Background())

	got := captured()
	if len(got) != 1 {
		t.Fatalf("sweep delivered %d envelopes, want 1", len(got))
	}
	if got[0].To.Target != "recovered_handler" {
		t.Errorf("swept target = %q, want recovered_handler", got[0].To.Target)
	}
	pending, err := dispatcher.queue.PendingCount(context.Background(), mqttWakePartition(target))
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending = %d after sweep, want 0", pending)
	}
}

func TestMQTTWakeHandlerNoMatchFallback(t *testing.T) {
	store := newTestWakeStore(t)

	var fallbackCalled bool
	fallback := func(topic string, payload []byte) {
		fallbackCalled = true
	}

	bus, captured := captureBus()
	handler := mqttWakeHandler(store, fallback, nil, newTestDispatcher(t, bus, nil))

	handler("unmatched/topic", []byte("data"))

	if !fallbackCalled {
		t.Error("expected fallback to be called")
	}
	if got := captured(); len(got) != 0 {
		t.Errorf("expected 0 envelopes for unmatched topic, got %d", len(got))
	}
}

func TestMQTTWakeHandlerNoDispatcherDropsMessage(t *testing.T) {
	store := newTestWakeStore(t)

	target := messages.LoopWakeTarget{Name: "handler"}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "test/topic", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	handler := mqttWakeHandler(store, nil, nil, nil)
	handler("test/topic", []byte("data"))
	// No dispatcher configured — the handler should log and return
	// without blocking or panicking. Wait briefly to confirm nothing
	// crashes asynchronously.
	time.Sleep(100 * time.Millisecond)
}

func TestSanitizePayloadTruncates(t *testing.T) {
	big := strings.Repeat("a", maxWakePayloadBytes+10)
	got := sanitizePayload([]byte(big))
	if !strings.Contains(got, "[Truncated:") {
		t.Errorf("expected truncation marker, got %q", got[:60])
	}
}

func TestSanitizePayloadEmpty(t *testing.T) {
	if got := sanitizePayload(nil); got != "" {
		t.Errorf("sanitizePayload(nil) = %q, want empty", got)
	}
}

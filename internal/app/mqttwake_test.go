package app

import (
	"context"
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

	_ "github.com/mattn/go-sqlite3"
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
	deps := mqttWakeDeps{
		registry:   looppkg.NewRegistry(),
		messageBus: bus,
		eventBus:   events.New(),
	}

	handler := mqttWakeHandler(store, nil, nil, deps)
	handler("frigate/front/events", []byte(`{"event":"motion"}`))

	got := waitFor(t, captured, 1, 500*time.Millisecond)
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
	deps := mqttWakeDeps{
		registry:   looppkg.NewRegistry(),
		messageBus: bus,
		eventBus:   events.New(),
	}

	handler := mqttWakeHandler(store, nil, nil, deps)
	handler("sensors/temp/reading", []byte(`{"value":22}`))

	got := waitFor(t, captured, 2, 500*time.Millisecond)
	if len(got) != 2 {
		t.Fatalf("expected 2 fan-out envelopes, got %d", len(got))
	}
	targets := map[string]bool{got[0].To.Target: true, got[1].To.Target: true}
	if !targets["handler_a"] || !targets["handler_b"] {
		t.Errorf("envelope targets = %v, want both handler_a and handler_b", targets)
	}
}

func TestMQTTWakeHandlerNoMatchFallback(t *testing.T) {
	store := newTestWakeStore(t)

	var fallbackCalled bool
	fallback := func(topic string, payload []byte) {
		fallbackCalled = true
	}

	bus, captured := captureBus()
	handler := mqttWakeHandler(store, fallback, nil, mqttWakeDeps{messageBus: bus})

	handler("unmatched/topic", []byte("data"))

	if !fallbackCalled {
		t.Error("expected fallback to be called")
	}
	if got := captured(); len(got) != 0 {
		t.Errorf("expected 0 envelopes for unmatched topic, got %d", len(got))
	}
}

func TestMQTTWakeHandlerNoBusDropsMessage(t *testing.T) {
	store := newTestWakeStore(t)

	target := messages.LoopWakeTarget{Name: "handler"}
	if err := store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "test/topic", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	handler := mqttWakeHandler(store, nil, nil, mqttWakeDeps{})
	handler("test/topic", []byte("data"))
	// No bus configured — the handler should log and return without
	// blocking or panicking. Wait briefly to confirm nothing crashes
	// asynchronously.
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

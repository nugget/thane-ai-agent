package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/runtime/metacognitive"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

func TestLogAlertWakeFeederQueuesMetacognitiveAttention(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	queue, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventBus := events.New()
	feeder := newLogAlertWakeFeeder(queue, messages.NewBus(nil), eventBus, nil)
	if feeder == nil {
		t.Fatal("feeder is nil")
	}
	t.Cleanup(func() {
		feeder.dispatcher.deregister(feeder.partition())
		feeder.bus.Unsubscribe(feeder.events)
	})

	observedAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	feeder.ingest(events.Event{
		Timestamp: observedAt,
		Source:    events.SourceLog,
		Kind:      events.KindLogRecord,
		Data: map[string]any{
			"level":       "WARN",
			"message":     "illegal tool call",
			"source_file": "internal/runtime/iterate/engine.go",
			"source_line": 336,
			"loop_name":   "email-default-handler",
			"request_id":  "req-1",
		},
	})

	items, err := queue.Peek(context.Background(), feeder.partition(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queued items = %d, want 1", len(items))
	}
	var record queuedWakeRecord
	if err := json.Unmarshal(items[0].Payload, &record); err != nil {
		t.Fatal(err)
	}
	if record.Target.Name != metacognitive.DefinitionName {
		t.Fatalf("target = %q", record.Target.Name)
	}
	if record.Event.Type != "warn" || record.Event.Title != "illegal tool call" {
		t.Fatalf("event = %+v", record.Event)
	}
	if record.Event.ObservedAt != observedAt {
		t.Fatalf("observed_at = %v", record.Event.ObservedAt)
	}
}

func TestLogAlertWakeFeederCoalescesRepeatedRecords(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	queue, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	feeder := newLogAlertWakeFeeder(queue, messages.NewBus(nil), events.New(), nil)
	t.Cleanup(func() {
		feeder.dispatcher.deregister(feeder.partition())
		feeder.bus.Unsubscribe(feeder.events)
	})
	event := events.Event{
		Timestamp: time.Now(),
		Source:    events.SourceLog,
		Kind:      events.KindLogRecord,
		Data: map[string]any{
			"level":     "ERROR",
			"message":   "provider unavailable",
			"component": "model_runtime",
		},
	}
	feeder.ingest(event)
	event.Timestamp = event.Timestamp.Add(time.Second)
	feeder.ingest(event)

	items, err := queue.Peek(context.Background(), feeder.partition(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queued items = %d, want repeated fingerprint coalesced to 1", len(items))
	}
}

func TestLogAlertWakeFeederDoesNotReingestOwnWarnings(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	queue, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	feeder := newLogAlertWakeFeeder(queue, messages.NewBus(nil), events.New(), nil)
	t.Cleanup(func() {
		feeder.dispatcher.deregister(feeder.partition())
		feeder.bus.Unsubscribe(feeder.events)
	})
	feeder.ingest(events.Event{
		Timestamp: time.Now(),
		Source:    events.SourceLog,
		Kind:      events.KindLogRecord,
		Data: map[string]any{
			"level":     "WARN",
			"message":   "log alert enqueue failed",
			"component": "log_alert_wake",
		},
	})

	items, err := queue.Peek(context.Background(), feeder.partition(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("queued items = %d, want 0", len(items))
	}

	feeder.ingest(events.Event{
		Timestamp: time.Now(),
		Source:    events.SourceLog,
		Kind:      events.KindLogRecord,
		Data: map[string]any{
			"level":     "ERROR",
			"message":   "metacognitive request exhausted",
			"loop_name": metacognitive.DefinitionName,
		},
	})
	items, err = queue.Peek(context.Background(), feeder.partition(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("queued metacognitive self-alerts = %d, want 0", len(items))
	}
}

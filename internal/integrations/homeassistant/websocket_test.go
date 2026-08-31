package homeassistant

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestEnqueueEventEvictsOldestOnOverflow verifies the overflow policy:
// a full events channel evicts its oldest entry to admit the newest, so
// a burst preserves fresh state instead of a stale backlog.
func TestEnqueueEventEvictsOldestOnOverflow(t *testing.T) {
	c := NewWSClient("http://ha.invalid", "token", nil)
	c.events = make(chan Event, 2)

	for i, payload := range []string{`1`, `2`, `3`} {
		c.enqueueEvent(Event{Type: "state_changed", Data: json.RawMessage(payload)})
		if i < 2 && c.droppedEvents.Load() != 0 {
			t.Fatalf("event %d dropped before the channel was full", i+1)
		}
	}

	if got := c.droppedEvents.Load(); got != 1 {
		t.Fatalf("droppedEvents = %d, want 1", got)
	}
	want := []string{`2`, `3`}
	for i, exp := range want {
		select {
		case ev := <-c.events:
			if string(ev.Data) != exp {
				t.Errorf("queued event %d = %s, want %s (oldest should have been evicted)", i, ev.Data, exp)
			}
		default:
			t.Fatalf("expected %d queued events, channel empty at %d", len(want), i)
		}
	}
}

// TestRecordDroppedEventCoalescesWarnings verifies the log contract: an
// overflow storm emits one warning per dropLogInterval, and the next
// window's summary carries every drop folded in since the last record.
func TestRecordDroppedEventCoalescesWarnings(t *testing.T) {
	h := &captureHandler{}
	c := NewWSClient("http://ha.invalid", "token", slog.New(h))

	for range 100 {
		c.recordDroppedEvent("state_changed")
	}
	if len(h.records) != 1 {
		t.Fatalf("storm of 100 drops emitted %d warnings, want 1", len(h.records))
	}
	if got := attrUint64(t, h.records[0], "dropped_since_last"); got != 1 {
		t.Errorf("first warning dropped_since_last = %d, want 1", got)
	}

	// Age the window out and drop once more: the summary must fold in
	// the 99 silent drops plus this one.
	c.lastDropLogNano.Store(time.Now().Add(-dropLogInterval - time.Second).UnixNano())
	c.recordDroppedEvent("state_changed")
	if len(h.records) != 2 {
		t.Fatalf("aged-out window emitted %d warnings total, want 2", len(h.records))
	}
	if got := attrUint64(t, h.records[1], "dropped_since_last"); got != 100 {
		t.Errorf("second warning dropped_since_last = %d, want 100", got)
	}
	if got := attrUint64(t, h.records[1], "dropped_total"); got != 101 {
		t.Errorf("second warning dropped_total = %d, want 101", got)
	}
}

// attrUint64 extracts a uint64-valued attribute from a captured record.
func attrUint64(t *testing.T, rec slog.Record, key string) uint64 {
	t.Helper()
	var got uint64
	found := false
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			got = a.Value.Uint64()
			found = true
			return false
		}
		return true
	})
	if !found {
		t.Fatalf("warning record missing %q attribute", key)
	}
	return got
}

func TestWSClient_Integration(t *testing.T) {
	// Skip if no HA token available
	token := os.Getenv("HOMEASSISTANT_TOKEN")
	if token == "" {
		t.Skip("HOMEASSISTANT_TOKEN not set")
	}

	url := os.Getenv("HOMEASSISTANT_URL")
	if url == "" {
		url = "https://homeassistant.hollowoak.net"
	}

	client := NewWSClient(url, token, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Connect once for all tests
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Close()

	// Test area registry
	t.Run("GetAreaRegistry", func(t *testing.T) {
		areas, err := client.GetAreaRegistry(ctx)
		if err != nil {
			t.Fatalf("GetAreaRegistry failed: %v", err)
		}
		if len(areas) == 0 {
			t.Error("Expected at least one area")
		}
		t.Logf("Found %d areas", len(areas))
		for i, a := range areas {
			if i >= 5 {
				t.Logf("  ... and %d more", len(areas)-5)
				break
			}
			t.Logf("  - %s (%s)", a.Name, a.AreaID)
		}
	})

	// Test entity registry
	t.Run("GetEntityRegistry", func(t *testing.T) {
		entities, err := client.GetEntityRegistryWS(ctx)
		if err != nil {
			t.Fatalf("GetEntityRegistry failed: %v", err)
		}
		if len(entities) == 0 {
			t.Error("Expected at least one entity")
		}
		t.Logf("Found %d entities", len(entities))

		// Count entities with area assignments
		withArea := 0
		for _, e := range entities {
			if e.AreaID != "" {
				withArea++
			}
		}
		t.Logf("  %d entities have area assignments", withArea)
	})

	// Test event subscription
	t.Run("Subscribe", func(t *testing.T) {
		if err := client.Subscribe(ctx, "state_changed"); err != nil {
			t.Fatalf("Subscribe failed: %v", err)
		}

		// Wait briefly for an event (HA is usually chatty)
		select {
		case event := <-client.Events():
			t.Logf("Received event: %s", event.Type)
			if event.Type == "state_changed" {
				var data StateChangedData
				if err := json.Unmarshal(event.Data, &data); err == nil {
					t.Logf("  entity: %s", data.EntityID)
				}
			}
		case <-time.After(5 * time.Second):
			t.Log("No events received in 5s (HA might be quiet)")
		}
	})
}

package homeassistant

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

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

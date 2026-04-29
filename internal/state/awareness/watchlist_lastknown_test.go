package awareness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func TestEnrichWithLastKnownGood_NoOpForAvailableEntity(t *testing.T) {
	current := &homeassistant.State{
		EntityID:    "binary_sensor.front_door",
		State:       "off",
		LastChanged: testNow,
		Attributes:  map[string]any{"device_class": "door"},
	}
	base := `{"entity":"binary_sensor.front_door","state":"closed"}`

	// Even with a getter that would error, available entities skip the call.
	getter := &fakeHA{histErr: errors.New("should not be called")}

	result := enrichWithLastKnownGood(context.Background(), getter, base, current, testNow)
	if result != base {
		t.Errorf("available entity should pass through unchanged\ngot:  %s\nwant: %s", result, base)
	}
}

func TestEnrichWithLastKnownGood_AddsTranslatedLastState(t *testing.T) {
	current := &homeassistant.State{
		EntityID:    "binary_sensor.front_door",
		State:       "unavailable",
		LastChanged: testNow.Add(-12 * time.Minute),
		Attributes:  map[string]any{"device_class": "door"},
	}
	base := `{"entity":"binary_sensor.front_door","available":false,"reason":"unavailable","unavailable_since":"-720s","device_class":"door"}`

	getter := &fakeHA{
		history: map[string][]homeassistant.State{
			"binary_sensor.front_door": {
				{EntityID: "binary_sensor.front_door", State: "off", LastChanged: testNow.Add(-3 * time.Hour)},
				{EntityID: "binary_sensor.front_door", State: "on", LastChanged: testNow.Add(-30 * time.Minute)},
				{EntityID: "binary_sensor.front_door", State: "off", LastChanged: testNow.Add(-15 * time.Minute)},
				{EntityID: "binary_sensor.front_door", State: "unavailable", LastChanged: testNow.Add(-12 * time.Minute)},
			},
		},
	}

	result := enrichWithLastKnownGood(context.Background(), getter, base, current, testNow)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, result)
	}
	if parsed["last_state"] != "closed" {
		t.Errorf("last_state = %v, want closed (translated from off via device_class door)", parsed["last_state"])
	}
	if parsed["last_state_seen"] != "-900s" {
		t.Errorf("last_state_seen = %v, want -900s", parsed["last_state_seen"])
	}
	if parsed["available"] != false {
		t.Errorf("preserves base availability, got %v", parsed["available"])
	}
}

func TestEnrichWithLastKnownGood_SkipsTrailingSentinels(t *testing.T) {
	// HA records state="unavailable" entries at the moment an entity
	// drops out. The walk must skip those and find the most recent
	// real reading underneath.
	current := &homeassistant.State{
		EntityID:    "sensor.temperature",
		State:       "unavailable",
		LastChanged: testNow.Add(-5 * time.Minute),
		Attributes:  map[string]any{"device_class": "temperature"},
	}
	base := `{"entity":"sensor.temperature","available":false,"reason":"unavailable"}`

	getter := &fakeHA{
		history: map[string][]homeassistant.State{
			"sensor.temperature": {
				{EntityID: "sensor.temperature", State: "72.4", LastChanged: testNow.Add(-20 * time.Minute)},
				{EntityID: "sensor.temperature", State: "unavailable", LastChanged: testNow.Add(-10 * time.Minute)},
				{EntityID: "sensor.temperature", State: "unknown", LastChanged: testNow.Add(-7 * time.Minute)},
				{EntityID: "sensor.temperature", State: "unavailable", LastChanged: testNow.Add(-5 * time.Minute)},
			},
		},
	}

	result := enrichWithLastKnownGood(context.Background(), getter, base, current, testNow)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["last_state"] != "72.4" {
		t.Errorf("last_state = %v, want 72.4 (skipping the sentinel trailers)", parsed["last_state"])
	}
	if parsed["last_state_seen"] != "-1200s" {
		t.Errorf("last_state_seen = %v, want -1200s", parsed["last_state_seen"])
	}
}

func TestEnrichWithLastKnownGood_AllSentinelHistory(t *testing.T) {
	current := &homeassistant.State{
		EntityID:    "sensor.cold",
		State:       "unavailable",
		LastChanged: testNow.Add(-3 * time.Hour),
	}
	base := `{"entity":"sensor.cold","available":false,"reason":"unavailable"}`

	getter := &fakeHA{
		history: map[string][]homeassistant.State{
			"sensor.cold": {
				{EntityID: "sensor.cold", State: "unavailable", LastChanged: testNow.Add(-3 * time.Hour)},
				{EntityID: "sensor.cold", State: "unknown", LastChanged: testNow.Add(-2 * time.Hour)},
			},
		},
	}

	result := enrichWithLastKnownGood(context.Background(), getter, base, current, testNow)

	if result != base {
		t.Errorf("expected base unchanged when no real history exists, got %s", result)
	}
}

func TestEnrichWithLastKnownGood_HistoryError(t *testing.T) {
	current := &homeassistant.State{
		EntityID: "sensor.x",
		State:    "unavailable",
	}
	base := `{"entity":"sensor.x","available":false}`
	getter := &fakeHA{histErr: errors.New("recorder timeout")}

	result := enrichWithLastKnownGood(context.Background(), getter, base, current, testNow)
	if result != base {
		t.Errorf("history error should degrade silently to base, got %s", result)
	}
}

func TestProvider_UnavailableEntitySurfacesLastKnownGood(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	ha := &fakeHA{
		states: map[string]*homeassistant.State{
			"binary_sensor.front_door": {
				EntityID:    "binary_sensor.front_door",
				State:       "unavailable",
				LastChanged: now.Add(-12 * time.Minute),
				Attributes: map[string]any{
					"friendly_name": "Front Door",
					"device_class":  "door",
				},
			},
		},
		history: map[string][]homeassistant.State{
			"binary_sensor.front_door": {
				{EntityID: "binary_sensor.front_door", State: "off", LastChanged: now.Add(-2 * time.Hour)},
				{EntityID: "binary_sensor.front_door", State: "on", LastChanged: now.Add(-25 * time.Minute)},
				{EntityID: "binary_sensor.front_door", State: "off", LastChanged: now.Add(-15 * time.Minute)},
				{EntityID: "binary_sensor.front_door", State: "unavailable", LastChanged: now.Add(-12 * time.Minute)},
			},
		},
	}

	p, store := setupTestProvider(t, ha)
	if err := store.Add("binary_sensor.front_door"); err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}

	payload := decodeWatchlistPayload(t, got)
	if payload["available"] != false {
		t.Errorf("available = %v, want false", payload["available"])
	}
	if payload["last_state"] != "closed" {
		t.Errorf("last_state = %v, want closed", payload["last_state"])
	}
	if _, has := payload["last_state_seen"]; !has {
		t.Error("missing last_state_seen")
	}
}

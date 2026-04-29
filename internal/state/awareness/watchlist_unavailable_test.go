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

// fakeRegistries provides the four registry calls needed for
// unavailability enrichment, with simple slice-backed storage so
// tests can shape exactly what the renderer sees.
type fakeRegistries struct {
	entities       []homeassistant.EntityRegistryEntry
	devices        []homeassistant.DeviceRegistryEntry
	states         []homeassistant.State
	configEntries  []homeassistant.ConfigEntry
	entitiesErr    error
	devicesErr     error
	statesErr      error
	configEntryErr error
}

func (f *fakeRegistries) GetEntityRegistry(_ context.Context) ([]homeassistant.EntityRegistryEntry, error) {
	return f.entities, f.entitiesErr
}

func (f *fakeRegistries) GetDeviceRegistry(_ context.Context) ([]homeassistant.DeviceRegistryEntry, error) {
	return f.devices, f.devicesErr
}

func (f *fakeRegistries) GetStates(_ context.Context) ([]homeassistant.State, error) {
	return f.states, f.statesErr
}

func (f *fakeRegistries) GetConfigEntries(_ context.Context) ([]homeassistant.ConfigEntry, error) {
	return f.configEntries, f.configEntryErr
}

func TestEnrichUnavailable_NoOpForAvailableEntity(t *testing.T) {
	current := &homeassistant.State{
		EntityID: "binary_sensor.front_door",
		State:    "off",
	}
	base := `{"entity":"binary_sensor.front_door","state":"closed"}`
	regs := newRenderRegistries(context.Background(), &fakeRegistries{})
	if got := enrichUnavailable(base, current, regs); got != base {
		t.Errorf("available entity should be untouched, got %s", got)
	}
}

func TestEnrichUnavailable_NoOpWithoutRegistries(t *testing.T) {
	current := &homeassistant.State{
		EntityID: "binary_sensor.x",
		State:    "unavailable",
	}
	base := `{"entity":"binary_sensor.x","available":false}`
	if got := enrichUnavailable(base, current, nil); got != base {
		t.Errorf("nil registries should leave payload untouched, got %s", got)
	}
}

func TestEnrichUnavailable_AddsDeviceAndIntegration(t *testing.T) {
	current := &homeassistant.State{
		EntityID: "binary_sensor.aqara_door",
		State:    "unavailable",
	}
	base := `{"entity":"binary_sensor.aqara_door","available":false}`

	fr := &fakeRegistries{
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "binary_sensor.aqara_door", DeviceID: "dev-1", Platform: "zwave_js"},
		},
		devices: []homeassistant.DeviceRegistryEntry{
			{ID: "dev-1", Manufacturer: "Aqara", Model: "MCCGQ11LM", SWVersion: "1.0.0", Name: "Front Door Sensor"},
		},
		configEntries: []homeassistant.ConfigEntry{
			{EntryID: "cfg-1", Domain: "zwave_js", State: "loaded"},
		},
	}
	regs := newRenderRegistries(context.Background(), fr)

	result := enrichUnavailable(base, current, regs)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, result)
	}
	device, ok := parsed["device"].(map[string]any)
	if !ok {
		t.Fatalf("device missing or wrong shape: %#v", parsed["device"])
	}
	if device["manufacturer"] != "Aqara" {
		t.Errorf("device.manufacturer = %v, want Aqara", device["manufacturer"])
	}
	if device["model"] != "MCCGQ11LM" {
		t.Errorf("device.model = %v, want MCCGQ11LM", device["model"])
	}
	if device["name"] != "Front Door Sensor" {
		t.Errorf("device.name = %v, want Front Door Sensor", device["name"])
	}

	integration, ok := parsed["integration"].(map[string]any)
	if !ok {
		t.Fatalf("integration missing: %#v", parsed["integration"])
	}
	if integration["name"] != "zwave_js" {
		t.Errorf("integration.name = %v, want zwave_js", integration["name"])
	}
	if integration["state"] != "loaded" {
		t.Errorf("integration.state = %v, want loaded", integration["state"])
	}
}

func TestEnrichUnavailable_DeviceAliveTrueWhenSiblingUp(t *testing.T) {
	current := &homeassistant.State{EntityID: "binary_sensor.broken", State: "unavailable"}
	base := `{"entity":"binary_sensor.broken","available":false}`

	fr := &fakeRegistries{
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "binary_sensor.broken", DeviceID: "dev-1"},
			{EntityID: "sensor.battery", DeviceID: "dev-1"},
		},
		devices: []homeassistant.DeviceRegistryEntry{
			{ID: "dev-1", Manufacturer: "Aqara"},
		},
		states: []homeassistant.State{
			{EntityID: "binary_sensor.broken", State: "unavailable"},
			{EntityID: "sensor.battery", State: "85"},
		},
	}
	regs := newRenderRegistries(context.Background(), fr)

	result := enrichUnavailable(base, current, regs)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(result), &parsed)
	if parsed["device_alive"] != true {
		t.Errorf("device_alive = %v, want true (battery sibling reporting fresh state)", parsed["device_alive"])
	}
}

func TestEnrichUnavailable_DeviceAliveFalseWhenAllSiblingsDown(t *testing.T) {
	current := &homeassistant.State{EntityID: "binary_sensor.broken", State: "unavailable"}
	base := `{"entity":"binary_sensor.broken","available":false}`

	fr := &fakeRegistries{
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "binary_sensor.broken", DeviceID: "dev-1"},
			{EntityID: "sensor.battery", DeviceID: "dev-1"},
			{EntityID: "sensor.signal", DeviceID: "dev-1"},
		},
		devices: []homeassistant.DeviceRegistryEntry{
			{ID: "dev-1", Manufacturer: "Aqara"},
		},
		states: []homeassistant.State{
			{EntityID: "binary_sensor.broken", State: "unavailable"},
			{EntityID: "sensor.battery", State: "unavailable"},
			{EntityID: "sensor.signal", State: "unknown"},
		},
	}
	regs := newRenderRegistries(context.Background(), fr)

	result := enrichUnavailable(base, current, regs)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(result), &parsed)
	if parsed["device_alive"] != false {
		t.Errorf("device_alive = %v, want false (every sibling sentinel)", parsed["device_alive"])
	}
}

func TestEnrichUnavailable_GatewayContextWalksViaDevice(t *testing.T) {
	current := &homeassistant.State{EntityID: "binary_sensor.door", State: "unavailable"}
	base := `{"entity":"binary_sensor.door","available":false}`

	fr := &fakeRegistries{
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "binary_sensor.door", DeviceID: "sensor-dev"},
			{EntityID: "sensor.hub_uptime", DeviceID: "hub-dev"},
		},
		devices: []homeassistant.DeviceRegistryEntry{
			{ID: "sensor-dev", Manufacturer: "Aqara", ViaDeviceID: "hub-dev"},
			{ID: "hub-dev", Manufacturer: "Z-Wave JS", Name: "Z-Wave Controller"},
		},
		states: []homeassistant.State{
			{EntityID: "binary_sensor.door", State: "unavailable"},
			{EntityID: "sensor.hub_uptime", State: "unavailable"},
		},
	}
	regs := newRenderRegistries(context.Background(), fr)

	result := enrichUnavailable(base, current, regs)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	gateway, ok := parsed["gateway"].(map[string]any)
	if !ok {
		t.Fatalf("gateway missing: %#v", parsed["gateway"])
	}
	if gateway["name"] != "Z-Wave Controller" {
		t.Errorf("gateway.name = %v, want Z-Wave Controller", gateway["name"])
	}
	if gateway["online"] != false {
		t.Errorf("gateway.online = %v, want false (no hub entity reporting)", gateway["online"])
	}
}

func TestEnrichUnavailable_IntegrationFailureIsSurfaced(t *testing.T) {
	current := &homeassistant.State{EntityID: "binary_sensor.x", State: "unavailable"}
	base := `{"entity":"binary_sensor.x","available":false}`

	fr := &fakeRegistries{
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "binary_sensor.x", Platform: "zwave_js"},
		},
		configEntries: []homeassistant.ConfigEntry{
			{EntryID: "cfg-1", Domain: "zwave_js", State: "setup_error", Reason: "controller offline"},
		},
	}
	regs := newRenderRegistries(context.Background(), fr)

	result := enrichUnavailable(base, current, regs)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(result), &parsed)
	integration, ok := parsed["integration"].(map[string]any)
	if !ok {
		t.Fatalf("integration missing: %#v", parsed["integration"])
	}
	if integration["state"] != "setup_error" {
		t.Errorf("integration.state = %v, want setup_error", integration["state"])
	}
	if integration["reason"] != "controller offline" {
		t.Errorf("integration.reason = %v, want controller offline", integration["reason"])
	}
}

func TestEnrichUnavailable_RegistryFetchErrorDegradesSilently(t *testing.T) {
	current := &homeassistant.State{EntityID: "sensor.x", State: "unavailable"}
	base := `{"entity":"sensor.x","available":false}`

	fr := &fakeRegistries{entitiesErr: errors.New("network failure")}
	regs := newRenderRegistries(context.Background(), fr)

	if got := enrichUnavailable(base, current, regs); got != base {
		t.Errorf("entity registry error should leave payload untouched, got %s", got)
	}
}

func TestProvider_UnavailableEnrichmentEndToEnd(t *testing.T) {
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
	}
	regs := &fakeRegistries{
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "binary_sensor.front_door", DeviceID: "dev-1", Platform: "zwave_js"},
		},
		devices: []homeassistant.DeviceRegistryEntry{
			{ID: "dev-1", Manufacturer: "Aqara", Model: "MCCGQ11LM"},
		},
		states: []homeassistant.State{
			{EntityID: "binary_sensor.front_door", State: "unavailable"},
		},
		configEntries: []homeassistant.ConfigEntry{
			{Domain: "zwave_js", State: "loaded"},
		},
	}

	p, store := setupTestProvider(t, ha)
	p.SetRegistryClient(regs)

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
	device, ok := payload["device"].(map[string]any)
	if !ok {
		t.Fatalf("device missing in end-to-end payload: %#v", payload["device"])
	}
	if device["manufacturer"] != "Aqara" {
		t.Errorf("device.manufacturer = %v, want Aqara", device["manufacturer"])
	}
}

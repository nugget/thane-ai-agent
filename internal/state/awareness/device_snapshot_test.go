package awareness

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// fakeDeviceClient supplies the registry calls plus a per-entity state
// map for ha_device tests. An entity_id absent from states makes
// GetState return an error, exercising the "no_state" anomaly path.
type fakeDeviceClient struct {
	areas    []homeassistant.Area
	entities []homeassistant.EntityRegistryEntry
	devices  []homeassistant.DeviceRegistryEntry
	floors   []homeassistant.FloorRegistryEntry
	labels   []homeassistant.LabelRegistryEntry
	configs  []homeassistant.ConfigEntry
	states   map[string]*homeassistant.State

	devicesErr error
}

func (f *fakeDeviceClient) GetAreas(_ context.Context) ([]homeassistant.Area, error) {
	return f.areas, nil
}
func (f *fakeDeviceClient) GetEntityRegistry(_ context.Context) ([]homeassistant.EntityRegistryEntry, error) {
	return f.entities, nil
}
func (f *fakeDeviceClient) GetDeviceRegistry(_ context.Context) ([]homeassistant.DeviceRegistryEntry, error) {
	return f.devices, f.devicesErr
}
func (f *fakeDeviceClient) GetFloorRegistry(_ context.Context) ([]homeassistant.FloorRegistryEntry, error) {
	return f.floors, nil
}
func (f *fakeDeviceClient) GetLabelRegistry(_ context.Context) ([]homeassistant.LabelRegistryEntry, error) {
	return f.labels, nil
}
func (f *fakeDeviceClient) GetStates(_ context.Context) ([]homeassistant.State, error) {
	out := make([]homeassistant.State, 0, len(f.states))
	for _, s := range f.states {
		out = append(out, *s)
	}
	return out, nil
}
func (f *fakeDeviceClient) GetConfigEntries(_ context.Context) ([]homeassistant.ConfigEntry, error) {
	return f.configs, nil
}
func (f *fakeDeviceClient) GetState(_ context.Context, entityID string) (*homeassistant.State, error) {
	s, ok := f.states[entityID]
	if !ok {
		return nil, fmt.Errorf("entity %s not found", entityID)
	}
	return s, nil
}

func mkState(id, state string, attrs map[string]any) *homeassistant.State {
	return &homeassistant.State{EntityID: id, State: state, Attributes: attrs}
}

// thermostatClient builds a device with a rich, mixed-salience set of
// child entities including one sentinel (unavailable) and one with no
// state at all, for the multi-entity / availability tests.
func thermostatClient() *fakeDeviceClient {
	return &fakeDeviceClient{
		areas: []homeassistant.Area{{AreaID: "office", Name: "Office"}},
		devices: []homeassistant.DeviceRegistryEntry{
			{
				ID:                 "dev_thermo",
				Name:               "Ecobee Thermostat",
				Manufacturer:       "Ecobee",
				Model:              "SmartThermostat",
				SWVersion:          "4.7",
				AreaID:             "office",
				PrimaryConfigEntry: "cfg_ecobee",
			},
		},
		configs: []homeassistant.ConfigEntry{
			{EntryID: "cfg_ecobee", Domain: "ecobee", Title: "Ecobee"},
		},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "climate.thermostat", DeviceID: "dev_thermo"},
			{EntityID: "sensor.thermostat_temp", DeviceID: "dev_thermo", DeviceClass: "temperature"},
			{EntityID: "sensor.thermostat_humidity", DeviceID: "dev_thermo", DeviceClass: "humidity"},
			{EntityID: "binary_sensor.thermostat_motion", DeviceID: "dev_thermo", DeviceClass: "motion"},
			{EntityID: "sensor.thermostat_battery", DeviceID: "dev_thermo", DeviceClass: "battery"},
			{EntityID: "sensor.thermostat_signal", DeviceID: "dev_thermo", DeviceClass: "signal_strength"},
			// Belongs to a different device — must not appear.
			{EntityID: "light.hall", DeviceID: "dev_other"},
		},
		states: map[string]*homeassistant.State{
			"climate.thermostat":              mkState("climate.thermostat", "heat", map[string]any{"hvac_action": "heating"}),
			"sensor.thermostat_temp":          mkState("sensor.thermostat_temp", "72", map[string]any{"device_class": "temperature"}),
			"sensor.thermostat_humidity":      mkState("sensor.thermostat_humidity", "40", map[string]any{"device_class": "humidity"}),
			"binary_sensor.thermostat_motion": mkState("binary_sensor.thermostat_motion", "off", map[string]any{"device_class": "motion"}),
			"sensor.thermostat_battery":       mkState("sensor.thermostat_battery", "unavailable", map[string]any{"device_class": "battery"}),
			// sensor.thermostat_signal intentionally absent → no_state.
			"light.hall": mkState("light.hall", "on", nil),
		},
	}
}

func decodeDevicePayload(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	return payload
}

// bucketEntities returns the entity_ids present in a named bucket.
func bucketEntities(payload map[string]any, bucket string) []string {
	list, _ := payload[bucket].([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if obj, ok := item.(map[string]any); ok {
			if id, ok := obj["entity"].(string); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestComputeDeviceSnapshot_ByID(t *testing.T) {
	out, err := ComputeDeviceSnapshot(context.Background(), thermostatClient(), DeviceSnapshotRequest{Device: "dev_thermo"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)
	if payload["device"] != "Ecobee Thermostat" {
		t.Errorf("device = %#v, want Ecobee Thermostat", payload["device"])
	}
	if payload["device_id"] != "dev_thermo" {
		t.Errorf("device_id = %#v, want dev_thermo", payload["device_id"])
	}
	if payload["integration"] != "ecobee" {
		t.Errorf("integration = %#v, want ecobee", payload["integration"])
	}
	// The cross-device entity must never leak into the snapshot.
	for _, bucket := range []string{"anomalies", "active", "ambient", "other"} {
		if contains(bucketEntities(payload, bucket), "light.hall") {
			t.Errorf("light.hall (other device) leaked into %s", bucket)
		}
	}
}

func TestComputeDeviceSnapshot_ByName(t *testing.T) {
	cases := []string{"Ecobee Thermostat", "ecobee thermostat", "thermostat"}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			out, err := ComputeDeviceSnapshot(context.Background(), thermostatClient(), DeviceSnapshotRequest{Device: q}, testNow)
			if err != nil {
				t.Fatalf("ComputeDeviceSnapshot(%q): %v", q, err)
			}
			payload := decodeDevicePayload(t, out)
			if payload["device_id"] != "dev_thermo" {
				t.Errorf("device_id = %#v, want dev_thermo (query %q)", payload["device_id"], q)
			}
		})
	}
}

func TestComputeDeviceSnapshot_MultiEntityAndAvailability(t *testing.T) {
	out, err := ComputeDeviceSnapshot(context.Background(), thermostatClient(), DeviceSnapshotRequest{Device: "dev_thermo"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)

	if got := payload["entity_count"]; got != float64(6) {
		t.Errorf("entity_count = %#v, want 6", got)
	}

	avail, ok := payload["availability"].(map[string]any)
	if !ok {
		t.Fatalf("availability missing or wrong type: %#v", payload["availability"])
	}
	if avail["reporting"] != float64(4) || avail["total"] != float64(6) {
		t.Errorf("availability = %#v, want reporting 4 / total 6", avail)
	}

	active := bucketEntities(payload, "active")
	if !contains(active, "climate.thermostat") {
		t.Errorf("active = %v, want climate.thermostat", active)
	}
	ambient := bucketEntities(payload, "ambient")
	if !contains(ambient, "sensor.thermostat_temp") || !contains(ambient, "sensor.thermostat_humidity") {
		t.Errorf("ambient = %v, want temp + humidity", ambient)
	}
	other := bucketEntities(payload, "other")
	if !contains(other, "binary_sensor.thermostat_motion") {
		t.Errorf("other = %v, want binary_sensor.thermostat_motion", other)
	}
	anomalies := bucketEntities(payload, "anomalies")
	if !contains(anomalies, "sensor.thermostat_battery") || !contains(anomalies, "sensor.thermostat_signal") {
		t.Errorf("anomalies = %v, want battery (sentinel) + signal (no_state)", anomalies)
	}

	unavailable, _ := payload["unavailable"].([]any)
	if len(unavailable) != 2 {
		t.Errorf("unavailable = %#v, want 2 entries", payload["unavailable"])
	}
}

func TestComputeDeviceSnapshot_AmbiguousName(t *testing.T) {
	client := &fakeDeviceClient{
		devices: []homeassistant.DeviceRegistryEntry{
			{ID: "dev_a", Name: "Front Door Sensor"},
			{ID: "dev_b", Name: "Back Door Sensor"},
		},
	}
	out, err := ComputeDeviceSnapshot(context.Background(), client, DeviceSnapshotRequest{Device: "sensor"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)
	if payload["found"] != false {
		t.Errorf("found = %#v, want false", payload["found"])
	}
	if payload["reason"] != "ambiguous" {
		t.Errorf("reason = %#v, want ambiguous", payload["reason"])
	}
	cands, _ := payload["candidates"].([]any)
	if len(cands) != 2 {
		t.Errorf("candidates = %#v, want 2", payload["candidates"])
	}
}

func TestComputeDeviceSnapshot_NotFound(t *testing.T) {
	client := &fakeDeviceClient{
		devices: []homeassistant.DeviceRegistryEntry{{ID: "dev_a", Name: "Thermostat"}},
	}
	out, err := ComputeDeviceSnapshot(context.Background(), client, DeviceSnapshotRequest{Device: "garage opener"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)
	if payload["found"] != false || payload["reason"] != "no_match" {
		t.Errorf("expected found=false reason=no_match, got %#v", payload)
	}
}

func TestComputeDeviceSnapshot_IncludeMetadata(t *testing.T) {
	client := thermostatClient()
	// Give one child a description to surface through the projection.
	for i := range client.entities {
		if client.entities[i].EntityID == "climate.thermostat" {
			client.entities[i].Description = "Main floor thermostat"
		}
	}

	req := DeviceSnapshotRequest{
		Device:  "dev_thermo",
		Include: homeassistant.EntityMetadataIncludes{Description: true},
	}
	out, err := ComputeDeviceSnapshot(context.Background(), client, req, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)
	active, _ := payload["active"].([]any)
	if len(active) == 0 {
		t.Fatalf("expected an active entity to enrich:\n%s", out)
	}
	obj, _ := active[0].(map[string]any)
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata on active entity, got %#v", obj["metadata"])
	}
	if meta["description"] != "Main floor thermostat" {
		t.Errorf("metadata.description = %#v, want Main floor thermostat", meta["description"])
	}
}

func TestComputeDeviceSnapshot_NoIncludeNoMetadata(t *testing.T) {
	out, err := ComputeDeviceSnapshot(context.Background(), thermostatClient(), DeviceSnapshotRequest{Device: "dev_thermo"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)
	for _, bucket := range []string{"anomalies", "active", "ambient", "other"} {
		list, _ := payload[bucket].([]any)
		for _, item := range list {
			if obj, ok := item.(map[string]any); ok {
				if _, has := obj["metadata"]; has {
					t.Errorf("entity in %s carries metadata without include:\n%s", bucket, out)
				}
			}
		}
	}
}

func TestComputeDeviceSnapshot_DisabledChildExcluded(t *testing.T) {
	client := &fakeDeviceClient{
		areas:   []homeassistant.Area{{AreaID: "office", Name: "Office"}},
		devices: []homeassistant.DeviceRegistryEntry{{ID: "dev_x", Name: "Widget", AreaID: "office"}},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "switch.widget", DeviceID: "dev_x"},
			{EntityID: "sensor.widget_legacy", DeviceID: "dev_x", DisabledBy: "integration"},
		},
		states: map[string]*homeassistant.State{
			"switch.widget": mkState("switch.widget", "on", nil),
		},
	}
	out, err := ComputeDeviceSnapshot(context.Background(), client, DeviceSnapshotRequest{Device: "dev_x"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)
	if payload["entity_count"] != float64(1) {
		t.Errorf("entity_count = %#v, want 1 (disabled excluded)", payload["entity_count"])
	}
	if payload["disabled_count"] != float64(1) {
		t.Errorf("disabled_count = %#v, want 1", payload["disabled_count"])
	}
	for _, bucket := range []string{"anomalies", "active", "ambient", "other"} {
		if contains(bucketEntities(payload, bucket), "sensor.widget_legacy") {
			t.Errorf("disabled entity rendered in %s", bucket)
		}
	}
}

func TestComputeDeviceSnapshot_RequiresDevice(t *testing.T) {
	if _, err := ComputeDeviceSnapshot(context.Background(), thermostatClient(), DeviceSnapshotRequest{}, testNow); err == nil {
		t.Error("expected error when device is empty")
	}
}

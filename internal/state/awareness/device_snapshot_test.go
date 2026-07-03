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

// thermostatClient builds a device whose children populate all four of
// HA's device-page groups — a control (climate), read-only sensors, a
// config entity (aux-heat switch, entity_category=config), and two
// diagnostics (battery + signal, entity_category=diagnostic) — including
// one sentinel (unavailable) and one with no state at all, for the
// grouping / availability tests.
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
			// entity_category=config → a switch that would otherwise be a
			// control lands in Configuration, proving category wins over domain.
			{EntityID: "switch.thermostat_aux_heat", DeviceID: "dev_thermo", EntityCategory: "config"},
			{EntityID: "sensor.thermostat_battery", DeviceID: "dev_thermo", DeviceClass: "battery", EntityCategory: "diagnostic"},
			{EntityID: "sensor.thermostat_signal", DeviceID: "dev_thermo", DeviceClass: "signal_strength", EntityCategory: "diagnostic"},
			// Belongs to a different device — must not appear.
			{EntityID: "light.hall", DeviceID: "dev_other"},
		},
		states: map[string]*homeassistant.State{
			"climate.thermostat":              mkState("climate.thermostat", "heat", map[string]any{"hvac_action": "heating"}),
			"sensor.thermostat_temp":          mkState("sensor.thermostat_temp", "72", map[string]any{"device_class": "temperature"}),
			"sensor.thermostat_humidity":      mkState("sensor.thermostat_humidity", "40", map[string]any{"device_class": "humidity"}),
			"binary_sensor.thermostat_motion": mkState("binary_sensor.thermostat_motion", "off", map[string]any{"device_class": "motion"}),
			"switch.thermostat_aux_heat":      mkState("switch.thermostat_aux_heat", "off", nil),
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

// deviceGroups are HA's four device-page groups, in output order.
var deviceGroups = []string{"controls", "sensors", "configuration", "diagnostic"}

// bucketEntities returns the entity_ids present in a named group.
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
	for _, bucket := range deviceGroups {
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

func TestComputeDeviceSnapshot_GroupsAndAvailability(t *testing.T) {
	out, err := ComputeDeviceSnapshot(context.Background(), thermostatClient(), DeviceSnapshotRequest{Device: "dev_thermo"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)

	if got := payload["entity_count"]; got != float64(7) {
		t.Errorf("entity_count = %#v, want 7", got)
	}

	avail, ok := payload["availability"].(map[string]any)
	if !ok {
		t.Fatalf("availability missing or wrong type: %#v", payload["availability"])
	}
	// 5 reporting (climate, temp, humidity, motion, aux_heat); battery is
	// a sentinel and signal has no state, so neither counts as reporting.
	if avail["reporting"] != float64(5) || avail["total"] != float64(7) {
		t.Errorf("availability = %#v, want reporting 5 / total 7", avail)
	}

	// Controls: the actionable primary.
	controls := bucketEntities(payload, "controls")
	if !contains(controls, "climate.thermostat") {
		t.Errorf("controls = %v, want climate.thermostat", controls)
	}
	// Sensors: read-only primaries (temperature, humidity, motion).
	sensors := bucketEntities(payload, "sensors")
	if !contains(sensors, "sensor.thermostat_temp") ||
		!contains(sensors, "sensor.thermostat_humidity") ||
		!contains(sensors, "binary_sensor.thermostat_motion") {
		t.Errorf("sensors = %v, want temp + humidity + motion", sensors)
	}
	// Configuration: entity_category=config wins over the switch domain.
	config := bucketEntities(payload, "configuration")
	if !contains(config, "switch.thermostat_aux_heat") {
		t.Errorf("configuration = %v, want switch.thermostat_aux_heat", config)
	}
	if contains(controls, "switch.thermostat_aux_heat") {
		t.Errorf("config switch leaked into controls: %v", controls)
	}
	// Diagnostic: battery (sentinel) + signal (no_state) still group here.
	diagnostic := bucketEntities(payload, "diagnostic")
	if !contains(diagnostic, "sensor.thermostat_battery") || !contains(diagnostic, "sensor.thermostat_signal") {
		t.Errorf("diagnostic = %v, want battery + signal", diagnostic)
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
	controls, _ := payload["controls"].([]any)
	if len(controls) == 0 {
		t.Fatalf("expected a control entity to enrich:\n%s", out)
	}
	obj, _ := controls[0].(map[string]any)
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata on control entity, got %#v", obj["metadata"])
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
	for _, bucket := range deviceGroups {
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
	for _, bucket := range deviceGroups {
		if contains(bucketEntities(payload, bucket), "sensor.widget_legacy") {
			t.Errorf("disabled entity rendered in %s", bucket)
		}
	}
}

// findEntity returns the rendered object for an entity_id across every
// device group, or nil if it isn't present.
func findEntity(payload map[string]any, entityID string) map[string]any {
	for _, group := range deviceGroups {
		list, _ := payload[group].([]any)
		for _, item := range list {
			obj, ok := item.(map[string]any)
			if ok && obj["entity"] == entityID {
				return obj
			}
		}
	}
	return nil
}

// A device view is explicit inspection: unlike the enumeration tools it
// shows hidden entities, marked, so the model sees the whole instrument
// panel the way HA's device page does.
func TestComputeDeviceSnapshot_ShowsHiddenEntitiesMarked(t *testing.T) {
	client := &fakeDeviceClient{
		areas:   []homeassistant.Area{{AreaID: "office", Name: "Office"}},
		devices: []homeassistant.DeviceRegistryEntry{{ID: "dev_dimmer", Name: "Office Overhead Lights", AreaID: "office"}},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "light.office_overhead", DeviceID: "dev_dimmer"},
			// Operator-hidden config knob — off HA's generated dashboards
			// but present on the device page.
			{EntityID: "number.office_overhead_ramp_rate", DeviceID: "dev_dimmer", EntityCategory: "config", HiddenBy: "user"},
		},
		states: map[string]*homeassistant.State{
			"light.office_overhead":            mkState("light.office_overhead", "on", nil),
			"number.office_overhead_ramp_rate": mkState("number.office_overhead_ramp_rate", "3", nil),
		},
	}
	out, err := ComputeDeviceSnapshot(context.Background(), client, DeviceSnapshotRequest{Device: "dev_dimmer"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)

	hidden := findEntity(payload, "number.office_overhead_ramp_rate")
	if hidden == nil {
		t.Fatalf("hidden entity missing from device view:\n%s", out)
	}
	if hidden["hidden"] != true {
		t.Errorf("hidden entity not marked hidden: %#v", hidden)
	}
	visible := findEntity(payload, "light.office_overhead")
	if visible == nil {
		t.Fatalf("visible entity missing:\n%s", out)
	}
	if _, marked := visible["hidden"]; marked {
		t.Errorf("visible entity wrongly marked hidden: %#v", visible)
	}
}

// The noisy groups (Configuration, Diagnostic) are capped with an honest
// overflow count so a device with dozens of tuning knobs stays legible.
func TestComputeDeviceSnapshot_CapsConfigurationGroup(t *testing.T) {
	client := &fakeDeviceClient{
		areas:   []homeassistant.Area{{AreaID: "office", Name: "Office"}},
		devices: []homeassistant.DeviceRegistryEntry{{ID: "dev_zwave", Name: "Z-Wave Switch", AreaID: "office"}},
		states:  map[string]*homeassistant.State{},
	}
	const configCount = maxDeviceOtherEntities + 4
	for i := 0; i < configCount; i++ {
		id := fmt.Sprintf("number.zwave_param_%02d", i)
		client.entities = append(client.entities, homeassistant.EntityRegistryEntry{
			EntityID: id, DeviceID: "dev_zwave", EntityCategory: "config",
		})
		client.states[id] = mkState(id, fmt.Sprintf("%d", i), nil)
	}

	out, err := ComputeDeviceSnapshot(context.Background(), client, DeviceSnapshotRequest{Device: "dev_zwave"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)

	config, _ := payload["configuration"].([]any)
	if len(config) != maxDeviceOtherEntities {
		t.Errorf("configuration length = %d, want cap %d", len(config), maxDeviceOtherEntities)
	}
	if payload["configuration_truncated_count"] != float64(configCount-maxDeviceOtherEntities) {
		t.Errorf("configuration_truncated_count = %#v, want %d", payload["configuration_truncated_count"], configCount-maxDeviceOtherEntities)
	}
}

// The device-info card — area and the device's own labels — rides on the
// identity block by default (no include projection needed), because a
// device view is exactly where that context belongs.
func TestComputeDeviceSnapshot_IdentityCarriesLabelsAndArea(t *testing.T) {
	client := &fakeDeviceClient{
		areas: []homeassistant.Area{{AreaID: "office", Name: "Office"}},
		labels: []homeassistant.LabelRegistryEntry{
			{LabelID: "label_hvac", Name: "HVAC", Color: "blue"},
		},
		devices: []homeassistant.DeviceRegistryEntry{{
			ID: "dev_thermo", Name: "Ecobee Thermostat", AreaID: "office",
			Labels: []string{"label_hvac"},
		}},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "climate.thermostat", DeviceID: "dev_thermo"},
		},
		states: map[string]*homeassistant.State{
			"climate.thermostat": mkState("climate.thermostat", "heat", nil),
		},
	}
	out, err := ComputeDeviceSnapshot(context.Background(), client, DeviceSnapshotRequest{Device: "dev_thermo"}, testNow)
	if err != nil {
		t.Fatalf("ComputeDeviceSnapshot: %v", err)
	}
	payload := decodeDevicePayload(t, out)
	identity, ok := payload["identity"].(map[string]any)
	if !ok {
		t.Fatalf("identity missing or wrong type: %#v", payload["identity"])
	}
	if identity["area_name"] != "Office" {
		t.Errorf("identity.area_name = %#v, want Office (no include needed)", identity["area_name"])
	}
	labels, ok := identity["labels"].([]any)
	if !ok || len(labels) != 1 {
		t.Fatalf("identity.labels = %#v, want 1 label by default", identity["labels"])
	}
	label0, _ := labels[0].(map[string]any)
	if label0["name"] != "HVAC" {
		t.Errorf("identity.labels[0].name = %#v, want HVAC", label0["name"])
	}
}

func TestComputeDeviceSnapshot_RequiresDevice(t *testing.T) {
	if _, err := ComputeDeviceSnapshot(context.Background(), thermostatClient(), DeviceSnapshotRequest{}, testNow); err == nil {
		t.Error("expected error when device is empty")
	}
}

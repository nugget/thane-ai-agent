package awareness

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// houseClient builds a representative whole-home fixture spanning every
// curated section plus entities that must be skipped (a plain light, a
// presence sensor going unavailable).
func houseClient() *fakeAreaClient {
	st := func(id, state string, attrs map[string]any) homeassistant.State {
		return homeassistant.State{EntityID: id, State: state, Attributes: attrs}
	}
	return &fakeAreaClient{
		areas: []homeassistant.Area{{AreaID: "living_room", Name: "Living Room"}},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "person.alice"},
			{EntityID: "person.bob"},
			{EntityID: "binary_sensor.front_door", DeviceClass: "door"},
			{EntityID: "binary_sensor.kitchen_window", DeviceClass: "window"},
			{EntityID: "binary_sensor.hall_motion", DeviceClass: "motion"}, // not an opening
			{EntityID: "binary_sensor.basement_smoke", DeviceClass: "smoke"},
			{EntityID: "lock.back_door"},
			{EntityID: "lock.front_door"},
			{EntityID: "cover.garage", DeviceClass: "garage"},
			{EntityID: "alarm_control_panel.home"},
			{EntityID: "climate.living_room", AreaID: "living_room"},
			{EntityID: "sensor.house_power", DeviceClass: "power"},
			{EntityID: "light.office"}, // must be curated out
		},
		states: []homeassistant.State{
			st("person.alice", "home", nil),
			st("person.bob", "not_home", nil),
			st("binary_sensor.front_door", "on", map[string]any{"device_class": "door"}),        // open
			st("binary_sensor.kitchen_window", "off", map[string]any{"device_class": "window"}), // closed → skip
			st("binary_sensor.hall_motion", "on", map[string]any{"device_class": "motion"}),     // not an opening → skip
			st("binary_sensor.basement_smoke", "on", map[string]any{"device_class": "smoke"}),   // alarm
			st("lock.back_door", "unlocked", nil),                                               // security
			st("lock.front_door", "unavailable", nil),                                           // anomaly
			st("cover.garage", "open", map[string]any{"device_class": "garage"}),                // security
			st("alarm_control_panel.home", "armed_away", nil),                                   // security
			st("climate.living_room", "heat", map[string]any{"current_temperature": 71, "temperature": 72}),
			st("sensor.house_power", "350", map[string]any{"device_class": "power", "unit_of_measurement": "W"}),
			st("light.office", "on", nil), // curated out
		},
	}
}

func decodeHome(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	return payload
}

// sectionIDs returns the entity_ids in a named home-snapshot section.
func sectionIDs(payload map[string]any, section string) []string {
	list, _ := payload[section].([]any)
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

func TestComputeHomeSnapshot_SectionAssembly(t *testing.T) {
	out, err := ComputeHomeSnapshot(context.Background(), houseClient(), HomeSnapshotRequest{IncludeEnergy: true}, testNow)
	if err != nil {
		t.Fatalf("ComputeHomeSnapshot: %v", err)
	}
	p := decodeHome(t, out)

	anomalies := sectionIDs(p, "anomalies")
	if !contains(anomalies, "binary_sensor.basement_smoke") || !contains(anomalies, "lock.front_door") {
		t.Errorf("anomalies = %v, want smoke (alarm) + front_door lock (unavailable)", anomalies)
	}

	security := sectionIDs(p, "security")
	for _, want := range []string{"binary_sensor.front_door", "lock.back_door", "cover.garage", "alarm_control_panel.home"} {
		if !contains(security, want) {
			t.Errorf("security = %v, want %s", security, want)
		}
	}
	// A closed window and a motion sensor are not security/openings items.
	if contains(security, "binary_sensor.kitchen_window") || contains(security, "binary_sensor.hall_motion") {
		t.Errorf("security wrongly included a closed/non-opening sensor: %v", security)
	}

	presence := sectionIDs(p, "presence")
	if !contains(presence, "person.alice") || !contains(presence, "person.bob") {
		t.Errorf("presence = %v, want alice + bob", presence)
	}

	if !contains(sectionIDs(p, "climate"), "climate.living_room") {
		t.Errorf("climate = %v, want living_room", sectionIDs(p, "climate"))
	}
	if !contains(sectionIDs(p, "energy"), "sensor.house_power") {
		t.Errorf("energy = %v, want house_power", sectionIDs(p, "energy"))
	}

	// Curation: a plain light belongs to no section.
	for _, section := range []string{"anomalies", "security", "presence", "climate", "energy"} {
		if contains(sectionIDs(p, section), "light.office") {
			t.Errorf("light.office leaked into %s — home snapshot must be curated", section)
		}
	}

	summary, _ := p["summary"].(map[string]any)
	if summary["home"] != float64(1) || summary["away"] != float64(1) {
		t.Errorf("summary home/away = %#v, want 1/1", summary)
	}
	if summary["anomalies"] != float64(2) {
		t.Errorf("summary anomalies = %#v, want 2", summary["anomalies"])
	}
	if _, quiet := p["status"]; quiet {
		t.Errorf("status should not be quiet when anomalies/security present:\n%s", out)
	}
}

// TestComputeHomeSnapshot_NoIncludeSkipsDeviceRegistry is the #1019
// regression: the default glanceable path must not pull the device
// registry (or the area/floor/label registries behind the resolver),
// since classification needs only live state + the entity registry.
func TestComputeHomeSnapshot_NoIncludeSkipsDeviceRegistry(t *testing.T) {
	client := houseClient()
	if _, err := ComputeHomeSnapshot(context.Background(), client, HomeSnapshotRequest{}, testNow); err != nil {
		t.Fatalf("ComputeHomeSnapshot: %v", err)
	}
	if client.deviceCalls != 0 {
		t.Errorf("GetDeviceRegistry called %d times on the no-include path, want 0", client.deviceCalls)
	}
	if client.floorCalls != 0 {
		t.Errorf("GetFloorRegistry called %d times on the no-include path, want 0", client.floorCalls)
	}
}

// TestComputeHomeSnapshot_IncludeFetchesDeviceRegistry confirms the
// device registry IS pulled when a per-entity include projection is
// requested.
func TestComputeHomeSnapshot_IncludeFetchesDeviceRegistry(t *testing.T) {
	client := houseClient()
	req := HomeSnapshotRequest{Include: homeassistant.EntityMetadataIncludes{Device: true}}
	if _, err := ComputeHomeSnapshot(context.Background(), client, req, testNow); err != nil {
		t.Fatalf("ComputeHomeSnapshot: %v", err)
	}
	if client.deviceCalls == 0 {
		t.Error("expected GetDeviceRegistry to be called when include.device is set")
	}
}

func TestComputeHomeSnapshot_EnergyGated(t *testing.T) {
	out, err := ComputeHomeSnapshot(context.Background(), houseClient(), HomeSnapshotRequest{}, testNow)
	if err != nil {
		t.Fatalf("ComputeHomeSnapshot: %v", err)
	}
	p := decodeHome(t, out)
	if _, ok := p["energy"]; ok {
		t.Errorf("energy section present without include_energy:\n%s", out)
	}
}

func TestComputeHomeSnapshot_AllQuiet(t *testing.T) {
	st := func(id, state string, attrs map[string]any) homeassistant.State {
		return homeassistant.State{EntityID: id, State: state, Attributes: attrs}
	}
	client := &fakeAreaClient{
		areas: []homeassistant.Area{{AreaID: "living_room", Name: "Living Room"}},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "person.alice"},
			{EntityID: "lock.front_door"},
			{EntityID: "binary_sensor.front_door", DeviceClass: "door"},
			{EntityID: "alarm_control_panel.home"},
			{EntityID: "climate.living_room", AreaID: "living_room"},
		},
		states: []homeassistant.State{
			st("person.alice", "home", nil),
			st("lock.front_door", "locked", nil),
			st("binary_sensor.front_door", "off", map[string]any{"device_class": "door"}),
			st("alarm_control_panel.home", "disarmed", nil),
			st("climate.living_room", "heat", map[string]any{"current_temperature": 71}),
		},
	}
	out, err := ComputeHomeSnapshot(context.Background(), client, HomeSnapshotRequest{}, testNow)
	if err != nil {
		t.Fatalf("ComputeHomeSnapshot: %v", err)
	}
	p := decodeHome(t, out)
	if p["status"] != "quiet" {
		t.Errorf("status = %#v, want quiet (nothing offline/open/unlocked/armed)\n%s", p["status"], out)
	}
	if _, ok := p["anomalies"]; ok {
		t.Errorf("expected no anomalies section when quiet")
	}
	if _, ok := p["security"]; ok {
		t.Errorf("expected no security section when quiet")
	}
	// Presence and climate are ambient context and still render.
	if !contains(sectionIDs(p, "presence"), "person.alice") {
		t.Errorf("presence should still render when quiet:\n%s", out)
	}
	if !contains(sectionIDs(p, "climate"), "climate.living_room") {
		t.Errorf("climate should still render when quiet:\n%s", out)
	}
}

func TestComputeHomeSnapshot_FiltersDiagnosticAndDisabled(t *testing.T) {
	st := func(id, state string, attrs map[string]any) homeassistant.State {
		return homeassistant.State{EntityID: id, State: state, Attributes: attrs}
	}
	client := &fakeAreaClient{
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "lock.front_door"},
			{EntityID: "lock.shed", DisabledBy: "user"},                                              // filtered
			{EntityID: "binary_sensor.diag_door", DeviceClass: "door", EntityCategory: "diagnostic"}, // filtered
		},
		states: []homeassistant.State{
			st("lock.front_door", "unlocked", nil),
			st("lock.shed", "unlocked", nil),
			st("binary_sensor.diag_door", "on", map[string]any{"device_class": "door"}),
		},
	}
	out, err := ComputeHomeSnapshot(context.Background(), client, HomeSnapshotRequest{}, testNow)
	if err != nil {
		t.Fatalf("ComputeHomeSnapshot: %v", err)
	}
	p := decodeHome(t, out)
	security := sectionIDs(p, "security")
	if !contains(security, "lock.front_door") {
		t.Errorf("security = %v, want lock.front_door", security)
	}
	if contains(security, "lock.shed") {
		t.Errorf("disabled lock.shed must be filtered out: %v", security)
	}
	if contains(security, "binary_sensor.diag_door") {
		t.Errorf("diagnostic door must be filtered out by default: %v", security)
	}
	if p["disabled_count"] != float64(1) {
		t.Errorf("disabled_count = %#v, want 1", p["disabled_count"])
	}
	if p["diagnostic_count"] != float64(1) {
		t.Errorf("diagnostic_count = %#v, want 1", p["diagnostic_count"])
	}
}

func TestComputeHomeSnapshot_SectionTruncation(t *testing.T) {
	st := func(id, state string, attrs map[string]any) homeassistant.State {
		return homeassistant.State{EntityID: id, State: state, Attributes: attrs}
	}
	var entities []homeassistant.EntityRegistryEntry
	var states []homeassistant.State
	for _, id := range []string{
		"binary_sensor.door_a", "binary_sensor.door_b", "binary_sensor.door_c",
	} {
		entities = append(entities, homeassistant.EntityRegistryEntry{EntityID: id, DeviceClass: "door"})
		states = append(states, st(id, "on", map[string]any{"device_class": "door"}))
	}
	client := &fakeAreaClient{entities: entities, states: states}

	out, err := ComputeHomeSnapshot(context.Background(), client, HomeSnapshotRequest{MaxPerSection: 2}, testNow)
	if err != nil {
		t.Fatalf("ComputeHomeSnapshot: %v", err)
	}
	p := decodeHome(t, out)
	if got := len(sectionIDs(p, "security")); got != 2 {
		t.Errorf("security rendered %d, want 2 (capped)", got)
	}
	if p["security_truncated_count"] != float64(1) {
		t.Errorf("security_truncated_count = %#v, want 1", p["security_truncated_count"])
	}
}

func TestComputeHomeSnapshot_IncludeMetadata(t *testing.T) {
	client := &fakeAreaClient{
		areas: []homeassistant.Area{{AreaID: "entry", Name: "Entryway"}},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "lock.front_door", AreaID: "entry", Description: "Front deadbolt"},
		},
		states: []homeassistant.State{
			{EntityID: "lock.front_door", State: "unlocked"},
		},
	}
	req := HomeSnapshotRequest{Include: homeassistant.EntityMetadataIncludes{Description: true}}
	out, err := ComputeHomeSnapshot(context.Background(), client, req, testNow)
	if err != nil {
		t.Fatalf("ComputeHomeSnapshot: %v", err)
	}
	p := decodeHome(t, out)
	sec, _ := p["security"].([]any)
	if len(sec) == 0 {
		t.Fatalf("expected a security item to enrich:\n%s", out)
	}
	obj, _ := sec[0].(map[string]any)
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata on security item, got %#v", obj["metadata"])
	}
	if meta["description"] != "Front deadbolt" {
		t.Errorf("metadata.description = %#v, want Front deadbolt", meta["description"])
	}
}

// HA 2026.7 presence semantics: in_zones is authoritative for the
// home/away rollup. State reports only the *smallest* zone a person is
// in, so someone in a nested zone inside the home would miscount as
// away on a bare state comparison.
func TestComputeHomeSnapshot_InZonesPresence(t *testing.T) {
	st := func(id, state string, attrs map[string]any) homeassistant.State {
		return homeassistant.State{EntityID: id, State: state, Attributes: attrs}
	}
	client := &fakeAreaClient{
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "person.alice"},
			{EntityID: "person.bob"},
			{EntityID: "person.carol"},
			{EntityID: "person.dave"},
		},
		states: []homeassistant.State{
			// Alice is in a nested zone inside home: state = smallest zone
			// name, but zone.home is in in_zones → home.
			st("person.alice", "Pool House", map[string]any{"in_zones": []any{"zone.pool_house", "zone.home"}}),
			// Bob is away with the attribute present-and-empty → away.
			st("person.bob", "not_home", map[string]any{"in_zones": []any{}}),
			// Carol is in an unrelated zone → away (zone.home absent).
			st("person.carol", "Work", map[string]any{"in_zones": []any{"zone.work"}}),
			// Dave has no in_zones (pre-2026.7 / scanner-derived person):
			// state fallback keeps him home.
			st("person.dave", "home", nil),
		},
	}

	out, err := ComputeHomeSnapshot(context.Background(), client, HomeSnapshotRequest{}, testNow)
	if err != nil {
		t.Fatalf("ComputeHomeSnapshot: %v", err)
	}
	payload := decodeHome(t, out)
	summary, _ := payload["summary"].(map[string]any)
	if summary["home"] != float64(2) || summary["away"] != float64(2) {
		t.Errorf("summary home/away = %v/%v, want 2/2 (nested-zone alice + fallback dave home)",
			summary["home"], summary["away"])
	}
}

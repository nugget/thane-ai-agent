package awareness

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// fakeAreaClient supplies the four registry calls plus the area
// listing and logbook for area_activity tests.
type fakeAreaClient struct {
	areas    []homeassistant.Area
	entities []homeassistant.EntityRegistryEntry
	devices  []homeassistant.DeviceRegistryEntry
	states   []homeassistant.State
	configs  []homeassistant.ConfigEntry
	logbook  []homeassistant.LogbookEntry

	areasErr   error
	logbookErr error
}

func (f *fakeAreaClient) GetAreas(_ context.Context) ([]homeassistant.Area, error) {
	return f.areas, f.areasErr
}
func (f *fakeAreaClient) GetEntityRegistry(_ context.Context) ([]homeassistant.EntityRegistryEntry, error) {
	return f.entities, nil
}
func (f *fakeAreaClient) GetDeviceRegistry(_ context.Context) ([]homeassistant.DeviceRegistryEntry, error) {
	return f.devices, nil
}
func (f *fakeAreaClient) GetStates(_ context.Context) ([]homeassistant.State, error) {
	return f.states, nil
}
func (f *fakeAreaClient) GetConfigEntries(_ context.Context) ([]homeassistant.ConfigEntry, error) {
	return f.configs, nil
}
func (f *fakeAreaClient) GetLogbookEvents(_ context.Context, _, _ time.Time, _ []string) ([]homeassistant.LogbookEntry, error) {
	return f.logbook, f.logbookErr
}

func TestComputeAreaActivity_BucketsByRelevance(t *testing.T) {
	now := testNow

	areas := []homeassistant.Area{{AreaID: "kitchen", Name: "Kitchen"}}
	entities := []homeassistant.EntityRegistryEntry{
		{EntityID: "binary_sensor.kitchen_smoke", AreaID: "kitchen", DeviceClass: "smoke"},
		{EntityID: "light.kitchen_main", AreaID: "kitchen"},
		{EntityID: "binary_sensor.kitchen_motion", AreaID: "kitchen", DeviceClass: "motion"},
		{EntityID: "sensor.kitchen_temp", AreaID: "kitchen", DeviceClass: "temperature"},
		{EntityID: "switch.kitchen_outlet", AreaID: "kitchen"},
		// Diagnostic entity should be filtered out by default.
		{EntityID: "sensor.kitchen_battery", AreaID: "kitchen", EntityCategory: "diagnostic"},
		// Disabled entity should be filtered.
		{EntityID: "sensor.kitchen_disabled", AreaID: "kitchen", DisabledBy: "user"},
		// Entity in another area should not appear.
		{EntityID: "light.living_room", AreaID: "living_room"},
	}
	states := []homeassistant.State{
		{EntityID: "binary_sensor.kitchen_smoke", State: "on", LastChanged: now.Add(-3 * time.Minute), Attributes: map[string]any{"device_class": "smoke"}},
		{EntityID: "light.kitchen_main", State: "on", LastChanged: now.Add(-10 * time.Minute)},
		{EntityID: "binary_sensor.kitchen_motion", State: "clear", LastChanged: now.Add(-45 * time.Second), Attributes: map[string]any{"device_class": "motion"}},
		{EntityID: "sensor.kitchen_temp", State: "72.4", LastChanged: now.Add(-15 * time.Hour), Attributes: map[string]any{"device_class": "temperature", "state_class": "measurement"}},
		{EntityID: "switch.kitchen_outlet", State: "off", LastChanged: now.Add(-12 * time.Hour)},
	}

	client := &fakeAreaClient{areas: areas, entities: entities, states: states}
	got, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "Kitchen"}, now)
	if err != nil {
		t.Fatalf("ComputeAreaActivity: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, got)
	}

	if parsed["area"] != "Kitchen" {
		t.Errorf("area = %v, want Kitchen", parsed["area"])
	}
	if parsed["area_id"] != "kitchen" {
		t.Errorf("area_id = %v, want kitchen", parsed["area_id"])
	}

	bucketEntities := func(key string) []string {
		raw, _ := parsed[key].([]any)
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			obj, _ := item.(map[string]any)
			if entity, ok := obj["entity"].(string); ok {
				out = append(out, entity)
			}
		}
		return out
	}

	anomalies := bucketEntities("anomalies")
	if len(anomalies) != 1 || anomalies[0] != "binary_sensor.kitchen_smoke" {
		t.Errorf("anomalies = %v, want [binary_sensor.kitchen_smoke]", anomalies)
	}
	active := bucketEntities("active")
	if len(active) != 1 || active[0] != "light.kitchen_main" {
		t.Errorf("active = %v, want [light.kitchen_main]", active)
	}
	recent := bucketEntities("recent_changes")
	if len(recent) != 1 || recent[0] != "binary_sensor.kitchen_motion" {
		t.Errorf("recent_changes = %v, want [binary_sensor.kitchen_motion]", recent)
	}
	ambient := bucketEntities("ambient")
	if len(ambient) != 1 || ambient[0] != "sensor.kitchen_temp" {
		t.Errorf("ambient = %v, want [sensor.kitchen_temp]", ambient)
	}
	stable := bucketEntities("stable")
	if len(stable) != 1 || stable[0] != "switch.kitchen_outlet" {
		t.Errorf("stable = %v, want [switch.kitchen_outlet]", stable)
	}

	// filtered_count counts the 2 entities filtered by disabled/diagnostic.
	if parsed["filtered_count"] != float64(2) {
		t.Errorf("filtered_count = %v, want 2 (disabled + diagnostic)", parsed["filtered_count"])
	}
}

func TestComputeAreaActivity_ResolvesByNameAliasAndID(t *testing.T) {
	now := testNow
	areas := []homeassistant.Area{
		{AreaID: "primary_bath", Name: "Primary Bathroom", Aliases: []string{"Master Bath"}},
	}
	entities := []homeassistant.EntityRegistryEntry{
		{EntityID: "light.bath", AreaID: "primary_bath"},
	}
	states := []homeassistant.State{
		{EntityID: "light.bath", State: "off", LastChanged: now.Add(-1 * time.Hour)},
	}
	client := &fakeAreaClient{areas: areas, entities: entities, states: states}

	for _, query := range []string{"primary_bath", "Primary Bathroom", "primary bathroom", "Master Bath"} {
		t.Run(query, func(t *testing.T) {
			got, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: query}, now)
			if err != nil {
				t.Fatalf("ComputeAreaActivity(%q): %v", query, err)
			}
			if !strings.Contains(got, `"area_id":"primary_bath"`) {
				t.Errorf("expected area resolution to succeed for %q, got %s", query, got)
			}
		})
	}
}

func TestComputeAreaActivity_TimelineFiltersNumericNoiseKeepsAlarms(t *testing.T) {
	now := testNow
	areas := []homeassistant.Area{{AreaID: "kitchen", Name: "Kitchen"}}
	entities := []homeassistant.EntityRegistryEntry{
		{EntityID: "sensor.kitchen_temp", AreaID: "kitchen", DeviceClass: "temperature"},
		{EntityID: "binary_sensor.kitchen_motion", AreaID: "kitchen", DeviceClass: "motion"},
		{EntityID: "binary_sensor.kitchen_smoke", AreaID: "kitchen", DeviceClass: "smoke"},
	}
	states := []homeassistant.State{
		{EntityID: "sensor.kitchen_temp", State: "72.4", LastChanged: now.Add(-30 * time.Minute), Attributes: map[string]any{"device_class": "temperature"}},
		{EntityID: "binary_sensor.kitchen_motion", State: "clear", LastChanged: now.Add(-2 * time.Minute), Attributes: map[string]any{"device_class": "motion"}},
		{EntityID: "binary_sensor.kitchen_smoke", State: "off", LastChanged: now.Add(-2 * time.Hour), Attributes: map[string]any{"device_class": "smoke"}},
	}
	logbook := []homeassistant.LogbookEntry{
		// Numeric drift on a temperature sensor — should be filtered out.
		{When: float64(now.Add(-15 * time.Minute).Unix()), EntityID: "sensor.kitchen_temp", State: "72.5", Domain: "sensor"},
		{When: float64(now.Add(-10 * time.Minute).Unix()), EntityID: "sensor.kitchen_temp", State: "72.4", Domain: "sensor"},
		// Discrete transitions on the motion sensor — kept.
		{When: float64(now.Add(-8 * time.Minute).Unix()), EntityID: "binary_sensor.kitchen_motion", State: "on", Domain: "binary_sensor"},
		{When: float64(now.Add(-3 * time.Minute).Unix()), EntityID: "binary_sensor.kitchen_motion", State: "off", Domain: "binary_sensor"},
		// Sentinel noise — filtered.
		{When: float64(now.Add(-5 * time.Minute).Unix()), EntityID: "binary_sensor.kitchen_smoke", State: "unavailable", Domain: "binary_sensor"},
	}

	client := &fakeAreaClient{areas: areas, entities: entities, states: states, logbook: logbook}
	got, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "Kitchen"}, now)
	if err != nil {
		t.Fatalf("ComputeAreaActivity: %v", err)
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(got), &parsed)

	timeline, _ := parsed["timeline"].([]any)
	if len(timeline) != 2 {
		t.Fatalf("timeline len = %d, want 2 (motion transitions kept, temp drift + sentinel filtered): %v", len(timeline), timeline)
	}
	for _, ev := range timeline {
		obj := ev.(map[string]any)
		if obj["entity"] != "binary_sensor.kitchen_motion" {
			t.Errorf("timeline contains non-motion entry: %v", obj)
		}
	}
	// Newest first — and translated through semanticState so the
	// motion-off entry reads as "clear", not raw "off". Timeline
	// vocabulary matches the bucketed-entity vocabulary for the
	// same device_class.
	first := timeline[0].(map[string]any)
	if first["state"] != "clear" {
		t.Errorf("first timeline state = %v, want clear (motion off translated)", first["state"])
	}
	second := timeline[1].(map[string]any)
	if second["state"] != "detected" {
		t.Errorf("second timeline state = %v, want detected (motion on translated)", second["state"])
	}
}

func TestComputeAreaActivity_StableBucketCappedAndCounted(t *testing.T) {
	now := testNow
	areas := []homeassistant.Area{{AreaID: "z", Name: "Zone"}}
	entities := make([]homeassistant.EntityRegistryEntry, 0, 8)
	states := make([]homeassistant.State, 0, 8)
	for i := 0; i < 8; i++ {
		eid := "switch.s" + strconv.Itoa(i)
		entities = append(entities, homeassistant.EntityRegistryEntry{EntityID: eid, AreaID: "z"})
		states = append(states, homeassistant.State{EntityID: eid, State: "off", LastChanged: now.Add(-24 * time.Hour)})
	}
	client := &fakeAreaClient{areas: areas, entities: entities, states: states}

	got, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "Zone", MaxStable: 3}, now)
	if err != nil {
		t.Fatalf("ComputeAreaActivity: %v", err)
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(got), &parsed)

	stable, _ := parsed["stable"].([]any)
	if len(stable) != 3 {
		t.Errorf("stable len = %d, want 3 (cap)", len(stable))
	}
	if parsed["stable_truncated_count"] != float64(5) {
		t.Errorf("stable_truncated_count = %v, want 5", parsed["stable_truncated_count"])
	}
}

func TestComputeAreaActivity_EntityInheritsAreaFromDevice(t *testing.T) {
	now := testNow
	areas := []homeassistant.Area{{AreaID: "garage", Name: "Garage"}}
	entities := []homeassistant.EntityRegistryEntry{
		{EntityID: "binary_sensor.garage_door", DeviceID: "dev-1"},
	}
	devices := []homeassistant.DeviceRegistryEntry{
		{ID: "dev-1", AreaID: "garage"},
	}
	states := []homeassistant.State{
		{EntityID: "binary_sensor.garage_door", State: "off", LastChanged: now.Add(-30 * time.Minute), Attributes: map[string]any{"device_class": "garage_door"}},
	}

	client := &fakeAreaClient{areas: areas, entities: entities, devices: devices, states: states}
	got, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "garage"}, now)
	if err != nil {
		t.Fatalf("ComputeAreaActivity: %v", err)
	}
	if !strings.Contains(got, "binary_sensor.garage_door") {
		t.Errorf("entity inheriting area from device should be included, got %s", got)
	}
}

func TestComputeAreaActivity_UnknownAreaErrors(t *testing.T) {
	client := &fakeAreaClient{areas: []homeassistant.Area{{AreaID: "kitchen", Name: "Kitchen"}}}
	_, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "Bathroom"}, testNow)
	if err == nil {
		t.Fatal("expected error for unknown area")
	}
}

func TestComputeAreaActivity_AreasFetchErrorPropagates(t *testing.T) {
	client := &fakeAreaClient{areasErr: errors.New("ws disconnected")}
	_, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "Kitchen"}, testNow)
	if err == nil {
		t.Fatal("expected error when area fetch fails")
	}
}

func TestComputeAreaActivity_TimelineUsesStateAttributeDeviceClass(t *testing.T) {
	// Some integrations expose device_class only as a live-state
	// attribute, not on the entity registry row. The timeline must
	// still translate via semanticState in that case so its
	// vocabulary stays consistent with the bucketed entity output.
	now := testNow
	areas := []homeassistant.Area{{AreaID: "kitchen", Name: "Kitchen"}}
	entities := []homeassistant.EntityRegistryEntry{
		// Registry has no device_class set.
		{EntityID: "binary_sensor.kitchen_door", AreaID: "kitchen"},
	}
	states := []homeassistant.State{
		// State carries the device_class instead.
		{
			EntityID:    "binary_sensor.kitchen_door",
			State:       "off",
			LastChanged: now.Add(-30 * time.Minute),
			Attributes:  map[string]any{"device_class": "door"},
		},
	}
	logbook := []homeassistant.LogbookEntry{
		{When: float64(now.Add(-10 * time.Minute).Unix()), EntityID: "binary_sensor.kitchen_door", State: "on", Domain: "binary_sensor"},
		{When: float64(now.Add(-2 * time.Minute).Unix()), EntityID: "binary_sensor.kitchen_door", State: "off", Domain: "binary_sensor"},
	}

	client := &fakeAreaClient{areas: areas, entities: entities, states: states, logbook: logbook}
	got, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "Kitchen"}, now)
	if err != nil {
		t.Fatalf("ComputeAreaActivity: %v", err)
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(got), &parsed)

	timeline, _ := parsed["timeline"].([]any)
	if len(timeline) != 2 {
		t.Fatalf("timeline len = %d, want 2: %v", len(timeline), timeline)
	}
	first := timeline[0].(map[string]any)
	if first["state"] != "closed" {
		t.Errorf("first timeline state = %v, want closed (door off translated even though device_class is only on the live state)", first["state"])
	}
	second := timeline[1].(map[string]any)
	if second["state"] != "open" {
		t.Errorf("second timeline state = %v, want open (door on translated)", second["state"])
	}
}

func TestComputeAreaActivity_RecentChangesTruncatedCount(t *testing.T) {
	now := testNow
	areas := []homeassistant.Area{{AreaID: "z", Name: "Zone"}}
	entities := make([]homeassistant.EntityRegistryEntry, 0, 12)
	states := make([]homeassistant.State, 0, 12)
	for i := 0; i < 12; i++ {
		eid := "switch.r" + strconv.Itoa(i)
		entities = append(entities, homeassistant.EntityRegistryEntry{EntityID: eid, AreaID: "z"})
		// Each switch is "off" but changed within the lookback window —
		// they all land in recent_changes, exceeding the cap of 10.
		states = append(states, homeassistant.State{
			EntityID:    eid,
			State:       "off",
			LastChanged: now.Add(-time.Duration(i+1) * time.Minute),
		})
	}
	client := &fakeAreaClient{areas: areas, entities: entities, states: states}

	got, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "Zone"}, now)
	if err != nil {
		t.Fatalf("ComputeAreaActivity: %v", err)
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(got), &parsed)

	recent, _ := parsed["recent_changes"].([]any)
	if len(recent) != 10 {
		t.Errorf("recent_changes len = %d, want 10 (cap)", len(recent))
	}
	if parsed["recent_changes_truncated_count"] != float64(2) {
		t.Errorf("recent_changes_truncated_count = %v, want 2", parsed["recent_changes_truncated_count"])
	}
}

func TestOptionalIntArg_Validation(t *testing.T) {
	cases := []struct {
		name      string
		args      map[string]any
		key       string
		wantValue *int
		wantErr   bool
	}{
		{name: "missing returns nil", args: map[string]any{}, key: "x"},
		{name: "json null returns nil", args: map[string]any{"x": nil}, key: "x"},
		{name: "valid float64 whole number", args: map[string]any{"x": float64(3600)}, key: "x", wantValue: ptrInt(3600)},
		{name: "valid int", args: map[string]any{"x": 3600}, key: "x", wantValue: ptrInt(3600)},
		{name: "fractional float rejected", args: map[string]any{"x": 3600.5}, key: "x", wantErr: true},
		{name: "NaN rejected", args: map[string]any{"x": math.NaN()}, key: "x", wantErr: true},
		{name: "Inf rejected", args: map[string]any{"x": math.Inf(1)}, key: "x", wantErr: true},
		{name: "negative rejected", args: map[string]any{"x": float64(-1)}, key: "x", wantErr: true},
		{name: "negative int rejected", args: map[string]any{"x": -5}, key: "x", wantErr: true},
		{name: "zero allowed", args: map[string]any{"x": 0}, key: "x", wantValue: ptrInt(0)},
		{name: "string rejected", args: map[string]any{"x": "3600"}, key: "x", wantErr: true},
		{name: "out-of-range float rejected", args: map[string]any{"x": float64(1e30)}, key: "x", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := optionalIntArg(tc.args, tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got value=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantValue == nil {
				if got != nil {
					t.Errorf("got %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tc.wantValue {
				t.Errorf("got %v, want %d", got, *tc.wantValue)
			}
		})
	}
}

func ptrInt(v int) *int { return &v }

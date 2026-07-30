package awareness

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestParseSubscriptionTarget(t *testing.T) {
	cases := []struct {
		raw   string
		kind  SubscriptionTargetKind
		value string
	}{
		{"sensor.office_temperature", TargetEntity, "sensor.office_temperature"},
		{"binary_sensor.*door*", TargetGlob, "binary_sensor.*door*"},
		{"area:office", TargetArea, "office"},
		{"label:critical_lights", TargetLabel, "critical_lights"},
		{"floor:upstairs", TargetFloor, "upstairs"},
		{"weather.home", TargetEntity, "weather.home"}, // dot, not a glob or prefix
		{"area:", TargetEntity, "area:"},               // empty id: malformed, not a wildcard-all
		{"floor: ", TargetEntity, "floor:"},            // whitespace-only id: same
	}
	for _, c := range cases {
		got := ParseSubscriptionTarget(c.raw)
		if got.Kind != c.kind || got.Value != c.value {
			t.Errorf("ParseSubscriptionTarget(%q) = {%v %q}, want {%v %q}", c.raw, got.Kind, got.Value, c.kind, c.value)
		}
		// Round-trips back to stored form for registry targets.
		if got.IsRegistryTarget() && got.String() != c.raw {
			t.Errorf("String() = %q, want round-trip %q", got.String(), c.raw)
		}
	}
}

// fakeHARegistry is a StateGetter that also serves registry data, so
// registry-target expansion can be exercised end to end.
type fakeHARegistry struct {
	fakeHA
	entities []homeassistant.EntityRegistryEntry
	devices  []homeassistant.DeviceRegistryEntry
	areas    []homeassistant.Area
	floors   []homeassistant.FloorRegistryEntry
	labels   []homeassistant.LabelRegistryEntry
}

func (f *fakeHARegistry) GetEntityRegistry(context.Context) ([]homeassistant.EntityRegistryEntry, error) {
	return f.entities, nil
}
func (f *fakeHARegistry) GetDeviceRegistry(context.Context) ([]homeassistant.DeviceRegistryEntry, error) {
	return f.devices, nil
}
func (f *fakeHARegistry) GetAreas(context.Context) ([]homeassistant.Area, error) { return f.areas, nil }
func (f *fakeHARegistry) GetFloorRegistry(context.Context) ([]homeassistant.FloorRegistryEntry, error) {
	return f.floors, nil
}
func (f *fakeHARegistry) GetLabelRegistry(context.Context) ([]homeassistant.LabelRegistryEntry, error) {
	return f.labels, nil
}
func (f *fakeHARegistry) GetConfigEntries(context.Context) ([]homeassistant.ConfigEntry, error) {
	return nil, nil
}

// officeRegistry seeds an office with two entities addressed different
// ways: one carries area_id directly, the other inherits area and a
// label from its device. This proves the inheritance rules.
func officeRegistry() *fakeHARegistry {
	mk := func(id, state string) *homeassistant.State {
		return &homeassistant.State{EntityID: id, State: state}
	}
	f := &fakeHARegistry{}
	f.states = map[string]*homeassistant.State{
		"light.office_overhead": mk("light.office_overhead", "on"),
		"sensor.office_power":   mk("sensor.office_power", "42"),
		"light.garage":          mk("light.garage", "off"),
	}
	f.entities = []homeassistant.EntityRegistryEntry{
		{EntityID: "light.office_overhead", AreaID: "office", Labels: []string{"critical"}},
		{EntityID: "sensor.office_power", DeviceID: "dev1"}, // area + label inherited from device
		{EntityID: "light.garage", AreaID: "garage"},
	}
	f.devices = []homeassistant.DeviceRegistryEntry{
		{ID: "dev1", AreaID: "office", Labels: []string{"critical"}},
	}
	f.areas = []homeassistant.Area{
		{AreaID: "office", Name: "Office", FloorID: "main"},
		{AreaID: "garage", Name: "Garage", FloorID: "main"},
	}
	f.floors = []homeassistant.FloorRegistryEntry{{FloorID: "main", Name: "Main"}}
	f.labels = []homeassistant.LabelRegistryEntry{{LabelID: "critical", Name: "Critical"}}
	return f
}

func setupRegistryProvider(t *testing.T, ha *fakeHARegistry) (*WatchlistProvider, *WatchlistStore) {
	t.Helper()
	p, store := setupTestProvider(t, ha)
	p.SetRegistryClient(ha)
	return p, store
}

func TestRegistryTarget_AreaExpandsWithInheritance(t *testing.T) {
	ha := officeRegistry()
	p, store := setupRegistryProvider(t, ha)
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "area:office"}); err != nil {
		t.Fatalf("add area target: %v", err)
	}
	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	// Both office entities render — the direct one and the device-inherited one.
	for _, want := range []string{"light.office_overhead", "sensor.office_power"} {
		if !strings.Contains(got, want) {
			t.Errorf("area:office should include %s (inheritance):\n%s", want, got)
		}
	}
	if strings.Contains(got, "light.garage") {
		t.Errorf("area:office must not include garage:\n%s", got)
	}
}

func TestRegistryTarget_LabelAndFloor(t *testing.T) {
	ha := officeRegistry()

	// Label: both office entities carry "critical" (one direct, one via device).
	p, store := setupRegistryProvider(t, ha)
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "label:critical"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, _ := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if !strings.Contains(got, "light.office_overhead") || !strings.Contains(got, "sensor.office_power") {
		t.Errorf("label:critical should match both office entities:\n%s", got)
	}

	// Floor: main covers office + garage, so all three entities.
	p2, store2 := setupRegistryProvider(t, ha)
	if err := store2.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "floor:main"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	got2, _ := p2.TagContext(context.Background(), agentctx.ContextRequest{})
	for _, want := range []string{"light.office_overhead", "sensor.office_power", "light.garage"} {
		if !strings.Contains(got2, want) {
			t.Errorf("floor:main should include %s:\n%s", want, got2)
		}
	}
}

func TestRegistryTarget_UnknownAreaIsSilent(t *testing.T) {
	ha := officeRegistry()
	p, store := setupRegistryProvider(t, ha)
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "area:atrium"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	// No members → the whole provider renders empty (like an empty glob).
	if got != "" {
		t.Errorf("unknown area should render nothing, got:\n%s", got)
	}
}

func TestRegistryTarget_NoRegistryClientMarksUnavailable(t *testing.T) {
	ha := officeRegistry()
	// setupTestProvider does NOT wire the registry client.
	p, store := setupTestProvider(t, ha)
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "area:office"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(got, "fetch_error") || !strings.Contains(got, "area:office") {
		t.Errorf("no registry client should mark the target unavailable, got:\n%s", got)
	}
}

// salienceRegistry seeds an office whose registry carries one entity of
// each salience class, all with live state, so filtering decisions are
// attributable to the registry entry alone.
func salienceRegistry() *fakeHARegistry {
	mk := func(id, state string) *homeassistant.State {
		return &homeassistant.State{EntityID: id, State: state}
	}
	f := &fakeHARegistry{}
	f.states = map[string]*homeassistant.State{
		"light.office_overhead":     mk("light.office_overhead", "on"),
		"sensor.office_hidden_dev":  mk("sensor.office_hidden_dev", "13"),
		"sensor.office_wifi_signal": mk("sensor.office_wifi_signal", "-61"),
		"switch.office_led_config":  mk("switch.office_led_config", "off"),
	}
	f.entities = []homeassistant.EntityRegistryEntry{
		{EntityID: "light.office_overhead", AreaID: "office"},
		{EntityID: "sensor.office_hidden_dev", AreaID: "office", HiddenBy: "user"},
		{EntityID: "sensor.office_wifi_signal", AreaID: "office", EntityCategory: "diagnostic"},
		{EntityID: "switch.office_led_config", AreaID: "office", EntityCategory: "config"},
	}
	f.areas = []homeassistant.Area{{AreaID: "office", Name: "Office", FloorID: "main"}}
	f.floors = []homeassistant.FloorRegistryEntry{{FloorID: "main", Name: "Main"}}
	return f
}

// TestRegistryTarget_ExpansionHonorsVisibility pins the trust boundary
// that makes subscribing a whole area safe without inspecting it: the
// owner's HA curation is the filter. Hidden and diagnostic/config
// members stay out of the default expansion; the include flags widen
// it deliberately.
func TestRegistryTarget_ExpansionHonorsVisibility(t *testing.T) {
	tests := []struct {
		name    string
		sub     looppkg.EntitySubscription
		wantIn  []string
		wantOut []string
	}{
		{
			name:    "default expansion is the visible, non-diagnostic office",
			sub:     looppkg.EntitySubscription{EntityID: "area:office"},
			wantIn:  []string{"light.office_overhead"},
			wantOut: []string{"sensor.office_hidden_dev", "sensor.office_wifi_signal", "switch.office_led_config"},
		},
		{
			name:    "include_hidden widens to hidden members only",
			sub:     looppkg.EntitySubscription{EntityID: "area:office", IncludeHidden: true},
			wantIn:  []string{"light.office_overhead", "sensor.office_hidden_dev"},
			wantOut: []string{"sensor.office_wifi_signal", "switch.office_led_config"},
		},
		{
			name:    "include_diagnostic widens to diagnostic and config members",
			sub:     looppkg.EntitySubscription{EntityID: "area:office", IncludeDiagnostic: true},
			wantIn:  []string{"light.office_overhead", "sensor.office_wifi_signal", "switch.office_led_config"},
			wantOut: []string{"sensor.office_hidden_dev"},
		},
		{
			name:   "both flags expand the whole registry membership",
			sub:    looppkg.EntitySubscription{EntityID: "area:office", IncludeHidden: true, IncludeDiagnostic: true},
			wantIn: []string{"light.office_overhead", "sensor.office_hidden_dev", "sensor.office_wifi_signal", "switch.office_led_config"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ha := salienceRegistry()
			p, store := setupRegistryProvider(t, ha)
			if err := store.Upsert(OwnerCore, tt.sub); err != nil {
				t.Fatalf("add subscription: %v", err)
			}
			got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
			if err != nil {
				t.Fatalf("TagContext: %v", err)
			}
			for _, want := range tt.wantIn {
				if !strings.Contains(got, want) {
					t.Errorf("expansion should include %s:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.wantOut {
				if strings.Contains(got, unwanted) {
					t.Errorf("expansion should exclude %s:\n%s", unwanted, got)
				}
			}
		})
	}
}

// TestRegistryTarget_ConcreteHiddenEntityStillWatched pins the boundary
// of the salience filter: it applies to membership the resolver infers,
// never to an entity the author named. A concrete watch on a hidden
// entity is deliberate.
func TestRegistryTarget_ConcreteHiddenEntityStillWatched(t *testing.T) {
	ha := salienceRegistry()
	p, store := setupRegistryProvider(t, ha)
	if err := store.Upsert(OwnerCore, looppkg.EntitySubscription{EntityID: "sensor.office_hidden_dev"}); err != nil {
		t.Fatalf("add subscription: %v", err)
	}
	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(got, "sensor.office_hidden_dev") {
		t.Errorf("a concretely named hidden entity must render:\n%s", got)
	}
}

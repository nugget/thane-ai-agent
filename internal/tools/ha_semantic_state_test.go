package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant/contextfmt"
)

// semanticStateFixture registers a garage_door binary_sensor (the prod bug:
// operator "Show as: Garage Door" → device_class garage_door, which HA writes
// into the state attributes) and a plain binary_sensor with no device_class —
// so we can prove ha_search_states / ha_list_entities carry the same
// class-aware translation ha_get_state and the snapshots already do. Numeric
// rounding parity is covered separately in TestHASemanticState_MatchesContextfmt.
func semanticStateFixture(t *testing.T) *Registry {
	t.Helper()
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{
			EntityID:   "binary_sensor.zone25_garage_bay_3",
			State:      "on",
			Attributes: map[string]any{"device_class": "garage_door", "friendly_name": "Garage Bay 3"},
		},
		{
			// No device_class → nothing to translate, raw state passes through.
			EntityID:   "binary_sensor.mystery",
			State:      "on",
			Attributes: map[string]any{"friendly_name": "Mystery"},
		},
	}
	return fake.registry(t)
}

func itemStateByID(items []haListEntityItem, id string) (string, bool) {
	for _, it := range items {
		if it.EntityID == id {
			return it.State, true
		}
	}
	return "", false
}

func TestHAListEntities_TranslatesDeviceClassState(t *testing.T) {
	reg := semanticStateFixture(t)
	out, err := reg.Execute(context.Background(), "ha_list_entities", `{"domain":"binary_sensor"}`)
	if err != nil {
		t.Fatalf("ha_list_entities: %v", err)
	}
	var res haListEntitiesResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}

	if got, ok := itemStateByID(res.Items, "binary_sensor.zone25_garage_bay_3"); !ok || got != "open" {
		t.Errorf("garage_door state = %q (found=%v), want \"open\" — device_class transform must be honored", got, ok)
	}
	if got, ok := itemStateByID(res.Items, "binary_sensor.mystery"); !ok || got != "on" {
		t.Errorf("plain binary_sensor state = %q (found=%v), want passthrough \"on\"", got, ok)
	}
}

func TestHASearchStates_TranslatesDeviceClassState(t *testing.T) {
	reg := semanticStateFixture(t)
	// The filter still speaks raw HA state ("on"); the rendered result is
	// the semantic label ("open"), matching ha_get_state and the snapshots.
	out, err := reg.Execute(context.Background(), "ha_search_states", `{"state":["on"]}`)
	if err != nil {
		t.Fatalf("ha_search_states: %v", err)
	}
	var res haSearchStatesResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}

	if got, ok := itemStateByID(res.Items, "binary_sensor.zone25_garage_bay_3"); !ok || got != "open" {
		t.Errorf("garage_door state = %q (found=%v), want \"open\"", got, ok)
	}
}

// haSemanticState must be exactly the canonical contextfmt projection — the
// whole point is that search/list match ha_get_state and the snapshots. This
// covers the numeric-rounding branch (rounded by device_class) that the
// binary_sensor cases above don't reach.
func TestHASemanticState_MatchesContextfmt(t *testing.T) {
	cases := []homeassistant.State{
		{EntityID: "binary_sensor.garage", State: "on", Attributes: map[string]any{"device_class": "garage_door"}},
		{EntityID: "sensor.office_temperature", State: "72.13", Attributes: map[string]any{"device_class": "temperature"}},
		{EntityID: "binary_sensor.mystery", State: "on"},                                               // no device_class → passthrough
		{EntityID: "sensor.unavail", State: "unavailable"},                                             // sentinel → passthrough
		{EntityID: "cover.front", State: "open", Attributes: map[string]any{"device_class": "garage"}}, // non-binary domain
	}
	for _, s := range cases {
		want := contextfmt.SemanticState(
			contextfmt.EntityDomain(s.EntityID),
			contextfmt.AttrString(s.Attributes, "device_class"),
			s.State,
		)
		if got := haSemanticState(s); got != want {
			t.Errorf("haSemanticState(%s state=%q) = %q, want %q (contextfmt parity)", s.EntityID, s.State, got, want)
		}
	}
}

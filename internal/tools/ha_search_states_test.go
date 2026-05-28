package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func decodeSearchStates(t *testing.T, raw string) haSearchStatesResult {
	t.Helper()
	var out haSearchStatesResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal result: %v\n%s", err, raw)
	}
	return out
}

func searchStatesEntityIDs(res haSearchStatesResult) map[string]bool {
	ids := make(map[string]bool, len(res.Items))
	for _, it := range res.Items {
		ids[it.EntityID] = true
	}
	return ids
}

func TestHASearchStates_StateAndDomainFilter(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{EntityID: "light.office", State: "on", Attributes: map[string]any{"friendly_name": "Office"}},
		{EntityID: "light.hall", State: "off", Attributes: map[string]any{"friendly_name": "Hall"}},
		{EntityID: "switch.fan", State: "on", Attributes: map[string]any{"friendly_name": "Fan"}},
	}
	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_search_states", `{"domain":"light","state":["on"]}`)
	if err != nil {
		t.Fatalf("ha_search_states: %v", err)
	}
	res := decodeSearchStates(t, result)
	if res.Count != 1 || res.Total != 1 {
		t.Fatalf("count/total = %d/%d, want 1/1\n%s", res.Count, res.Total, result)
	}
	ids := searchStatesEntityIDs(res)
	if !ids["light.office"] || ids["light.hall"] || ids["switch.fan"] {
		t.Errorf("matched ids = %v, want only light.office", ids)
	}
}

func TestHASearchStates_StateAcrossDomains(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{EntityID: "binary_sensor.door", State: "unavailable"},
		{EntityID: "sensor.temp", State: "unavailable"},
		{EntityID: "light.lamp", State: "on"},
	}
	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_search_states", `{"state":["unavailable","unknown"]}`)
	if err != nil {
		t.Fatalf("ha_search_states: %v", err)
	}
	res := decodeSearchStates(t, result)
	ids := searchStatesEntityIDs(res)
	if len(ids) != 2 || !ids["binary_sensor.door"] || !ids["sensor.temp"] {
		t.Errorf("matched ids = %v, want door+temp", ids)
	}
}

func TestHASearchStates_NumericAttributePredicate(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{EntityID: "sensor.a_batt", State: "12", Attributes: map[string]any{"battery": float64(12)}},
		{EntityID: "sensor.b_batt", State: "88", Attributes: map[string]any{"battery": float64(88)}},
		{EntityID: "sensor.c_batt", State: "5", Attributes: map[string]any{"battery": "5"}}, // string-encoded
		{EntityID: "sensor.no_batt", State: "on", Attributes: map[string]any{}},
	}
	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_search_states", `{"attribute":"battery","comparison":"<","value":20}`)
	if err != nil {
		t.Fatalf("ha_search_states: %v", err)
	}
	res := decodeSearchStates(t, result)
	ids := searchStatesEntityIDs(res)
	if len(ids) != 2 || !ids["sensor.a_batt"] || !ids["sensor.c_batt"] {
		t.Errorf("matched ids = %v, want a_batt(12) + c_batt(5); b_batt(88) and no_batt excluded", ids)
	}
}

func TestHASearchStates_AreaFilter(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{EntityID: "light.office_lamp", State: "on", Attributes: map[string]any{"friendly_name": "Office Lamp"}},
		{EntityID: "light.kitchen_lamp", State: "on", Attributes: map[string]any{"friendly_name": "Kitchen Lamp"}},
	}
	fake.areas = []map[string]any{
		{"area_id": "office", "name": "Office"},
		{"area_id": "kitchen", "name": "Kitchen"},
	}
	fake.entityRows = []map[string]any{
		{"entity_id": "light.office_lamp", "area_id": "office"},
		{"entity_id": "light.kitchen_lamp", "area_id": "kitchen"},
	}
	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_search_states", `{"state":["on"],"area":"Office"}`)
	if err != nil {
		t.Fatalf("ha_search_states: %v", err)
	}
	res := decodeSearchStates(t, result)
	ids := searchStatesEntityIDs(res)
	if len(ids) != 1 || !ids["light.office_lamp"] {
		t.Errorf("matched ids = %v, want only light.office_lamp (area Office)", ids)
	}
}

func TestHASearchStates_RendersIncludeMetadataWithoutLeakingAreaOnAreaFilter(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{EntityID: "light.office_lamp", State: "on", Attributes: map[string]any{"friendly_name": "Office Lamp"}},
	}
	fake.areas = []map[string]any{{"area_id": "office", "name": "Office"}}
	fake.entityRows = []map[string]any{
		{"entity_id": "light.office_lamp", "area_id": "office", "platform": "hue"},
	}
	reg := fake.registry(t)

	// Filter by area but only request description metadata. The area
	// resolution must not leak an area block into the rendered output.
	result, err := reg.Execute(context.Background(), "ha_search_states", `{"state":["on"],"area":"office","include":{"description":true}}`)
	if err != nil {
		t.Fatalf("ha_search_states: %v", err)
	}
	res := decodeSearchStates(t, result)
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	meta := res.Items[0].Metadata
	if meta == nil {
		t.Fatal("expected description metadata to be rendered")
	}
	if meta.Area != nil {
		t.Errorf("area metadata leaked into output despite include only requesting description: %#v", meta.Area)
	}
}

func TestHASearchStates_ScopedEnrichmentWithoutAreaFilter(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{EntityID: "sensor.office_temp", State: "72", Attributes: map[string]any{"friendly_name": "Office Temp"}},
	}
	fake.entityRows = []map[string]any{
		{"entity_id": "sensor.office_temp", "description": "Ambient office temperature", "platform": "zwave_js"},
	}
	reg := fake.registry(t)

	// include without area must still render description metadata —
	// this exercises the post-limit scoped registry fetch path
	// (fetchHAEntityMetadataBundleForEntityIDs), not the bulk path.
	result, err := reg.Execute(context.Background(), "ha_search_states", `{"state":["72"],"include":{"description":true}}`)
	if err != nil {
		t.Fatalf("ha_search_states: %v", err)
	}
	res := decodeSearchStates(t, result)
	if len(res.Items) != 1 || res.Items[0].Metadata == nil {
		t.Fatalf("expected one enriched item, got %#v", res.Items)
	}
	if res.Items[0].Metadata.Description != "Ambient office temperature" {
		t.Errorf("description = %q, want registry description", res.Items[0].Metadata.Description)
	}
}

func TestHASearchStates_RequiresAFilter(t *testing.T) {
	fake := newFakeHAServer(t)
	reg := fake.registry(t)
	if _, err := reg.Execute(context.Background(), "ha_search_states", `{}`); err == nil {
		t.Fatal("expected error when no filter is supplied")
	}
}

func TestHASearchStates_PartialAttributePredicateRejected(t *testing.T) {
	fake := newFakeHAServer(t)
	reg := fake.registry(t)
	// attribute without comparison/value is incomplete.
	if _, err := reg.Execute(context.Background(), "ha_search_states", `{"attribute":"battery"}`); err == nil {
		t.Fatal("expected error for attribute predicate missing comparison/value")
	}
	// bad comparison operator.
	if _, err := reg.Execute(context.Background(), "ha_search_states", `{"attribute":"battery","comparison":"=<","value":10}`); err == nil {
		t.Fatal("expected error for invalid comparison operator")
	}
}

func TestHASearchStates_Truncation(t *testing.T) {
	fake := newFakeHAServer(t)
	for i := 0; i < 5; i++ {
		fake.states = append(fake.states, homeassistant.State{
			EntityID: "light.l" + string(rune('a'+i)),
			State:    "on",
		})
	}
	reg := fake.registry(t)

	result, err := reg.Execute(context.Background(), "ha_search_states", `{"state":["on"],"limit":2}`)
	if err != nil {
		t.Fatalf("ha_search_states: %v", err)
	}
	res := decodeSearchStates(t, result)
	if res.Count != 2 || res.Total != 5 || !res.Truncated {
		t.Errorf("count=%d total=%d truncated=%v, want 2/5/true", res.Count, res.Total, res.Truncated)
	}
}

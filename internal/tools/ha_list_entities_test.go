package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func listEntitiesFixture(t *testing.T) *Registry {
	t.Helper()
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{EntityID: "binary_sensor.front_door", State: "off"},
		{EntityID: "binary_sensor.back_door", State: "on"},
		{EntityID: "binary_sensor.kitchen_window", State: "off"},
		{EntityID: "sensor.office_temperature", State: "72"},
		{EntityID: "climate.living_room_temperature", State: "70"},
		{EntityID: "light.office_lamp", State: "on"},
		{EntityID: "light.office_desk", State: "off"},
		{EntityID: "light.hall", State: "off"},
	}
	return fake.registry(t)
}

func listEntities(t *testing.T, reg *Registry, argsJSON string) haListEntitiesResult {
	t.Helper()
	out, err := reg.Execute(context.Background(), "ha_list_entities", argsJSON)
	if err != nil {
		t.Fatalf("ha_list_entities(%s): %v", argsJSON, err)
	}
	var res haListEntitiesResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	return res
}

func listEntityIDs(res haListEntitiesResult) map[string]bool {
	ids := make(map[string]bool, len(res.Items))
	for _, it := range res.Items {
		ids[it.EntityID] = true
	}
	return ids
}

func TestHAListEntities_DomainPrefixUnchanged(t *testing.T) {
	reg := listEntitiesFixture(t)
	res := listEntities(t, reg, `{"domain":"light"}`)
	if res.Total != 3 || res.Count != 3 {
		t.Fatalf("light total/count = %d/%d, want 3/3", res.Total, res.Count)
	}
	ids := listEntityIDs(res)
	if !ids["light.office_lamp"] || !ids["light.hall"] || ids["binary_sensor.front_door"] {
		t.Errorf("domain filter leaked: %v", ids)
	}
}

func TestHAListEntities_PatternWithinDomain(t *testing.T) {
	reg := listEntitiesFixture(t)
	res := listEntities(t, reg, `{"pattern":"binary_sensor.*door*"}`)
	ids := listEntityIDs(res)
	if len(ids) != 2 || !ids["binary_sensor.front_door"] || !ids["binary_sensor.back_door"] {
		t.Errorf("matched = %v, want front_door + back_door (not kitchen_window)", ids)
	}
}

func TestHAListEntities_PatternCrossDomainSuffix(t *testing.T) {
	reg := listEntitiesFixture(t)
	res := listEntities(t, reg, `{"pattern":"*_temperature"}`)
	ids := listEntityIDs(res)
	if len(ids) != 2 || !ids["sensor.office_temperature"] || !ids["climate.living_room_temperature"] {
		t.Errorf("matched = %v, want the two *_temperature entities across domains", ids)
	}
}

func TestHAListEntities_DomainAndPatternCombine(t *testing.T) {
	reg := listEntitiesFixture(t)
	res := listEntities(t, reg, `{"domain":"light","pattern":"*office*"}`)
	ids := listEntityIDs(res)
	if len(ids) != 2 || !ids["light.office_lamp"] || !ids["light.office_desk"] {
		t.Errorf("matched = %v, want only office lights (domain AND pattern)", ids)
	}
}

func TestHAListEntities_NoMatch(t *testing.T) {
	reg := listEntitiesFixture(t)
	res := listEntities(t, reg, `{"pattern":"*nonexistent*"}`)
	if res.Total != 0 || res.Count != 0 || len(res.Items) != 0 {
		t.Errorf("expected zero matches, got %#v", res)
	}
}

func TestHAListEntities_MalformedPatternErrors(t *testing.T) {
	reg := listEntitiesFixture(t)
	if _, err := reg.Execute(context.Background(), "ha_list_entities", `{"pattern":"light.[bad"}`); err == nil {
		t.Fatal("expected error for malformed glob pattern")
	}
}

func TestHAListEntities_RequiresDomainOrPattern(t *testing.T) {
	reg := listEntitiesFixture(t)
	if _, err := reg.Execute(context.Background(), "ha_list_entities", `{}`); err == nil {
		t.Fatal("expected error when neither domain nor pattern is supplied")
	}
}

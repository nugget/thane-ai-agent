package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// visibilityFixture seeds three lights, one hidden by the operator.
func visibilityFixture(t *testing.T) *fakeHAServer {
	t.Helper()
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{EntityID: "light.office_main", State: "on", Attributes: map[string]any{"friendly_name": "Office Main"}},
		{EntityID: "light.office_debug", State: "off", Attributes: map[string]any{"friendly_name": "Office Debug"}},
		{EntityID: "light.hall", State: "on", Attributes: map[string]any{"friendly_name": "Hall"}},
	}
	fake.entityRows = []map[string]any{
		{"entity_id": "light.office_main"},
		{"entity_id": "light.office_debug", "hidden_by": "user"},
		{"entity_id": "light.hall"},
	}
	return fake
}

func TestHAListEntities_DefaultExcludesHiddenAndAdvertisesCount(t *testing.T) {
	reg := visibilityFixture(t).registry(t)
	raw, err := reg.Execute(context.Background(), "ha_list_entities", `{"domain":"light"}`)
	if err != nil {
		t.Fatalf("ha_list_entities: %v", err)
	}
	var got haListEntitiesResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if got.Count != 2 || got.Total != 2 {
		t.Fatalf("count/total = %d/%d, want 2/2 (hidden excluded)", got.Count, got.Total)
	}
	if got.HiddenExcluded != 1 {
		t.Errorf("hidden_excluded = %d, want 1 (existence advertised, never silent)", got.HiddenExcluded)
	}
	if strings.Contains(raw, "office_debug") {
		t.Errorf("hidden entity leaked into the default view:\n%s", raw)
	}
}

func TestHAListEntities_IncludeHiddenSurfacesAndMarks(t *testing.T) {
	reg := visibilityFixture(t).registry(t)
	raw, err := reg.Execute(context.Background(), "ha_list_entities", `{"domain":"light","include_hidden":true}`)
	if err != nil {
		t.Fatalf("ha_list_entities: %v", err)
	}
	var got haListEntitiesResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Count != 3 || got.HiddenExcluded != 0 {
		t.Fatalf("count/hidden_excluded = %d/%d, want 3/0", got.Count, got.HiddenExcluded)
	}
	var hiddenMarked bool
	for _, it := range got.Items {
		if it.EntityID == "light.office_debug" {
			hiddenMarked = it.Hidden
		} else if it.Hidden {
			t.Errorf("visible entity %s marked hidden", it.EntityID)
		}
	}
	if !hiddenMarked {
		t.Error("hidden entity surfaced via include_hidden must be marked hidden, never silently blended in")
	}
}

func TestHASearchStates_DefaultExcludesHidden(t *testing.T) {
	reg := visibilityFixture(t).registry(t)
	// Default: hidden excluded, count advertised.
	raw, err := reg.Execute(context.Background(), "ha_search_states", `{"domain":"light","state":["on","off"]}`)
	if err != nil {
		t.Fatalf("ha_search_states: %v", err)
	}
	var got haSearchStatesResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if got.Count != 2 || got.HiddenExcluded != 1 {
		t.Fatalf("count/hidden_excluded = %d/%d, want 2/1", got.Count, got.HiddenExcluded)
	}
	if strings.Contains(raw, "office_debug") {
		t.Errorf("hidden entity leaked:\n%s", raw)
	}

	// include_hidden surfaces and marks it.
	raw2, err := reg.Execute(context.Background(), "ha_search_states", `{"domain":"light","state":["on","off"],"include_hidden":true}`)
	if err != nil {
		t.Fatalf("include_hidden: %v", err)
	}
	var got2 haSearchStatesResult
	if err := json.Unmarshal([]byte(raw2), &got2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got2.Count != 3 || got2.HiddenExcluded != 0 {
		t.Errorf("include_hidden count/excluded = %d/%d, want 3/0", got2.Count, got2.HiddenExcluded)
	}
}

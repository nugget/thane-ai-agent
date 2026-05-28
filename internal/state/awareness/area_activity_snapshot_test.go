package awareness

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// TestComputeAreaActivity_FloorContext covers the #1005 enhancement:
// the snapshot carries the area's resolved floor context.
func TestComputeAreaActivity_FloorContext(t *testing.T) {
	client := &fakeAreaClient{
		areas:  []homeassistant.Area{{AreaID: "office", Name: "Office", FloorID: "ground"}},
		floors: []homeassistant.FloorRegistryEntry{{FloorID: "ground", Name: "Ground Floor"}},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "light.office_lamp", AreaID: "office"},
		},
		states: []homeassistant.State{
			{EntityID: "light.office_lamp", State: "on"},
		},
	}

	out, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "office"}, testNow)
	if err != nil {
		t.Fatalf("ComputeAreaActivity: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	floor, ok := payload["floor"].(map[string]any)
	if !ok {
		t.Fatalf("expected floor context, got %#v", payload["floor"])
	}
	if floor["id"] != "ground" || floor["name"] != "Ground Floor" {
		t.Errorf("floor = %#v, want ground/Ground Floor", floor)
	}
}

// TestComputeAreaActivity_IncludeMetadata covers per-entity metadata
// projection: each bucketed entity carries the requested include block.
func TestComputeAreaActivity_IncludeMetadata(t *testing.T) {
	client := &fakeAreaClient{
		areas: []homeassistant.Area{{AreaID: "office", Name: "Office"}},
		entities: []homeassistant.EntityRegistryEntry{
			{
				EntityID:    "light.office_lamp",
				AreaID:      "office",
				Description: "Desk lamp",
				Platform:    "hue",
			},
		},
		states: []homeassistant.State{
			{EntityID: "light.office_lamp", State: "on"},
		},
	}

	req := AreaActivityRequest{
		Area:    "office",
		Include: homeassistant.EntityMetadataIncludes{Description: true},
	}
	out, err := ComputeAreaActivity(context.Background(), client, req, testNow)
	if err != nil {
		t.Fatalf("ComputeAreaActivity: %v", err)
	}
	var payload struct {
		Active []struct {
			Entity   string `json:"entity"`
			Metadata *struct {
				Description string `json:"description"`
			} `json:"metadata"`
		} `json:"active"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(payload.Active) != 1 {
		t.Fatalf("active bucket = %#v, want one entity\n%s", payload.Active, out)
	}
	if payload.Active[0].Metadata == nil {
		t.Fatalf("expected metadata on the active entity:\n%s", out)
	}
	if payload.Active[0].Metadata.Description != "Desk lamp" {
		t.Errorf("metadata.description = %q, want Desk lamp", payload.Active[0].Metadata.Description)
	}
}

// TestComputeAreaActivity_NoIncludeNoMetadata confirms the projection
// is opt-in: without include, entities carry no metadata block.
func TestComputeAreaActivity_NoIncludeNoMetadata(t *testing.T) {
	client := &fakeAreaClient{
		areas: []homeassistant.Area{{AreaID: "office", Name: "Office"}},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "light.office_lamp", AreaID: "office", Description: "Desk lamp"},
		},
		states: []homeassistant.State{
			{EntityID: "light.office_lamp", State: "on"},
		},
	}
	out, err := ComputeAreaActivity(context.Background(), client, AreaActivityRequest{Area: "office"}, testNow)
	if err != nil {
		t.Fatalf("ComputeAreaActivity: %v", err)
	}
	if got := out; jsonHasKey(t, got, "metadata") {
		t.Errorf("expected no metadata block without include:\n%s", got)
	}
}

func jsonHasKey(t *testing.T, raw, key string) bool {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, bucket := range []string{"anomalies", "active", "recent_changes", "ambient", "stable"} {
		list, _ := doc[bucket].([]any)
		for _, item := range list {
			if obj, ok := item.(map[string]any); ok {
				if _, has := obj[key]; has {
					return true
				}
			}
		}
	}
	return false
}

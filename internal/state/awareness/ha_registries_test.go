package awareness

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func TestRenderRegistriesCachesMetadataResolver(t *testing.T) {
	fr := &fakeRegistries{
		areas: []homeassistant.Area{{AreaID: "office", Name: "Office"}},
		entities: []homeassistant.EntityRegistryEntry{
			{EntityID: "sensor.one", AreaID: "office"},
			{EntityID: "sensor.two", AreaID: "office"},
		},
	}
	regs := newRenderRegistries(context.Background(), fr)
	include := &homeassistant.EntityMetadataIncludes{Area: true}

	for _, entityID := range []string{"sensor.one", "sensor.two"} {
		got := regs.entityMetadata(entityID, &homeassistant.State{EntityID: entityID}, include)
		if got == nil || got.Area == nil || got.Area.Name != "Office" {
			t.Fatalf("entityMetadata(%q) = %#v, want office area", entityID, got)
		}
	}

	if fr.entitiesCalls != 1 {
		t.Fatalf("entity registry calls = %d, want 1", fr.entitiesCalls)
	}
	if fr.areasCalls != 1 {
		t.Fatalf("area registry calls = %d, want 1", fr.areasCalls)
	}
	if fr.devicesCalls != 1 {
		t.Fatalf("device registry calls = %d, want 1", fr.devicesCalls)
	}
	if fr.labelsCalls != 0 {
		t.Fatalf("label registry calls = %d, want 0", fr.labelsCalls)
	}
}

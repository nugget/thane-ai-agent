package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func TestInferDomainFromDescription(t *testing.T) {
	tests := []struct {
		description string
		want        string
	}{
		{"office light", "light"},
		{"LED strip", "light"},
		{"ceiling lamp", "light"},
		{"kitchen fan", "fan"},
		{"exhaust fan", "fan"},
		{"front door lock", "lock"},
		{"garage door", "cover"},
		{"window blinds", "cover"},
		{"living room thermostat", "climate"},
		{"temperature sensor", "sensor"},
		{"random device", ""}, // No match
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got := inferDomainFromDescription(tt.description)
			if got != tt.want {
				t.Errorf("inferDomainFromDescription(%q) = %q, want %q", tt.description, got, tt.want)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"light.office_lamp", []string{"light", "office", "lamp"}},
		{"ap-hor-office", []string{"ap", "hor", "office"}},
		{"simple", []string{"simple"}},
		{"a b c", []string{}}, // Single chars filtered
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := tokenize(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTokenMatchScore(t *testing.T) {
	tests := []struct {
		name   string
		query  []string
		target []string
		minExp float64 // Minimum expected score
	}{
		{"exact match", []string{"office", "light"}, []string{"office", "light"}, 1.0},
		{"partial match", []string{"office"}, []string{"office", "lamp"}, 1.0},
		{"substring", []string{"off"}, []string{"office"}, 0.7},
		{"no match", []string{"bedroom"}, []string{"office", "light"}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenMatchScore(tt.query, tt.target)
			if got < tt.minExp {
				t.Errorf("tokenMatchScore(%v, %v) = %v, want >= %v", tt.query, tt.target, got, tt.minExp)
			}
		})
	}
}

func TestFuzzyMatchEntityInfos(t *testing.T) {
	entities := []homeassistant.EntityInfo{
		{EntityID: "light.office_lamp", FriendlyName: "Office Lamp"},
		{EntityID: "light.ap_hor_office_led", FriendlyName: "AP HOR Office LED"},
		{EntityID: "light.bedroom_ceiling", FriendlyName: "Bedroom Ceiling Light"},
	}

	tests := []struct {
		description string
		wantFirst   string // Expected first match entity_id
	}{
		{"office lamp", "light.office_lamp"},
		{"office LED", "light.ap_hor_office_led"},
		{"bedroom", "light.bedroom_ceiling"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			matches := fuzzyMatchEntityInfos(tt.description, entities)
			if len(matches) == 0 {
				t.Errorf("fuzzyMatchEntityInfos(%q) returned no matches", tt.description)
				return
			}
			if matches[0].EntityID != tt.wantFirst {
				t.Errorf("fuzzyMatchEntityInfos(%q) first match = %q, want %q",
					tt.description, matches[0].EntityID, tt.wantFirst)
			}
		})
	}
}

func TestFuzzyMatchEntityInfosWithMetadata(t *testing.T) {
	entities := []homeassistant.EntityInfo{{
		EntityID:     "sensor.t_123",
		FriendlyName: "Temperature",
		State:        "72",
	}}
	entry := &homeassistant.EntityRegistryEntry{
		EntityID:    "sensor.t_123",
		DeviceID:    "device_1",
		Labels:      []string{"label_environment"},
		Description: "Ambient office temperature",
	}
	bundle := &haEntityMetadataBundle{
		include: homeassistant.AllEntityMetadataIncludes(),
		entries: map[string]*homeassistant.EntityRegistryEntry{
			entry.EntityID: entry,
		},
		resolver: homeassistant.NewEntityMetadataResolver(
			[]homeassistant.Area{{AreaID: "office", Name: "Office"}},
			[]homeassistant.LabelRegistryEntry{{LabelID: "label_environment", Name: "Environment"}},
			[]homeassistant.DeviceRegistryEntry{{
				ID:         "device_1",
				NameByUser: "Office Climate Hub",
				AreaID:     "office",
			}},
		),
	}

	matches := fuzzyMatchEntityInfosWithMetadata("office climate hub", entities, bundle)
	if len(matches) == 0 {
		t.Fatal("expected metadata-backed match")
	}
	if matches[0].EntityID != "sensor.t_123" {
		t.Fatalf("first match = %q, want sensor.t_123", matches[0].EntityID)
	}
}

func TestFuzzyMatchEntityInfosWeightsSingleTokenMetadata(t *testing.T) {
	entities := []homeassistant.EntityInfo{
		{EntityID: "light.office_lamp", FriendlyName: "Office Lamp"},
		{EntityID: "sensor.t_123", FriendlyName: "Temperature"},
	}
	entry := &homeassistant.EntityRegistryEntry{
		EntityID: "sensor.t_123",
		AreaID:   "office",
	}
	bundle := &haEntityMetadataBundle{
		include: homeassistant.EntityMetadataIncludes{Area: true},
		entries: map[string]*homeassistant.EntityRegistryEntry{
			entry.EntityID: entry,
		},
		resolver: homeassistant.NewEntityMetadataResolver(
			[]homeassistant.Area{{AreaID: "office", Name: "Office"}},
			nil,
			nil,
		),
	}

	matches := fuzzyMatchEntityInfosWithMetadata("office", entities, bundle)
	if len(matches) == 0 {
		t.Fatal("expected direct entity match")
	}
	if matches[0].EntityID != "light.office_lamp" {
		t.Fatalf("first match = %q, want light.office_lamp", matches[0].EntityID)
	}
	for _, match := range matches {
		if match.EntityID == "sensor.t_123" {
			t.Fatalf("metadata-only single-token match should not pass threshold: %#v", matches)
		}
	}
}

func TestFindEntityAreaLookupDoesNotFetchLabels(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{{
		EntityID: "light.office_lamp",
		State:    "off",
		Attributes: map[string]any{
			"friendly_name": "Office Lamp",
		},
	}}
	fake.areas = []map[string]any{{
		"area_id": "office",
		"name":    "Office",
	}}
	fake.entityRows = []map[string]any{{
		"entity_id": "light.office_lamp",
		"area_id":   "office",
	}}

	reg := fake.registry(t)
	result, err := reg.Execute(context.Background(), "ha_find_entity", `{
		"description": "lamp",
		"area": "Office"
	}`)
	if err != nil {
		t.Fatalf("ha_find_entity: %v", err)
	}
	var got FindEntityResult
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result: %v\n%s", err, result)
	}
	if !got.Found || got.EntityID != "light.office_lamp" || got.AreaName != "Office" {
		t.Fatalf("result = %#v, want office lamp with area", got)
	}

	fake.mu.Lock()
	labelCalls := fake.wsCalls["config/label_registry/list"]
	fake.mu.Unlock()
	if labelCalls != 0 {
		t.Fatalf("label registry calls = %d, want 0", labelCalls)
	}
}

func TestToJSON(t *testing.T) {
	result := FindEntityResult{
		Found:    true,
		EntityID: "light.test",
	}
	got := toJSON(result)
	if got == "" || got == `{"error":"json encoding failed"}` {
		t.Errorf("toJSON failed unexpectedly: %s", got)
	}
}

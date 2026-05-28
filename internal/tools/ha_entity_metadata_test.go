package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func TestHAGetStateIncludesEntityMetadata(t *testing.T) {
	fake := newFakeHAServer(t)
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	fake.states = []homeassistant.State{{
		EntityID: "sensor.office_temperature",
		State:    "72.1",
		Attributes: map[string]any{
			"friendly_name":       "Office Temperature",
			"unit_of_measurement": "F",
			"device_class":        "temperature",
		},
		LastChanged: now.Add(-5 * time.Minute),
		LastUpdated: now.Add(-5 * time.Minute),
	}}
	fake.areas = []map[string]any{{
		"area_id":  "office",
		"name":     "Office",
		"floor_id": "building_a",
		"labels":   []any{"label_work"},
	}}
	fake.floors = []map[string]any{{
		"floor_id": "building_a",
		"name":     "Building A",
		"aliases":  []string{"main building"},
	}}
	fake.labels = []map[string]any{
		{"label_id": "label_env", "name": "Environment", "description": "Ambient signals"},
		{"label_id": "label_work", "name": "Work"},
		{"label_id": "label_device", "name": "Device Health"},
	}
	fake.devices = []map[string]any{{
		"id":           "device_1",
		"name_by_user": "Office Climate Hub",
		"manufacturer": "Acme",
		"model":        "Enviro",
		"area_id":      "office",
		"labels":       []any{"label_device"},
	}}
	fake.entityRows = []map[string]any{{
		"entity_id":       "sensor.office_temperature",
		"name":            "Temperature",
		"description":     "Ambient office temperature",
		"device_id":       "device_1",
		"labels":          []any{"label_env"},
		"platform":        "zwave_js",
		"device_class":    "temperature",
		"translation_key": "temperature",
		"has_entity_name": true,
		"hidden_by":       "user",
	}}

	reg := fake.registry(t)
	result, err := reg.Execute(context.Background(), "ha_get_state", `{
		"entity_id": "sensor.office_temperature",
		"include": {"all": true}
	}`)
	if err != nil {
		t.Fatalf("ha_get_state: %v", err)
	}

	var got struct {
		Entity   string `json:"entity"`
		Metadata struct {
			Description string `json:"description"`
			Area        struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Floor struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"floor"`
			} `json:"area"`
			Device struct {
				ID         string `json:"id"`
				NameByUser string `json:"name_by_user"`
			} `json:"device"`
			Visibility struct {
				Enabled        bool   `json:"enabled"`
				Visible        bool   `json:"visible"`
				DefaultContext bool   `json:"default_context"`
				ContextRole    string `json:"context_role"`
				HiddenBy       string `json:"hidden_by"`
			} `json:"visibility"`
			TranslationKey string `json:"translation_key"`
			HasEntityName  bool   `json:"has_entity_name"`
			Labels         []struct {
				ID      string   `json:"id"`
				Name    string   `json:"name"`
				Sources []string `json:"sources"`
			} `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result: %v\n%s", err, result)
	}
	if got.Entity != "sensor.office_temperature" {
		t.Fatalf("entity = %q, want sensor.office_temperature", got.Entity)
	}
	if got.Metadata.Description != "Ambient office temperature" {
		t.Errorf("description = %q, want registry description", got.Metadata.Description)
	}
	if got.Metadata.Area.ID != "office" || got.Metadata.Area.Name != "Office" {
		t.Errorf("area = %#v, want office", got.Metadata.Area)
	}
	if got.Metadata.Area.Floor.ID != "building_a" || got.Metadata.Area.Floor.Name != "Building A" {
		t.Errorf("floor = %#v, want Building A", got.Metadata.Area.Floor)
	}
	if got.Metadata.Device.ID != "device_1" || got.Metadata.Device.NameByUser != "Office Climate Hub" {
		t.Errorf("device = %#v, want device_1", got.Metadata.Device)
	}
	if !got.Metadata.Visibility.Enabled ||
		got.Metadata.Visibility.Visible ||
		got.Metadata.Visibility.DefaultContext ||
		got.Metadata.Visibility.ContextRole != "hidden" ||
		got.Metadata.Visibility.HiddenBy != "user" {
		t.Errorf("visibility = %#v, want enabled hidden-by-user", got.Metadata.Visibility)
	}
	if got.Metadata.TranslationKey != "temperature" || !got.Metadata.HasEntityName {
		t.Errorf("translation metadata = %q/%v, want temperature/true", got.Metadata.TranslationKey, got.Metadata.HasEntityName)
	}
	if len(got.Metadata.Labels) != 3 {
		t.Fatalf("labels = %#v, want 3", got.Metadata.Labels)
	}
	fake.mu.Lock()
	entityGetCalls := fake.wsCalls["config/entity_registry/get"]
	entityListCalls := fake.wsCalls["config/entity_registry/list"]
	fake.mu.Unlock()
	if entityGetCalls != 1 {
		t.Fatalf("entity registry get calls = %d, want 1", entityGetCalls)
	}
	if entityListCalls != 0 {
		t.Fatalf("entity registry list calls = %d, want 0", entityListCalls)
	}
}

func TestHAListEntitiesMetadataHydratesOnlyReturnedEntities(t *testing.T) {
	fake := newFakeHAServer(t)
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	fake.states = []homeassistant.State{
		{
			EntityID: "sensor.office_temperature",
			State:    "72.1",
			Attributes: map[string]any{
				"friendly_name": "Office Temperature",
				"device_class":  "temperature",
			},
			LastChanged: now.Add(-5 * time.Minute),
			LastUpdated: now.Add(-5 * time.Minute),
		},
		{
			EntityID: "sensor.office_humidity",
			State:    "45",
			Attributes: map[string]any{
				"friendly_name": "Office Humidity",
				"device_class":  "humidity",
			},
			LastChanged: now.Add(-4 * time.Minute),
			LastUpdated: now.Add(-4 * time.Minute),
		},
		{
			EntityID: "sensor.office_voltage",
			State:    "120",
			Attributes: map[string]any{
				"friendly_name": "Office Voltage",
				"device_class":  "voltage",
			},
			LastChanged: now.Add(-3 * time.Minute),
			LastUpdated: now.Add(-3 * time.Minute),
		},
		{
			EntityID: "light.office_lamp",
			State:    "off",
			Attributes: map[string]any{
				"friendly_name": "Office Lamp",
			},
			LastChanged: now.Add(-2 * time.Minute),
			LastUpdated: now.Add(-2 * time.Minute),
		},
	}
	fake.entityRows = []map[string]any{
		{
			"entity_id":    "sensor.office_temperature",
			"name":         "Temperature",
			"description":  "Ambient office temperature",
			"hidden_by":    "user",
			"platform":     "zwave_js",
			"disabled_by":  "",
			"device_class": "",
		},
		{
			"entity_id":   "sensor.office_humidity",
			"name":        "Humidity",
			"description": "Ambient office humidity",
			"platform":    "zwave_js",
		},
		{
			"entity_id":   "sensor.office_voltage",
			"name":        "Voltage",
			"description": "Outlet voltage",
			"platform":    "zwave_js",
		},
	}

	reg := fake.registry(t)
	result, err := reg.Execute(context.Background(), "ha_list_entities", `{
		"domain": "sensor",
		"limit": 2,
		"include": {"description": true, "visibility": true}
	}`)
	if err != nil {
		t.Fatalf("ha_list_entities: %v", err)
	}

	var got haListEntitiesResult
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result: %v\n%s", err, result)
	}
	if got.Domain != "sensor" || got.Count != 2 || got.Total != 3 || !got.Truncated {
		t.Fatalf("result summary = %#v, want two returned of three sensors", got)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}
	if got.Items[0].Metadata == nil {
		t.Fatal("first item metadata is nil")
	}
	if got.Items[0].Metadata.Description != "Ambient office temperature" {
		t.Fatalf("first description = %q, want registry description", got.Items[0].Metadata.Description)
	}
	if got.Items[0].Metadata.DeviceClass != "temperature" {
		t.Fatalf("first device_class = %q, want state device_class", got.Items[0].Metadata.DeviceClass)
	}
	if got.Items[0].Metadata.Visibility == nil || got.Items[0].Metadata.Visibility.ContextRole != "hidden" {
		t.Fatalf("first visibility = %#v, want hidden role", got.Items[0].Metadata.Visibility)
	}

	fake.mu.Lock()
	entityGetCalls := fake.wsCalls["config/entity_registry/get"]
	entityListCalls := fake.wsCalls["config/entity_registry/list"]
	fake.mu.Unlock()
	if entityGetCalls != 2 {
		t.Fatalf("entity registry get calls = %d, want 2", entityGetCalls)
	}
	if entityListCalls != 0 {
		t.Fatalf("entity registry list calls = %d, want 0", entityListCalls)
	}
}

func TestParseEntityMetadataIncludesArgRejectsBoolean(t *testing.T) {
	t.Parallel()

	if _, err := ParseEntityMetadataIncludesArg(true, "include"); err == nil {
		t.Fatal("ParseEntityMetadataIncludesArg(true) succeeded, want error")
	}

	got, err := ParseEntityMetadataIncludesArg(map[string]any{"all": true}, "include")
	if err != nil {
		t.Fatalf("ParseEntityMetadataIncludesArg({all:true}) error = %v", err)
	}
	if got != homeassistant.AllEntityMetadataIncludes() {
		t.Fatalf("ParseEntityMetadataIncludesArg({all:true}) = %#v, want all metadata", got)
	}
}

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
		"area_id": "office",
		"name":    "Office",
		"labels":  []any{"label_work"},
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
		"entity_id":    "sensor.office_temperature",
		"name":         "Temperature",
		"description":  "Ambient office temperature",
		"device_id":    "device_1",
		"labels":       []any{"label_env"},
		"platform":     "zwave_js",
		"device_class": "temperature",
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
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"area"`
			Device struct {
				ID         string `json:"id"`
				NameByUser string `json:"name_by_user"`
			} `json:"device"`
			Labels []struct {
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
	if got.Metadata.Device.ID != "device_1" || got.Metadata.Device.NameByUser != "Office Climate Hub" {
		t.Errorf("device = %#v, want device_1", got.Metadata.Device)
	}
	if len(got.Metadata.Labels) != 3 {
		t.Fatalf("labels = %#v, want 3", got.Metadata.Labels)
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

package awareness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

var testNow = time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC)

func TestFormatDefault_BasicSensor(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "sensor.office_temperature",
		State:       "72.4",
		LastChanged: testNow.Add(-45 * time.Second),
		Attributes: map[string]any{
			"friendly_name":       "Office Temperature",
			"unit_of_measurement": "°F",
		},
	}

	result := formatEntityContext(state, testNow)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("default format should be valid JSON: %v\nGot: %s", err, result)
	}
	if parsed["entity"] != "sensor.office_temperature" {
		t.Error("missing entity")
	}
	if parsed["name"] != "Office Temperature" {
		t.Error("missing friendly name")
	}
	if parsed["state"] != "72.4" {
		t.Error("missing state")
	}
	if parsed["unit"] != "°F" {
		t.Error("missing unit")
	}
	if parsed["since"] != "-45s" {
		t.Errorf("since = %v, want -45s", parsed["since"])
	}
}

func TestFormatWeather(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "weather.home",
		State:       "sunny",
		LastChanged: testNow.Add(-300 * time.Second),
		Attributes: map[string]any{
			"temperature":  72.4,
			"humidity":     45.0,
			"wind_speed":   8.3,
			"wind_bearing": 180.0,
			"pressure":     1013.0,
			"forecast": []any{
				map[string]any{
					"datetime":    testNow.Add(6 * time.Hour).Format(time.RFC3339),
					"condition":   "cloudy",
					"temperature": 78.0,
					"templow":     62.0,
				},
				map[string]any{
					"datetime":    testNow.Add(12 * time.Hour).Format(time.RFC3339),
					"condition":   "rainy",
					"temperature": 70.0,
					"templow":     58.0,
				},
			},
		},
	}

	result := formatEntityContext(state, testNow)

	// Should be valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("weather output should be valid JSON: %v\nGot: %s", err, result)
	}

	if parsed["entity"] != "weather.home" {
		t.Error("missing entity ID")
	}
	if parsed["state"] != "sunny" {
		t.Error("missing state")
	}
	if parsed["temperature"] != 72.4 {
		t.Errorf("temperature = %v, want 72.4", parsed["temperature"])
	}
	if parsed["humidity"] != 45.0 {
		t.Error("missing humidity")
	}

	// Check forecast is included with delta timestamps.
	forecast, ok := parsed["forecast"].([]any)
	if !ok || len(forecast) == 0 {
		t.Fatal("missing forecast array")
	}
	fc0, _ := forecast[0].(map[string]any)
	if fc0["condition"] != "cloudy" {
		t.Error("missing forecast condition")
	}
	if dt, ok := fc0["dt"].(string); !ok || !strings.HasPrefix(dt, "+") {
		t.Errorf("forecast dt should be positive delta, got %v", fc0["dt"])
	}
}

func TestFormatClimate(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "climate.thermostat",
		State:       "heat",
		LastChanged: testNow.Add(-600 * time.Second),
		Attributes: map[string]any{
			"current_temperature": 70.0,
			"temperature":         72.0,
			"current_humidity":    45.0,
			"hvac_mode":           "heat",
			"preset_mode":         "home",
		},
	}

	result := formatEntityContext(state, testNow)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("climate output should be valid JSON: %v\nGot: %s", err, result)
	}

	if parsed["entity"] != "climate.thermostat" {
		t.Error("missing entity")
	}
	if parsed["current_temp"] != 70.0 {
		t.Errorf("current_temp = %v, want 70", parsed["current_temp"])
	}
	if parsed["target_temp"] != 72.0 {
		t.Errorf("target_temp = %v, want 72", parsed["target_temp"])
	}
	if parsed["hvac_mode"] != "heat" {
		t.Error("missing hvac_mode")
	}
}

func TestFormatLight(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "light.office",
		State:       "on",
		LastChanged: testNow.Add(-30 * time.Second),
		Attributes: map[string]any{
			"brightness":        float64(191), // ~75%
			"color_temp_kelvin": float64(4000),
		},
	}

	result := formatEntityContext(state, testNow)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("light output should be valid JSON: %v\nGot: %s", err, result)
	}

	if parsed["state"] != "on" {
		t.Error("missing state")
	}
	// Brightness should be normalized to percentage.
	brightness, ok := parsed["brightness"].(float64)
	if !ok {
		t.Fatalf("brightness should be a number, got %T", parsed["brightness"])
	}
	if brightness < 74 || brightness > 76 {
		t.Errorf("brightness = %v, want ~75 (percent)", brightness)
	}
}

func TestFormatPerson(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "person.nugget",
		State:       "home",
		LastChanged: testNow.Add(-3600 * time.Second),
		Attributes: map[string]any{
			"friendly_name": "Nugget",
			"source":        "device_tracker.phone",
		},
	}

	result := formatEntityContext(state, testNow)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("person output should be valid JSON: %v\nGot: %s", err, result)
	}

	if parsed["entity"] != "person.nugget" {
		t.Error("missing entity")
	}
	if parsed["state"] != "home" {
		t.Error("missing state")
	}
	if parsed["since"] != "-3600s" {
		t.Errorf("since = %v, want -3600s", parsed["since"])
	}
}

func TestFormatDefault_NoAttributes(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "binary_sensor.door",
		State:       "off",
		LastChanged: testNow.Add(-10 * time.Second),
		Attributes:  map[string]any{},
	}

	result := formatEntityContext(state, testNow)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("default format should be valid JSON: %v\nGot: %s", err, result)
	}
	if parsed["entity"] != "binary_sensor.door" {
		t.Error("missing entity ID")
	}
	if parsed["state"] != "off" {
		t.Error("missing state")
	}
	if parsed["since"] != "-10s" {
		t.Errorf("since = %v, want -10s", parsed["since"])
	}
	// No name or unit when attributes are empty.
	if _, hasName := parsed["name"]; hasName {
		t.Error("name should be omitted when no friendly_name")
	}
	if _, hasUnit := parsed["unit"]; hasUnit {
		t.Error("unit should be omitted when no unit_of_measurement")
	}
}

func TestEntityDomain(t *testing.T) {
	tests := []struct {
		entityID string
		want     string
	}{
		{"weather.home", "weather"},
		{"sensor.temp", "sensor"},
		{"light.office", "light"},
		{"person.nugget", "person"},
		{"nodomain", ""},
	}
	for _, tt := range tests {
		got := entityDomain(tt.entityID)
		if got != tt.want {
			t.Errorf("entityDomain(%q) = %q, want %q", tt.entityID, got, tt.want)
		}
	}
}

func TestNormalizeBrightness(t *testing.T) {
	tests := []struct {
		input any
		want  int
	}{
		{float64(255), 100},
		{float64(128), 50},
		{float64(0), 0},
	}
	for _, tt := range tests {
		got := normalizeBrightness(tt.input)
		if got != tt.want {
			t.Errorf("normalizeBrightness(%v) = %v, want %d", tt.input, got, tt.want)
		}
	}

	if got := normalizeBrightness(nil); got != nil {
		t.Errorf("normalizeBrightness(nil) = %v, want nil", got)
	}
}

func TestFormatPersonPresence(t *testing.T) {
	result := FormatPersonPresence(
		"person.nugget", "Nugget", "home",
		testNow.Add(-120*time.Second),
		"office", "ap-office",
		testNow,
	)

	var parsed PersonPresenceContext
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("should be valid JSON: %v\nGot: %s", err, result)
	}

	if parsed.Entity != "person.nugget" {
		t.Error("missing entity")
	}
	if parsed.Name != "Nugget" {
		t.Error("missing name")
	}
	if parsed.State != "home" {
		t.Error("state should be home")
	}
	if parsed.Since != "-120s" {
		t.Errorf("since = %q, want -120s", parsed.Since)
	}
	if parsed.Room != "office" {
		t.Error("missing room")
	}
	if parsed.RoomSr != "ap-office" {
		t.Error("missing room source")
	}
}

func TestFormatPersonPresence_NotHome(t *testing.T) {
	result := FormatPersonPresence(
		"person.nugget", "Nugget", "not_home",
		testNow.Add(-3600*time.Second),
		"", "", testNow,
	)

	var parsed PersonPresenceContext
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("should be valid JSON: %v", err)
	}

	if parsed.State != "away" {
		t.Errorf("not_home should be normalized to 'away', got %q", parsed.State)
	}
	if parsed.Room != "" {
		t.Error("room should be empty when away")
	}
}

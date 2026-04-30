package awareness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
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

func TestFormatWeather_OpportunisticOptionalAttributes(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "weather.rooftop",
		State:       "partlycloudy",
		LastChanged: testNow.Add(-10 * time.Minute),
		Attributes: map[string]any{
			"temperature":          72.44,
			"temperature_unit":     "°F",
			"apparent_temperature": 75.2,
			"dew_point":            64.8,
			"humidity":             float64(74),
			"wind_speed":           12.34,
			"wind_speed_unit":      "mph",
			"wind_gust_speed":      22.2,
			"cloud_coverage":       63.4,
			"uv_index":             4.25,
			"ozone":                298.6,
			"precipitation":        0.127,
			"precipitation_unit":   "in",
		},
	}

	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("weather output should be valid JSON: %v\nGot: %s", err, result)
	}

	checks := map[string]any{
		"temperature":          "72.4 °F",
		"apparent_temperature": "75.2 °F",
		"dew_point":            "64.8 °F",
		"humidity":             float64(74),
		"wind_speed":           "12.3 mph",
		"wind_gust_speed":      "22.2 mph",
		"cloud_coverage":       float64(63),
		"uv_index":             4.3,
		"ozone":                float64(299),
		"precipitation":        "0.13 in",
	}
	for key, want := range checks {
		if got := parsed[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	for _, key := range []string{"temperature_unit", "wind_speed_unit", "wind_gust_speed_unit", "precipitation_unit"} {
		if got, has := parsed[key]; has {
			t.Errorf("%s should be folded into its measurement value, got %v", key, got)
		}
	}
	if _, has := parsed["station"]; has {
		t.Errorf("station should not be inferred for a generic weather entity, got %v", parsed["station"])
	}
}

func TestFormatWeather_NWSMETAR(t *testing.T) {
	lastChanged, err := time.Parse(time.RFC3339Nano, "2026-04-30T05:47:28.212451+00:00")
	if err != nil {
		t.Fatalf("parse lastChanged: %v", err)
	}
	lastUpdated, err := time.Parse(time.RFC3339Nano, "2026-04-30T06:01:50.171952+00:00")
	if err != nil {
		t.Fatalf("parse lastUpdated: %v", err)
	}
	now := time.Date(2026, 4, 30, 6, 2, 50, 0, time.UTC)
	state := &homeassistant.State{
		EntityID:    "weather.nws_msrh_klbx",
		State:       "fog",
		LastChanged: lastChanged,
		LastUpdated: lastUpdated,
		Attributes: map[string]any{
			"temperature":        21.0,
			"temperature_unit":   "°C",
			"humidity":           float64(100),
			"pressure":           1011.1,
			"pressure_unit":      "hPa",
			"wind_bearing":       0.0,
			"wind_speed":         0.0,
			"wind_speed_unit":    "km/h",
			"visibility":         8.05,
			"visibility_unit":    "km",
			"precipitation_unit": "mm",
			"attribution":        "Data from National Weather Service/NOAA",
			"friendly_name":      "KLBX (Closest METAR to MSR Houston. Angleton, TX) KLBX",
			"supported_features": float64(6),
		},
	}

	result := formatEntityContext(state, now)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("weather output should be valid JSON: %v\nGot: %s", err, result)
	}

	checks := map[string]any{
		"entity":       "weather.nws_msrh_klbx",
		"name":         "KLBX (Closest METAR to MSR Houston. Angleton, TX) KLBX",
		"station":      "KLBX",
		"source":       "National Weather Service/NOAA",
		"state":        "fog",
		"temperature":  "21 °C",
		"humidity":     float64(100),
		"pressure":     "1011.1 hPa",
		"wind_speed":   "0 km/h",
		"wind_bearing": float64(0),
		"visibility":   "8.05 km",
		"since":        "-921s",
		"updated":      "-59s",
	}
	for key, want := range checks {
		if got := parsed[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	if _, has := parsed["supported_features"]; has {
		t.Errorf("supported_features should stay out of model context, got %v", parsed["supported_features"])
	}
	for _, key := range []string{"temperature_unit", "pressure_unit", "wind_speed_unit", "visibility_unit", "precipitation_unit"} {
		if got, has := parsed[key]; has {
			t.Errorf("%s should be folded into its measurement value, got %v", key, got)
		}
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

func TestFormatSun(t *testing.T) {
	sunrise := testNow.Add(6 * time.Hour)
	sunset := testNow.Add(-2 * time.Hour)

	state := &homeassistant.State{
		EntityID:    "sun.sun",
		State:       "above_horizon",
		LastChanged: testNow.Add(-3 * time.Hour),
		Attributes: map[string]any{
			"next_rising":  sunrise.Format(time.RFC3339),
			"next_setting": sunset.Format(time.RFC3339),
			"elevation":    42.7654321,
		},
	}

	result := formatEntityContext(state, testNow)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("sun output should be valid JSON: %v\nGot: %s", err, result)
	}

	if parsed["entity"] != "sun.sun" {
		t.Error("missing entity")
	}
	if parsed["state"] != "above_horizon" {
		t.Error("missing state")
	}
	// Next rising should be a positive delta (future).
	if rise, ok := parsed["next_rising"].(string); !ok || !strings.HasPrefix(rise, "+") {
		t.Errorf("next_rising should be positive delta, got %v", parsed["next_rising"])
	}
	// Next setting should be a negative delta (past).
	if set, ok := parsed["next_setting"].(string); !ok || !strings.HasPrefix(set, "-") {
		t.Errorf("next_setting should be negative delta, got %v", parsed["next_setting"])
	}
	// Elevation should be rounded to 1 decimal.
	if elev, ok := parsed["elevation"].(float64); !ok || elev != 42.8 {
		t.Errorf("elevation = %v, want 42.8", parsed["elevation"])
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

func TestFormatDefault_BinarySensorDeviceClassTranslation(t *testing.T) {
	cases := []struct {
		name        string
		entityID    string
		state       string
		deviceClass string
		want        string
	}{
		{"door open", "binary_sensor.front_door", "on", "door", "open"},
		{"door closed", "binary_sensor.front_door", "off", "door", "closed"},
		{"garage_door open", "binary_sensor.garage", "on", "garage_door", "open"},
		{"window closed", "binary_sensor.bedroom_window", "off", "window", "closed"},
		{"motion detected", "binary_sensor.hallway", "on", "motion", "detected"},
		{"motion clear", "binary_sensor.hallway", "off", "motion", "clear"},
		{"smoke detected", "binary_sensor.kitchen_smoke", "on", "smoke", "detected"},
		{"moisture wet", "binary_sensor.basement_leak", "on", "moisture", "wet"},
		{"occupancy clear", "binary_sensor.office", "off", "occupancy", "clear"},
		{"occupancy occupied", "binary_sensor.office", "on", "occupancy", "occupied"},
		{"connectivity disconnected", "binary_sensor.router", "off", "connectivity", "disconnected"},
		{"battery low", "binary_sensor.remote_battery", "on", "battery", "low"},
		{"problem ok", "binary_sensor.printer", "off", "problem", "ok"},
		{"safety unsafe", "binary_sensor.pool", "on", "safety", "unsafe"},
		{"tamper clear", "binary_sensor.alarm", "off", "tamper", "clear"},
		{"tamper tampering", "binary_sensor.alarm", "on", "tamper", "tampering"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &homeassistant.State{
				EntityID:    tc.entityID,
				State:       tc.state,
				LastChanged: testNow.Add(-30 * time.Second),
				Attributes: map[string]any{
					"device_class": tc.deviceClass,
				},
			}
			result := formatEntityContext(state, testNow)
			var parsed map[string]any
			if err := json.Unmarshal([]byte(result), &parsed); err != nil {
				t.Fatalf("output should be valid JSON: %v\nGot: %s", err, result)
			}
			if got := parsed["state"]; got != tc.want {
				t.Errorf("state = %v, want %q", got, tc.want)
			}
			if got := parsed["device_class"]; got != tc.deviceClass {
				t.Errorf("device_class = %v, want %q", got, tc.deviceClass)
			}
		})
	}
}

func TestFormatDefault_BinarySensorPassthroughForUnknownDeviceClass(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "binary_sensor.unmapped",
		State:       "on",
		LastChanged: testNow.Add(-10 * time.Second),
		Attributes: map[string]any{
			"device_class": "totally_made_up",
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, result)
	}
	if parsed["state"] != "on" {
		t.Errorf("unmapped device_class should pass through state, got %v", parsed["state"])
	}
}

func TestFormatEntityContext_SentinelStatesYieldAvailabilityShape(t *testing.T) {
	// HA emits "unavailable" or "unknown" when integrations drop out.
	// These are intercepted before domain dispatch and rendered as a
	// structured availability payload so the model never sees a
	// sentinel string in the state field where a domain value belongs.
	for _, raw := range []string{"unavailable", "unknown"} {
		state := &homeassistant.State{
			EntityID:    "binary_sensor.front_door",
			State:       raw,
			LastChanged: testNow.Add(-12 * time.Minute),
			Attributes: map[string]any{
				"friendly_name": "Front Door",
				"device_class":  "door",
			},
		}
		result := formatEntityContext(state, testNow)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(result), &parsed); err != nil {
			t.Fatalf("invalid JSON for %s: %v", raw, err)
		}
		if parsed["available"] != false {
			t.Errorf("[%s] available = %v, want false", raw, parsed["available"])
		}
		if parsed["reason"] != raw {
			t.Errorf("[%s] reason = %v, want %q", raw, parsed["reason"], raw)
		}
		if _, hasState := parsed["state"]; hasState {
			t.Errorf("[%s] state field must be omitted (had %v)", raw, parsed["state"])
		}
		if parsed["unavailable_since"] != "-720s" {
			t.Errorf("[%s] unavailable_since = %v, want -720s", raw, parsed["unavailable_since"])
		}
		if parsed["device_class"] != "door" {
			t.Errorf("[%s] device_class = %v, want door", raw, parsed["device_class"])
		}
		if parsed["name"] != "Front Door" {
			t.Errorf("[%s] name = %v, want Front Door", raw, parsed["name"])
		}
	}
}

func TestFormatEntityContext_SentinelInterceptsAllDomains(t *testing.T) {
	// The sentinel interception must run before domain dispatch, so
	// even rich-formatter domains (weather, climate, light, cover)
	// emit the availability shape rather than their per-domain JSON.
	for _, entityID := range []string{"weather.home", "climate.thermostat", "light.kitchen", "cover.garage"} {
		state := &homeassistant.State{
			EntityID:    entityID,
			State:       "unavailable",
			LastChanged: testNow.Add(-30 * time.Second),
			Attributes:  map[string]any{},
		}
		result := formatEntityContext(state, testNow)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(result), &parsed); err != nil {
			t.Fatalf("[%s] invalid JSON: %v", entityID, err)
		}
		if parsed["available"] != false {
			t.Errorf("[%s] expected availability shape, got %v", entityID, parsed)
		}
	}
}

func TestFormatFetchError(t *testing.T) {
	result := formatFetchError("sensor.gone")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["entity"] != "sensor.gone" {
		t.Errorf("entity = %v, want sensor.gone", parsed["entity"])
	}
	if parsed["available"] != false {
		t.Errorf("available = %v, want false", parsed["available"])
	}
	if parsed["reason"] != "fetch_error" {
		t.Errorf("reason = %v, want fetch_error", parsed["reason"])
	}
}

func TestFormatDefault_SurfacesStateClass(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "sensor.utility_meter",
		State:       "8421.5",
		LastChanged: testNow.Add(-60 * time.Second),
		Attributes: map[string]any{
			"device_class":        "energy",
			"state_class":         "total_increasing",
			"unit_of_measurement": "kWh",
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["state_class"] != "total_increasing" {
		t.Errorf("state_class = %v, want total_increasing", parsed["state_class"])
	}
}

func TestFormatDefault_SurfacesAssumedState(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "switch.assumed",
		State:       "on",
		LastChanged: testNow,
		Attributes: map[string]any{
			"assumed_state": true,
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["assumed_state"] != true {
		t.Errorf("assumed_state = %v, want true", parsed["assumed_state"])
	}
}

func TestFormatDefault_OmitsAssumedStateWhenFalse(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "switch.real",
		State:       "on",
		LastChanged: testNow,
		Attributes:  map[string]any{},
	}
	result := formatEntityContext(state, testNow)
	if strings.Contains(result, "assumed_state") {
		t.Errorf("assumed_state must be omitted when not asserted true, got %s", result)
	}
}

func TestFormatClimate_SurfacesHVACAction(t *testing.T) {
	// hvac_mode is "heat" (the user setting); hvac_action is "idle"
	// (what the unit is actually doing right now). The model needs
	// both to answer "is the heat running?".
	state := &homeassistant.State{
		EntityID:    "climate.thermostat",
		State:       "heat",
		LastChanged: testNow.Add(-600 * time.Second),
		Attributes: map[string]any{
			"current_temperature": 70.0,
			"temperature":         72.0,
			"hvac_mode":           "heat",
			"hvac_action":         "idle",
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["hvac_action"] != "idle" {
		t.Errorf("hvac_action = %v, want idle", parsed["hvac_action"])
	}
	if parsed["hvac_mode"] != "heat" {
		t.Errorf("hvac_mode = %v, want heat", parsed["hvac_mode"])
	}
}

func TestFormatLight_OmitsStaleAttributesWhenOff(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "light.bedroom",
		State:       "off",
		LastChanged: testNow.Add(-300 * time.Second),
		Attributes: map[string]any{
			"brightness":        float64(255),
			"color_temp_kelvin": float64(4000),
			"rgb_color":         []any{255, 0, 0},
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["state"] != "off" {
		t.Errorf("state = %v, want off", parsed["state"])
	}
	for _, k := range []string{"brightness", "color_temp", "rgb_color"} {
		if _, has := parsed[k]; has {
			t.Errorf("%s must be omitted when light is off (was %v)", k, parsed[k])
		}
	}
}

func TestFormatLight_IncludesAttributesWhenOn(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "light.bedroom",
		State:       "on",
		LastChanged: testNow.Add(-30 * time.Second),
		Attributes: map[string]any{
			"brightness":        float64(255),
			"color_temp_kelvin": float64(4000),
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, has := parsed["brightness"]; !has {
		t.Error("brightness should be present when light is on")
	}
}

func TestFormatCover_GarageDoorWithPosition(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "cover.garage",
		State:       "open",
		LastChanged: testNow.Add(-120 * time.Second),
		Attributes: map[string]any{
			"friendly_name":         "Garage Door",
			"device_class":          "garage",
			"current_position":      float64(30),
			"current_tilt_position": float64(15),
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, result)
	}
	if parsed["entity"] != "cover.garage" {
		t.Error("missing entity")
	}
	if parsed["state"] != "open" {
		t.Errorf("state = %v, want open", parsed["state"])
	}
	if parsed["device_class"] != "garage" {
		t.Errorf("device_class = %v, want garage", parsed["device_class"])
	}
	if parsed["position"] != float64(30) {
		t.Errorf("position = %v, want 30", parsed["position"])
	}
	if parsed["tilt_position"] != float64(15) {
		t.Errorf("tilt_position = %v, want 15", parsed["tilt_position"])
	}
	if parsed["since"] != "-120s" {
		t.Errorf("since = %v, want -120s", parsed["since"])
	}
}

func TestFormatCover_OmitsMissingPosition(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "cover.simple",
		State:       "closed",
		LastChanged: testNow.Add(-5 * time.Second),
		Attributes: map[string]any{
			"device_class": "door",
		},
	}
	result := formatEntityContext(state, testNow)
	if strings.Contains(result, `"position"`) {
		t.Errorf("position should be omitted when missing, got %s", result)
	}
	if strings.Contains(result, `"tilt_position"`) {
		t.Errorf("tilt_position should be omitted when missing, got %s", result)
	}
}

func TestSemanticState_NumericFallthrough(t *testing.T) {
	// Numeric default-domain states still get device_class precision
	// rounding even after the binary_sensor translation path was added.
	if got := semanticState("sensor", "temperature", "72.456"); got != "72.5" {
		t.Errorf("semanticState numeric = %q, want 72.5", got)
	}
	if got := semanticState("binary_sensor", "", "on"); got != "on" {
		t.Errorf("binary_sensor without device_class should pass through, got %q", got)
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

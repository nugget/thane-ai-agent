package awareness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func TestFormatLock_BasicLocked(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "lock.front_door",
		State:       "locked",
		LastChanged: testNow.Add(-30 * time.Minute),
		Attributes: map[string]any{
			"friendly_name": "Front Door Lock",
			"battery_level": float64(72),
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, result)
	}
	if parsed["state"] != "locked" {
		t.Errorf("state = %v, want locked", parsed["state"])
	}
	if parsed["battery"] != float64(72) {
		t.Errorf("battery = %v, want 72", parsed["battery"])
	}
	if parsed["name"] != "Front Door Lock" {
		t.Errorf("name = %v, want Front Door Lock", parsed["name"])
	}
}

func TestFormatLock_JammedSurfacesDirectly(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "lock.back_door",
		State:       "jammed",
		LastChanged: testNow,
		Attributes:  map[string]any{},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["state"] != "jammed" {
		t.Errorf("state = %v, want jammed (no translation)", parsed["state"])
	}
}

func TestFormatMediaPlayer_PlayingIncludesMediaMetadata(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "media_player.living_room",
		State:       "playing",
		LastChanged: testNow.Add(-90 * time.Second),
		Attributes: map[string]any{
			"volume_level":     0.45,
			"media_title":      "Heroes",
			"media_artist":     "David Bowie",
			"media_album_name": "Heroes",
			"source":           "Spotify",
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["state"] != "playing" {
		t.Errorf("state = %v, want playing", parsed["state"])
	}
	if parsed["volume"] != float64(45) {
		t.Errorf("volume = %v, want 45 (normalized from 0.45)", parsed["volume"])
	}
	if parsed["media_title"] != "Heroes" {
		t.Errorf("media_title = %v, want Heroes", parsed["media_title"])
	}
	if parsed["media_artist"] != "David Bowie" {
		t.Errorf("media_artist = %v, want David Bowie", parsed["media_artist"])
	}
	if parsed["source"] != "Spotify" {
		t.Errorf("source = %v, want Spotify", parsed["source"])
	}
}

func TestFormatMediaPlayer_IdleOmitsMediaMetadata(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "media_player.living_room",
		State:       "idle",
		LastChanged: testNow.Add(-30 * time.Minute),
		Attributes: map[string]any{
			"volume_level": 0.45,
			// HA may still populate these stale fields; the formatter
			// must drop them when the player isn't actively presenting.
			"media_title":  "Heroes",
			"media_artist": "David Bowie",
		},
	}
	result := formatEntityContext(state, testNow)
	if strings.Contains(result, "media_title") {
		t.Errorf("media_title must be omitted when idle, got %s", result)
	}
	if strings.Contains(result, "media_artist") {
		t.Errorf("media_artist must be omitted when idle, got %s", result)
	}
}

func TestFormatMediaPlayer_TVSurfacesSeriesInfo(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "media_player.living_room_tv",
		State:       "playing",
		LastChanged: testNow.Add(-5 * time.Minute),
		Attributes: map[string]any{
			"volume_level":       0.3,
			"media_series_title": "The Bear",
			"media_season":       float64(3),
			"media_episode":      float64(7),
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["media_series"] != "The Bear" {
		t.Errorf("media_series = %v, want The Bear", parsed["media_series"])
	}
	if parsed["media_season"] != float64(3) {
		t.Errorf("media_season = %v, want 3", parsed["media_season"])
	}
}

func TestFormatFan_OnIncludesRunningAttributes(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "fan.bedroom",
		State:       "on",
		LastChanged: testNow.Add(-15 * time.Minute),
		Attributes: map[string]any{
			"percentage":  float64(60),
			"preset_mode": "sleep",
			"direction":   "forward",
			"oscillating": true,
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["percentage"] != float64(60) {
		t.Errorf("percentage = %v, want 60", parsed["percentage"])
	}
	if parsed["preset_mode"] != "sleep" {
		t.Errorf("preset_mode = %v, want sleep", parsed["preset_mode"])
	}
	if parsed["direction"] != "forward" {
		t.Errorf("direction = %v, want forward", parsed["direction"])
	}
	if parsed["oscillating"] != true {
		t.Errorf("oscillating = %v, want true", parsed["oscillating"])
	}
}

func TestFormatFan_OffOmitsRunningAttributes(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "fan.bedroom",
		State:       "off",
		LastChanged: testNow.Add(-1 * time.Hour),
		Attributes: map[string]any{
			"percentage":  float64(60),
			"preset_mode": "sleep",
		},
	}
	result := formatEntityContext(state, testNow)
	for _, k := range []string{"percentage", "preset_mode", "direction", "oscillating"} {
		if strings.Contains(result, `"`+k+`"`) {
			t.Errorf("%s must be omitted when fan is off, got %s", k, result)
		}
	}
}

func TestFormatVacuum_ErrorStateSurfaced(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "vacuum.roomba",
		State:       "error",
		LastChanged: testNow.Add(-10 * time.Minute),
		Attributes: map[string]any{
			"friendly_name": "Roomba",
			"battery_level": float64(55),
			"status":        "stuck on edge",
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["state"] != "error" {
		t.Errorf("state = %v, want error", parsed["state"])
	}
	if parsed["status"] != "stuck on edge" {
		t.Errorf("status = %v, want stuck on edge", parsed["status"])
	}
	if parsed["battery"] != float64(55) {
		t.Errorf("battery = %v, want 55", parsed["battery"])
	}
}

func TestFormatVacuum_DockedWithBattery(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "vacuum.roomba",
		State:       "docked",
		LastChanged: testNow.Add(-2 * time.Hour),
		Attributes: map[string]any{
			"battery_level": float64(100),
			"fan_speed":     "balanced",
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["state"] != "docked" {
		t.Errorf("state = %v, want docked", parsed["state"])
	}
	if parsed["fan_speed"] != "balanced" {
		t.Errorf("fan_speed = %v, want balanced", parsed["fan_speed"])
	}
}

func TestFormatUpdate_TranslatesOnOff(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"on", "update_available"},
		{"off", "up_to_date"},
	}
	for _, tc := range cases {
		state := &homeassistant.State{
			EntityID:    "update.thane",
			State:       tc.raw,
			LastChanged: testNow,
			Attributes: map[string]any{
				"installed_version": "1.2.3",
				"latest_version":    "1.2.4",
			},
		}
		result := formatEntityContext(state, testNow)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(result), &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if parsed["state"] != tc.want {
			t.Errorf("state for %q = %v, want %q", tc.raw, parsed["state"], tc.want)
		}
		if parsed["installed_version"] != "1.2.3" {
			t.Errorf("installed_version = %v, want 1.2.3", parsed["installed_version"])
		}
		if parsed["latest_version"] != "1.2.4" {
			t.Errorf("latest_version = %v, want 1.2.4", parsed["latest_version"])
		}
	}
}

func TestFormatUpdate_InProgress(t *testing.T) {
	state := &homeassistant.State{
		EntityID:    "update.thane",
		State:       "on",
		LastChanged: testNow,
		Attributes: map[string]any{
			"installed_version": "1.2.3",
			"latest_version":    "1.2.4",
			"in_progress":       true,
		},
	}
	result := formatEntityContext(state, testNow)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["in_progress"] != true {
		t.Errorf("in_progress = %v, want true", parsed["in_progress"])
	}
}

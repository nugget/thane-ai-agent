package awareness

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

// formatEntityContext returns a context line for an entity, choosing
// the format based on the entity domain. Rich domains (weather, climate,
// light) emit compact JSON with relevant attributes following #458
// conventions. Default domains use the original markdown line format.
func formatEntityContext(state *homeassistant.State, now time.Time) string {
	domain := entityDomain(state.EntityID)

	switch domain {
	case "weather":
		return formatWeather(state, now)
	case "climate":
		return formatClimate(state, now)
	case "light":
		return formatLight(state, now)
	case "person":
		return formatPerson(state, now)
	default:
		return formatDefault(state, now)
	}
}

// formatDefault produces the original markdown line format:
//
//   - **Office Temperature** (sensor.office_temperature): 72.4 °F (since -45s)
func formatDefault(state *homeassistant.State, now time.Time) string {
	displayName := state.EntityID
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		displayName = name
	}

	stateValue := state.State
	if unit, ok := state.Attributes["unit_of_measurement"].(string); ok && unit != "" {
		stateValue += " " + unit
	}

	since := FormatDeltaOnly(state.LastChanged, now)
	return fmt.Sprintf("- **%s** (%s): %s (since %s)", displayName, state.EntityID, stateValue, since)
}

// weatherContext is the JSON structure for weather entity context.
type weatherContext struct {
	Entity      string            `json:"entity"`
	State       string            `json:"state"`
	Temperature any               `json:"temperature,omitempty"`
	Humidity    any               `json:"humidity,omitempty"`
	WindSpeed   any               `json:"wind_speed,omitempty"`
	WindBearing any               `json:"wind_bearing,omitempty"`
	Pressure    any               `json:"pressure,omitempty"`
	Since       string            `json:"since"`
	Forecast    []weatherForecast `json:"forecast,omitempty"`
}

// weatherForecast is a single forecast entry.
type weatherForecast struct {
	Delta     string `json:"dt"`
	Condition string `json:"condition,omitempty"`
	TempHigh  any    `json:"high,omitempty"`
	TempLow   any    `json:"low,omitempty"`
}

func formatWeather(state *homeassistant.State, now time.Time) string {
	wc := weatherContext{
		Entity:      state.EntityID,
		State:       state.State,
		Temperature: state.Attributes["temperature"],
		Humidity:    state.Attributes["humidity"],
		WindSpeed:   state.Attributes["wind_speed"],
		WindBearing: state.Attributes["wind_bearing"],
		Pressure:    state.Attributes["pressure"],
		Since:       FormatDeltaOnly(state.LastChanged, now),
	}

	// Extract forecast entries (HA returns []any of map[string]any).
	if rawForecast, ok := state.Attributes["forecast"].([]any); ok {
		limit := 3
		if len(rawForecast) < limit {
			limit = len(rawForecast)
		}
		for i := 0; i < limit; i++ {
			entry, ok := rawForecast[i].(map[string]any)
			if !ok {
				continue
			}
			fc := weatherForecast{
				Condition: attrString(entry, "condition"),
				TempHigh:  entry["temperature"],
				TempLow:   entry["templow"],
			}
			// Delta-annotate forecast time if available.
			if dtStr, ok := entry["datetime"].(string); ok {
				if dt, err := time.Parse(time.RFC3339, dtStr); err == nil {
					fc.Delta = FormatDeltaOnly(dt, now)
				} else {
					fc.Delta = dtStr
				}
			}
			wc.Forecast = append(wc.Forecast, fc)
		}
	}

	return marshalCompact(wc)
}

// climateContext is the JSON structure for climate entity context.
type climateContext struct {
	Entity      string `json:"entity"`
	State       string `json:"state"`
	CurrentTemp any    `json:"current_temp,omitempty"`
	TargetTemp  any    `json:"target_temp,omitempty"`
	TargetHigh  any    `json:"target_high,omitempty"`
	TargetLow   any    `json:"target_low,omitempty"`
	Humidity    any    `json:"humidity,omitempty"`
	HVACMode    string `json:"hvac_mode,omitempty"`
	PresetMode  string `json:"preset_mode,omitempty"`
	Since       string `json:"since"`
}

func formatClimate(state *homeassistant.State, now time.Time) string {
	cc := climateContext{
		Entity:      state.EntityID,
		State:       state.State,
		CurrentTemp: state.Attributes["current_temperature"],
		TargetTemp:  state.Attributes["temperature"],
		TargetHigh:  state.Attributes["target_temp_high"],
		TargetLow:   state.Attributes["target_temp_low"],
		Humidity:    state.Attributes["current_humidity"],
		HVACMode:    attrString(state.Attributes, "hvac_mode"),
		PresetMode:  attrString(state.Attributes, "preset_mode"),
		Since:       FormatDeltaOnly(state.LastChanged, now),
	}
	return marshalCompact(cc)
}

// lightContext is the JSON structure for light entity context.
type lightContext struct {
	Entity     string `json:"entity"`
	State      string `json:"state"`
	Brightness any    `json:"brightness,omitempty"`
	ColorTemp  any    `json:"color_temp,omitempty"`
	RGBColor   any    `json:"rgb_color,omitempty"`
	Since      string `json:"since"`
}

func formatLight(state *homeassistant.State, now time.Time) string {
	lc := lightContext{
		Entity:     state.EntityID,
		State:      state.State,
		Brightness: normalizeBrightness(state.Attributes["brightness"]),
		ColorTemp:  state.Attributes["color_temp_kelvin"],
		RGBColor:   state.Attributes["rgb_color"],
		Since:      FormatDeltaOnly(state.LastChanged, now),
	}
	return marshalCompact(lc)
}

// normalizeBrightness converts HA's 0-255 brightness to a 0-100
// percentage for easier model reasoning.
func normalizeBrightness(v any) any {
	if v == nil {
		return nil
	}
	switch b := v.(type) {
	case float64:
		pct := int(b / 255.0 * 100.0)
		return pct
	case int:
		pct := int(float64(b) / 255.0 * 100.0)
		return pct
	default:
		return v
	}
}

// entityDomain extracts the domain from an entity ID
// (e.g., "weather" from "weather.home").
func entityDomain(entityID string) string {
	if idx := strings.IndexByte(entityID, '.'); idx > 0 {
		return entityID[:idx]
	}
	return ""
}

// attrString extracts a string attribute, returning "" if missing or
// not a string.
func attrString(attrs map[string]any, key string) string {
	if v, ok := attrs[key].(string); ok {
		return v
	}
	return ""
}

// personContext is the JSON structure for person entity context.
// This is the watchlist version; the PresenceTracker has richer data
// (room, device MACs) that the raw HA state doesn't carry.
type personContext struct {
	Entity string `json:"entity"`
	State  string `json:"state"`
	Since  string `json:"since"`
	Source string `json:"source,omitempty"`
}

func formatPerson(state *homeassistant.State, now time.Time) string {
	pc := personContext{
		Entity: state.EntityID,
		State:  state.State,
		Since:  FormatDeltaOnly(state.LastChanged, now),
		Source: attrString(state.Attributes, "source"),
	}
	return marshalCompact(pc)
}

// PersonPresenceContext is the JSON structure emitted by the
// PresenceTracker for each tracked person. Richer than personContext
// because the tracker has room data from UniFi AP associations.
type PersonPresenceContext struct {
	Entity string `json:"entity"`
	Name   string `json:"name"`
	State  string `json:"state"`
	Since  string `json:"since"`
	Room   string `json:"room,omitempty"`
	RoomSr string `json:"room_source,omitempty"`
}

// FormatPersonPresence formats a tracked person as compact JSON with
// delta-annotated timestamps. Exported for use by the PresenceTracker.
func FormatPersonPresence(entityID, name, state string, since time.Time, room, roomSource string, now time.Time) string {
	displayState := state
	if strings.EqualFold(state, "not_home") {
		displayState = "away"
	}
	pc := PersonPresenceContext{
		Entity: entityID,
		Name:   name,
		State:  displayState,
		Since:  FormatDeltaOnly(since, now),
		Room:   room,
		RoomSr: roomSource,
	}
	return marshalCompact(pc)
}

// marshalCompact returns compact JSON for a struct, falling back to
// fmt.Sprintf on marshal error (should never happen with these types).
func marshalCompact(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

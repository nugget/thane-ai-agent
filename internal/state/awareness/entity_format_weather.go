package awareness

import (
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// weatherContext is the JSON structure for weather entity context.
// Weather integrations vary widely. This shape preserves the standard
// HA weather attributes when present, plus common provider extensions
// such as visibility, dew point, gusts, precipitation, and METAR
// station identity.
type weatherContext struct {
	Entity        string            `json:"entity"`
	Name          string            `json:"name,omitempty"`
	Station       string            `json:"station,omitempty"`
	Source        string            `json:"source,omitempty"`
	State         string            `json:"state"`
	Temperature   any               `json:"temperature,omitempty"`
	ApparentTemp  any               `json:"apparent_temperature,omitempty"`
	DewPoint      any               `json:"dew_point,omitempty"`
	Humidity      any               `json:"humidity,omitempty"`
	WindSpeed     any               `json:"wind_speed,omitempty"`
	WindGustSpeed any               `json:"wind_gust_speed,omitempty"`
	WindBearing   any               `json:"wind_bearing,omitempty"`
	CloudCoverage any               `json:"cloud_coverage,omitempty"`
	UVIndex       any               `json:"uv_index,omitempty"`
	Ozone         any               `json:"ozone,omitempty"`
	Visibility    any               `json:"visibility,omitempty"`
	Pressure      any               `json:"pressure,omitempty"`
	Precipitation any               `json:"precipitation,omitempty"`
	Since         string            `json:"since"`
	Updated       string            `json:"updated,omitempty"`
	Forecast      []weatherForecast `json:"forecast,omitempty"`
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
		Entity:        state.EntityID,
		Name:          attrString(state.Attributes, "friendly_name"),
		Station:       weatherStationID(state),
		Source:        weatherSource(state.Attributes),
		State:         state.State,
		Temperature:   measuredAttr(state.Attributes, "temperature", "temperature_unit", 1),
		ApparentTemp:  measuredFirstAttr(state.Attributes, []string{"apparent_temperature", "apparent_temp", "feels_like"}, attrString(state.Attributes, "temperature_unit"), 1),
		DewPoint:      measuredFirstAttr(state.Attributes, []string{"dew_point", "dewpoint"}, attrString(state.Attributes, "temperature_unit"), 1),
		Humidity:      roundAttr(state.Attributes["humidity"], 0),
		WindSpeed:     measuredAttr(state.Attributes, "wind_speed", "wind_speed_unit", 1),
		WindGustSpeed: measuredFirstAttr(state.Attributes, []string{"wind_gust_speed", "wind_gust"}, firstNonEmpty(attrString(state.Attributes, "wind_gust_speed_unit"), attrString(state.Attributes, "wind_speed_unit")), 1),
		WindBearing:   roundAttr(state.Attributes["wind_bearing"], 0),
		CloudCoverage: roundAttr(firstAttr(state.Attributes, "cloud_coverage", "cloudcover", "clouds"), 0),
		UVIndex:       roundAttr(firstAttr(state.Attributes, "uv_index", "uv"), 1),
		Ozone:         roundAttr(state.Attributes["ozone"], 0),
		Visibility:    measuredAttr(state.Attributes, "visibility", "visibility_unit", 2),
		Pressure:      measuredAttr(state.Attributes, "pressure", "pressure_unit", 1),
		Precipitation: measuredAttr(state.Attributes, "precipitation", "precipitation_unit", 2),
		Since:         promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if !state.LastUpdated.IsZero() && state.LastUpdated.Sub(state.LastChanged) > time.Second {
		wc.Updated = promptfmt.FormatDeltaOnly(state.LastUpdated, now)
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
				TempHigh:  measuredFirstAttr(entry, []string{"temperature"}, attrString(state.Attributes, "temperature_unit"), 1),
				TempLow:   measuredFirstAttr(entry, []string{"templow"}, attrString(state.Attributes, "temperature_unit"), 1),
			}
			// Delta-annotate forecast time if available. HA may include
			// fractional seconds, so try RFC3339Nano first.
			if dtStr, ok := entry["datetime"].(string); ok {
				dt, err := time.Parse(time.RFC3339Nano, dtStr)
				if err != nil {
					dt, err = time.Parse(time.RFC3339, dtStr)
				}
				if err == nil {
					fc.Delta = promptfmt.FormatDeltaOnly(dt, now)
				} else {
					fc.Delta = dtStr
				}
			}
			wc.Forecast = append(wc.Forecast, fc)
		}
	}

	return promptfmt.MarshalCompact(wc)
}

func measuredAttr(attrs map[string]any, valueKey, unitKey string, places int) any {
	return measuredValue(attrs[valueKey], attrString(attrs, unitKey), places)
}

func measuredFirstAttr(attrs map[string]any, valueKeys []string, unit string, places int) any {
	return measuredValue(firstAttr(attrs, valueKeys...), unit, places)
}

func measuredValue(value any, unit string, places int) any {
	rounded := roundAttr(value, places)
	if rounded == nil {
		return nil
	}
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return rounded
	}
	return fmt.Sprintf("%v %s", rounded, unit)
}

func firstAttr(attrs map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := attrs[key]; ok && v != nil {
			return v
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func weatherSource(attrs map[string]any) string {
	source := strings.TrimSpace(attrString(attrs, "attribution"))
	return strings.TrimPrefix(source, "Data from ")
}

func weatherStationID(state *homeassistant.State) string {
	for _, key := range []string{"station_id", "station", "metar_station", "observation_station"} {
		if value := strings.TrimSpace(attrString(state.Attributes, key)); value != "" {
			return strings.ToUpper(value)
		}
	}
	if !looksLikeMETARWeather(state) {
		return ""
	}
	if idx := strings.LastIndexByte(state.EntityID, '_'); idx >= 0 && idx+1 < len(state.EntityID) {
		candidate := state.EntityID[idx+1:]
		if looksLikeStationID(candidate) {
			return strings.ToUpper(candidate)
		}
	}
	for _, field := range strings.Fields(attrString(state.Attributes, "friendly_name")) {
		candidate := strings.Trim(field, " \t\r\n()[]{}.,;:")
		if looksLikeStationID(candidate) {
			return strings.ToUpper(candidate)
		}
	}
	return ""
}

func looksLikeMETARWeather(state *homeassistant.State) bool {
	haystack := strings.ToLower(strings.Join([]string{
		state.EntityID,
		attrString(state.Attributes, "friendly_name"),
		attrString(state.Attributes, "attribution"),
	}, " "))
	return strings.Contains(haystack, "metar") ||
		strings.Contains(haystack, "national weather service") ||
		strings.Contains(haystack, "noaa") ||
		strings.HasPrefix(state.EntityID, "weather.nws_")
}

func looksLikeStationID(candidate string) bool {
	candidate = strings.ToUpper(strings.TrimSpace(candidate))
	switch candidate {
	case "METAR", "NOAA", "NWS", "ICAO", "ASOS", "AWOS", "AUTO", "DATA", "FROM":
		return false
	}
	if len(candidate) != 4 {
		return false
	}
	for _, r := range candidate {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

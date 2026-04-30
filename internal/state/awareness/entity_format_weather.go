package awareness

import (
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
	Entity            string            `json:"entity"`
	Name              string            `json:"name,omitempty"`
	Station           string            `json:"station,omitempty"`
	Source            string            `json:"source,omitempty"`
	State             string            `json:"state"`
	Temperature       any               `json:"temperature,omitempty"`
	TemperatureUnit   string            `json:"temperature_unit,omitempty"`
	ApparentTemp      any               `json:"apparent_temperature,omitempty"`
	DewPoint          any               `json:"dew_point,omitempty"`
	Humidity          any               `json:"humidity,omitempty"`
	WindSpeed         any               `json:"wind_speed,omitempty"`
	WindSpeedUnit     string            `json:"wind_speed_unit,omitempty"`
	WindGustSpeed     any               `json:"wind_gust_speed,omitempty"`
	WindGustSpeedUnit string            `json:"wind_gust_speed_unit,omitempty"`
	WindBearing       any               `json:"wind_bearing,omitempty"`
	CloudCoverage     any               `json:"cloud_coverage,omitempty"`
	UVIndex           any               `json:"uv_index,omitempty"`
	Ozone             any               `json:"ozone,omitempty"`
	Visibility        any               `json:"visibility,omitempty"`
	VisibilityUnit    string            `json:"visibility_unit,omitempty"`
	Pressure          any               `json:"pressure,omitempty"`
	PressureUnit      string            `json:"pressure_unit,omitempty"`
	Precipitation     any               `json:"precipitation,omitempty"`
	PrecipitationUnit string            `json:"precipitation_unit,omitempty"`
	Since             string            `json:"since"`
	Updated           string            `json:"updated,omitempty"`
	Forecast          []weatherForecast `json:"forecast,omitempty"`
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
		Entity:            state.EntityID,
		Name:              attrString(state.Attributes, "friendly_name"),
		Station:           weatherStationID(state),
		Source:            weatherSource(state.Attributes),
		State:             state.State,
		Temperature:       roundAttr(state.Attributes["temperature"], 1),
		TemperatureUnit:   attrString(state.Attributes, "temperature_unit"),
		ApparentTemp:      roundAttr(firstAttr(state.Attributes, "apparent_temperature", "apparent_temp", "feels_like"), 1),
		DewPoint:          roundAttr(firstAttr(state.Attributes, "dew_point", "dewpoint"), 1),
		Humidity:          roundAttr(state.Attributes["humidity"], 0),
		WindSpeed:         roundAttr(state.Attributes["wind_speed"], 1),
		WindSpeedUnit:     attrString(state.Attributes, "wind_speed_unit"),
		WindGustSpeed:     roundAttr(firstAttr(state.Attributes, "wind_gust_speed", "wind_gust"), 1),
		WindBearing:       roundAttr(state.Attributes["wind_bearing"], 0),
		CloudCoverage:     roundAttr(firstAttr(state.Attributes, "cloud_coverage", "cloudcover", "clouds"), 0),
		UVIndex:           roundAttr(firstAttr(state.Attributes, "uv_index", "uv"), 1),
		Ozone:             roundAttr(state.Attributes["ozone"], 0),
		Visibility:        roundAttr(state.Attributes["visibility"], 2),
		VisibilityUnit:    attrString(state.Attributes, "visibility_unit"),
		Pressure:          roundAttr(state.Attributes["pressure"], 1),
		PressureUnit:      attrString(state.Attributes, "pressure_unit"),
		Precipitation:     roundAttr(state.Attributes["precipitation"], 2),
		PrecipitationUnit: attrString(state.Attributes, "precipitation_unit"),
		Since:             promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if wc.WindGustSpeed != nil {
		wc.WindGustSpeedUnit = firstNonEmpty(attrString(state.Attributes, "wind_gust_speed_unit"), wc.WindSpeedUnit)
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
				TempHigh:  roundAttr(entry["temperature"], 1),
				TempLow:   roundAttr(entry["templow"], 1),
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
	for _, field := range strings.Fields(attrString(state.Attributes, "friendly_name")) {
		candidate := strings.Trim(field, " \t\r\n()[]{}.,;:")
		if looksLikeStationID(candidate) {
			return strings.ToUpper(candidate)
		}
	}
	if idx := strings.LastIndexByte(state.EntityID, '_'); idx >= 0 && idx+1 < len(state.EntityID) {
		candidate := state.EntityID[idx+1:]
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
	if len(candidate) < 3 || len(candidate) > 5 {
		return false
	}
	for _, r := range candidate {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

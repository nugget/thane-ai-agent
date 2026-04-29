package awareness

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// formatEntityContext returns a context line for an entity, choosing
// the format based on the entity domain. Rich domains (weather, climate,
// light) emit compact JSON with relevant attributes following #458
// conventions. Default domains use the original markdown line format.
//
// Sentinel states (unavailable, unknown) are intercepted before domain
// dispatch and rendered as a structured availability payload so the
// model reads available:false instead of inferring from a magic state
// string.
func formatEntityContext(state *homeassistant.State, now time.Time) string {
	if isSentinelState(state.State) {
		return formatUnavailable(state, now)
	}

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
	case "sun":
		return formatSun(state, now)
	case "cover":
		return formatCover(state, now)
	default:
		return formatDefault(state, now)
	}
}

// isSentinelState reports whether the given state is one of the Home
// Assistant sentinel values that indicate the entity is not currently
// reporting a real state.
func isSentinelState(state string) bool {
	return state == "unavailable" || state == "unknown"
}

// unavailableContext is the JSON structure for entities in a sentinel
// state. The shape deliberately omits any "state" field so the model
// cannot misread a sentinel string as a domain value (e.g. "unknown"
// being interpreted as a binary_sensor reading). Identity fields
// (name, device_class) are preserved so the model still knows what
// went offline.
type unavailableContext struct {
	Entity           string `json:"entity"`
	Name             string `json:"name,omitempty"`
	Available        bool   `json:"available"`
	Reason           string `json:"reason"`
	UnavailableSince string `json:"unavailable_since,omitempty"`
	DeviceClass      string `json:"device_class,omitempty"`
}

func formatUnavailable(state *homeassistant.State, now time.Time) string {
	uc := unavailableContext{
		Entity:      state.EntityID,
		Available:   false,
		Reason:      state.State, // "unavailable" or "unknown"
		DeviceClass: attrString(state.Attributes, "device_class"),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		uc.Name = name
	}
	if !state.LastChanged.IsZero() {
		uc.UnavailableSince = promptfmt.FormatDeltaOnly(state.LastChanged, now)
	}
	return promptfmt.MarshalCompact(uc)
}

// formatFetchError produces the structured availability payload for an
// entity whose state could not be fetched at all. Mirrors the shape of
// formatUnavailable so the model sees a consistent schema regardless
// of whether HA returned a sentinel state or the request itself failed.
func formatFetchError(entityID string) string {
	return promptfmt.MarshalCompact(unavailableContext{
		Entity:    entityID,
		Available: false,
		Reason:    "fetch_error",
	})
}

// defaultContext is the JSON structure for entities without a
// domain-specific formatter.
type defaultContext struct {
	Entity      string `json:"entity"`
	Name        string `json:"name,omitempty"`
	State       string `json:"state"`
	Unit        string `json:"unit,omitempty"`
	DeviceClass string `json:"device_class,omitempty"`
	Since       string `json:"since"`
	Updated     string `json:"updated,omitempty"` // only when differs from since
}

// formatDefault produces compact JSON for any entity type. Includes
// device_class when available and last_updated when it differs from
// last_changed (indicating attribute-only updates). Numeric state
// values are rounded based on device_class. Binary sensor states
// (on/off) are translated to device_class-specific semantic labels
// (door → open/closed, motion → detected/clear, etc.) so the model
// reads the meaning rather than inferring it from the on/off encoding.
func formatDefault(state *homeassistant.State, now time.Time) string {
	deviceClass := attrString(state.Attributes, "device_class")
	domain := entityDomain(state.EntityID)
	dc := defaultContext{
		Entity:      state.EntityID,
		State:       semanticState(domain, deviceClass, state.State),
		Unit:        attrString(state.Attributes, "unit_of_measurement"),
		DeviceClass: deviceClass,
		Since:       promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		dc.Name = name
	}
	// Include last_updated when it meaningfully differs from
	// last_changed (attribute-only updates vs state changes).
	if !state.LastUpdated.IsZero() && state.LastUpdated.Sub(state.LastChanged) > time.Second {
		dc.Updated = promptfmt.FormatDeltaOnly(state.LastUpdated, now)
	}
	return promptfmt.MarshalCompact(dc)
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
		Temperature: roundAttr(state.Attributes["temperature"], 1),
		Humidity:    roundAttr(state.Attributes["humidity"], 0),
		WindSpeed:   roundAttr(state.Attributes["wind_speed"], 1),
		WindBearing: roundAttr(state.Attributes["wind_bearing"], 0),
		Pressure:    roundAttr(state.Attributes["pressure"], 0),
		Since:       promptfmt.FormatDeltaOnly(state.LastChanged, now),
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
		CurrentTemp: roundAttr(state.Attributes["current_temperature"], 1),
		TargetTemp:  roundAttr(state.Attributes["temperature"], 1),
		TargetHigh:  roundAttr(state.Attributes["target_temp_high"], 1),
		TargetLow:   roundAttr(state.Attributes["target_temp_low"], 1),
		Humidity:    roundAttr(state.Attributes["current_humidity"], 0),
		HVACMode:    attrString(state.Attributes, "hvac_mode"),
		PresetMode:  attrString(state.Attributes, "preset_mode"),
		Since:       promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	return promptfmt.MarshalCompact(cc)
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
		Since:      promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	return promptfmt.MarshalCompact(lc)
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
		Since:  promptfmt.FormatDeltaOnly(state.LastChanged, now),
		Source: attrString(state.Attributes, "source"),
	}
	return promptfmt.MarshalCompact(pc)
}

// sunContext is the JSON structure for the sun.sun entity.
// Provides above/below horizon state with delta-annotated next
// sunrise and sunset times — critical for a home agent's awareness
// of lighting, security, and automation context.
type sunContext struct {
	Entity    string `json:"entity"`
	State     string `json:"state"` // above_horizon or below_horizon
	NextRise  string `json:"next_rising,omitempty"`
	NextSet   string `json:"next_setting,omitempty"`
	Elevation any    `json:"elevation,omitempty"`
	Since     string `json:"since"`
}

func formatSun(state *homeassistant.State, now time.Time) string {
	sc := sunContext{
		Entity:    state.EntityID,
		State:     state.State,
		Elevation: roundAttr(state.Attributes["elevation"], 1),
		Since:     promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}

	// Delta-annotate next rising/setting times.
	if rising, ok := state.Attributes["next_rising"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, rising); err == nil {
			sc.NextRise = promptfmt.FormatDeltaOnly(t, now)
		} else if t, err := time.Parse(time.RFC3339, rising); err == nil {
			sc.NextRise = promptfmt.FormatDeltaOnly(t, now)
		}
	}
	if setting, ok := state.Attributes["next_setting"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, setting); err == nil {
			sc.NextSet = promptfmt.FormatDeltaOnly(t, now)
		} else if t, err := time.Parse(time.RFC3339, setting); err == nil {
			sc.NextSet = promptfmt.FormatDeltaOnly(t, now)
		}
	}

	return promptfmt.MarshalCompact(sc)
}

// coverContext is the JSON structure for cover entity context.
// Cover state is already semantic (open/closed/opening/closing/stopped)
// but current_position carries information that the bare state hides:
// a blind at "open" with position 30 is meaningfully different from
// fully open. Tilt position is included for venetian-style covers.
type coverContext struct {
	Entity      string `json:"entity"`
	Name        string `json:"name,omitempty"`
	State       string `json:"state"`
	DeviceClass string `json:"device_class,omitempty"`
	Position    any    `json:"position,omitempty"`
	Tilt        any    `json:"tilt_position,omitempty"`
	Since       string `json:"since"`
}

func formatCover(state *homeassistant.State, now time.Time) string {
	cc := coverContext{
		Entity:      state.EntityID,
		State:       state.State,
		DeviceClass: attrString(state.Attributes, "device_class"),
		Position:    roundAttr(state.Attributes["current_position"], 0),
		Tilt:        roundAttr(state.Attributes["current_tilt_position"], 0),
		Since:       promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		cc.Name = name
	}
	return promptfmt.MarshalCompact(cc)
}

// binarySensorStateLabels maps a binary_sensor device_class to its
// semantic [off, on] labels. Tracks the canonical Home Assistant
// device_class catalog. Translation collapses the on/off encoding into
// the actual fact about the world, so the model does not need to know
// that device_class:door + state:on means "door is open".
var binarySensorStateLabels = map[string][2]string{
	"battery":          {"normal", "low"},
	"battery_charging": {"not_charging", "charging"},
	"carbon_monoxide":  {"clear", "detected"},
	"cold":             {"normal", "cold"},
	"connectivity":     {"disconnected", "connected"},
	"door":             {"closed", "open"},
	"garage_door":      {"closed", "open"},
	"gas":              {"clear", "detected"},
	"heat":             {"normal", "hot"},
	"light":            {"no_light", "light_detected"},
	"lock":             {"locked", "unlocked"},
	"moisture":         {"dry", "wet"},
	"motion":           {"clear", "detected"},
	"moving":           {"stopped", "moving"},
	"occupancy":        {"clear", "detected"},
	"opening":          {"closed", "open"},
	"plug":             {"unplugged", "plugged_in"},
	"power":            {"no_power", "powered"},
	"presence":         {"away", "home"},
	"problem":          {"ok", "problem"},
	"running":          {"not_running", "running"},
	"safety":           {"safe", "unsafe"},
	"smoke":            {"clear", "detected"},
	"sound":            {"clear", "detected"},
	"tamper":           {"ok", "tampering"},
	"update":           {"up_to_date", "update_available"},
	"vibration":        {"clear", "detected"},
	"window":           {"closed", "open"},
}

// semanticState returns a model-friendly state label for the given
// (domain, device_class, state) tuple. For binary_sensors with a known
// device_class, on/off is translated to its semantic pair (e.g. door
// on/off → open/closed). Numeric default-domain states are rounded by
// device_class. All other inputs pass through unchanged so unavailable,
// unknown, and unmapped values are preserved.
func semanticState(domain, deviceClass, state string) string {
	if domain == "binary_sensor" && deviceClass != "" {
		if labels, ok := binarySensorStateLabels[deviceClass]; ok {
			switch state {
			case "off":
				return labels[0]
			case "on":
				return labels[1]
			}
		}
	}
	return roundState(state, deviceClass)
}

// roundState rounds a numeric state string to appropriate precision
// based on device_class. Non-numeric states pass through unchanged.
func roundState(state, deviceClass string) string {
	f, err := strconv.ParseFloat(state, 64)
	if err != nil {
		return state // non-numeric, pass through
	}

	places := statePrecision(deviceClass)
	return roundFloat(f, places)
}

func statePrecision(deviceClass string) int {
	switch deviceClass {
	case "temperature":
		return 1
	case "humidity", "battery":
		return 0
	case "power", "energy", "voltage", "current":
		return 1
	default:
		return 2
	}
}

// roundFloat formats a float to the given decimal places, stripping
// trailing zeros for cleanliness.
func roundFloat(f float64, places int) string {
	mult := math.Pow10(places)
	rounded := math.Round(f*mult) / mult
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

// roundAttr rounds a numeric attribute value (any type) to the given
// decimal places. Returns nil for nil input. Non-numeric values pass
// through unchanged.
func roundAttr(v any, places int) any {
	if v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		mult := math.Pow10(places)
		return math.Round(n*mult) / mult
	case int:
		return n
	default:
		return v
	}
}

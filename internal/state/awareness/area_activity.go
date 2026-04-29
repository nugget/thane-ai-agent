package awareness

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// AreaActivityClient is the slice of Home Assistant calls the
// area_activity tool needs. Defined as an interface so tests can
// supply a fake; the concrete homeassistant.Client implements all
// methods out of the box.
type AreaActivityClient interface {
	HARegistryClient
	GetAreas(ctx context.Context) ([]homeassistant.Area, error)
	GetLogbookEvents(ctx context.Context, startTime, endTime time.Time, entityIDs []string) ([]homeassistant.LogbookEntry, error)
}

// AreaActivityRequest describes one invocation of the tool.
type AreaActivityRequest struct {
	Area              string // name or area_id (case-insensitive on name)
	LookbackSeconds   int    // <= 0 falls back to default
	IncludeDiagnostic bool   // include diagnostic/config category entities
	MaxStable         int    // cap on the stable bucket; <= 0 uses default
}

const (
	areaActivityDefaultLookback   = 3600
	areaActivityDefaultMaxStable  = 5
	areaActivityMaxRecentChanges  = 10
	areaActivityMaxTimelineEvents = 20
)

// alarmSecurityClasses are the binary_sensor device_classes whose "on"
// state is genuinely an alarm — surfaced in anomalies and allowed to
// appear in the cross-entity timeline even when other numeric/discrete
// transitions would be filtered as noise.
var alarmSecurityClasses = map[string]bool{
	"smoke":           true,
	"carbon_monoxide": true,
	"gas":             true,
	"moisture":        true,
	"safety":          true,
	"tamper":          true,
}

// ambientNumericClasses are device_classes for numeric sensors whose
// current value is meaningful baseline context for "what's it like in
// this area" rather than event-driven activity.
var ambientNumericClasses = map[string]bool{
	"temperature":          true,
	"humidity":             true,
	"illuminance":          true,
	"pressure":             true,
	"atmospheric_pressure": true,
	"carbon_dioxide":       true,
	"pm25":                 true,
	"pm10":                 true,
}

// ComputeAreaActivity resolves the area, gathers entities + states +
// logbook events, buckets them by relevance, and returns a single
// JSON object describing the area for model consumption. Caps and
// orderings follow the model-facing-context conventions: most
// actionable items first, deterministic sort, and bounded output
// with explicit *_truncated_count fields whenever a bucket or the
// timeline gets capped.
func ComputeAreaActivity(ctx context.Context, client AreaActivityClient, req AreaActivityRequest, now time.Time) (string, error) {
	if client == nil {
		return "", fmt.Errorf("area_activity: client is required")
	}
	if strings.TrimSpace(req.Area) == "" {
		return "", fmt.Errorf("area_activity: area is required")
	}

	lookback := req.LookbackSeconds
	if lookback <= 0 {
		lookback = areaActivityDefaultLookback
	}
	maxStable := req.MaxStable
	if maxStable <= 0 {
		maxStable = areaActivityDefaultMaxStable
	}

	areas, err := client.GetAreas(ctx)
	if err != nil {
		return "", fmt.Errorf("area_activity: get areas: %w", err)
	}
	area, ok := resolveArea(areas, req.Area)
	if !ok {
		return "", fmt.Errorf("area_activity: no area matched %q", req.Area)
	}

	entities, err := client.GetEntityRegistry(ctx)
	if err != nil {
		return "", fmt.Errorf("area_activity: get entity registry: %w", err)
	}
	devices, err := client.GetDeviceRegistry(ctx)
	if err != nil {
		return "", fmt.Errorf("area_activity: get device registry: %w", err)
	}
	allStates, err := client.GetStates(ctx)
	if err != nil {
		return "", fmt.Errorf("area_activity: get states: %w", err)
	}

	deviceByID := indexDevices(devices)
	statesByID := indexStates(allStates)

	members, totalInArea := selectAreaMembers(entities, deviceByID, area.AreaID, req.IncludeDiagnostic)
	if len(members) == 0 {
		return promptfmt.MarshalCompact(emptyAreaPayload(area, lookback, totalInArea)), nil
	}

	cutoff := now.Add(-time.Duration(lookback) * time.Second)
	classifier := newAreaClassifier(members, cutoff)
	for _, m := range members {
		state := statesByID[m.entry.EntityID]
		if state == nil {
			classifier.placeMissing(m, now)
			continue
		}
		classifier.classify(m, state, now)
	}

	anomalies := classifier.anomalies
	active := classifier.active
	recent := classifier.recentChanges
	recentTruncated := 0
	if len(recent) > areaActivityMaxRecentChanges {
		recentTruncated = len(recent) - areaActivityMaxRecentChanges
		recent = recent[:areaActivityMaxRecentChanges]
	}
	ambient := classifier.ambient
	stable := classifier.stable
	stableTruncated := 0
	if len(stable) > maxStable {
		stableTruncated = len(stable) - maxStable
		stable = stable[:maxStable]
	}

	timeline, timelineTruncated, err := buildAreaTimeline(ctx, client, members, statesByID, cutoff, now)
	if err != nil {
		return "", fmt.Errorf("area_activity: build timeline: %w", err)
	}

	payload := map[string]any{
		"area":           area.Name,
		"area_id":        area.AreaID,
		"lookback":       fmt.Sprintf("-%ds", lookback),
		"entity_count":   len(members),
		"filtered_count": totalInArea - len(members),
		"anomalies":      anomalies,
		"active":         active,
		"recent_changes": recent,
		"ambient":        ambient,
		"stable":         stable,
	}
	if recentTruncated > 0 {
		payload["recent_changes_truncated_count"] = recentTruncated
	}
	if stableTruncated > 0 {
		payload["stable_truncated_count"] = stableTruncated
	}
	if len(timeline) > 0 {
		payload["timeline"] = timeline
	}
	if timelineTruncated > 0 {
		payload["timeline_truncated_count"] = timelineTruncated
	}

	return promptfmt.MarshalCompact(payload), nil
}

// resolveArea matches either the area_id slug or a case-insensitive
// area name. The name match is exact (no fuzziness) so the model
// gets predictable behavior — if the model wants to be loose, it
// can call list_areas first.
func resolveArea(areas []homeassistant.Area, query string) (homeassistant.Area, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return homeassistant.Area{}, false
	}
	qLower := strings.ToLower(q)
	for _, a := range areas {
		if a.AreaID == q {
			return a, true
		}
	}
	for _, a := range areas {
		if strings.EqualFold(a.Name, q) {
			return a, true
		}
		for _, alias := range a.Aliases {
			if strings.EqualFold(alias, q) {
				return a, true
			}
		}
		if strings.EqualFold(a.AreaID, qLower) {
			return a, true
		}
	}
	return homeassistant.Area{}, false
}

// areaMember pairs an entity registry entry with the resolved
// device row (when present) so downstream classification can read
// device_class from the registry without re-joining.
type areaMember struct {
	entry  homeassistant.EntityRegistryEntry
	device *homeassistant.DeviceRegistryEntry
}

// selectAreaMembers returns the entities considered part of the
// area, plus the count of entities matched before the diagnostic/
// disabled filter was applied. Entity area assignment can come
// directly (entity.AreaID) or be inherited from the device
// (device.AreaID); both paths must be honored.
func selectAreaMembers(
	entities []homeassistant.EntityRegistryEntry,
	deviceByID map[string]*homeassistant.DeviceRegistryEntry,
	areaID string,
	includeDiagnostic bool,
) ([]areaMember, int) {
	members := make([]areaMember, 0, 16)
	totalMatched := 0
	for i := range entities {
		entry := entities[i]
		var device *homeassistant.DeviceRegistryEntry
		if entry.DeviceID != "" {
			device = deviceByID[entry.DeviceID]
		}

		entityAreaID := entry.AreaID
		if entityAreaID == "" && device != nil {
			entityAreaID = device.AreaID
		}
		if entityAreaID != areaID {
			continue
		}
		totalMatched++

		if entry.DisabledBy != "" || entry.HiddenBy != "" {
			continue
		}
		if !includeDiagnostic && (entry.EntityCategory == "diagnostic" || entry.EntityCategory == "config") {
			continue
		}

		members = append(members, areaMember{entry: entry, device: device})
	}
	return members, totalMatched
}

func indexDevices(devices []homeassistant.DeviceRegistryEntry) map[string]*homeassistant.DeviceRegistryEntry {
	out := make(map[string]*homeassistant.DeviceRegistryEntry, len(devices))
	for i := range devices {
		out[devices[i].ID] = &devices[i]
	}
	return out
}

func indexStates(states []homeassistant.State) map[string]*homeassistant.State {
	out := make(map[string]*homeassistant.State, len(states))
	for i := range states {
		out[states[i].EntityID] = &states[i]
	}
	return out
}

func emptyAreaPayload(area homeassistant.Area, lookback, totalInArea int) map[string]any {
	return map[string]any{
		"area":           area.Name,
		"area_id":        area.AreaID,
		"lookback":       fmt.Sprintf("-%ds", lookback),
		"entity_count":   0,
		"filtered_count": totalInArea,
		"anomalies":      []any{},
		"active":         []any{},
		"recent_changes": []any{},
		"ambient":        []any{},
		"stable":         []any{},
	}
}

// areaClassifier holds the per-bucket accumulators and the cutoff
// for the recent-changes window. Each entity is placed into exactly
// one bucket, evaluated in order: anomalies → active → recent →
// ambient → stable.
type areaClassifier struct {
	cutoff        time.Time
	anomalies     []map[string]any
	active        []map[string]any
	recentChanges []map[string]any
	ambient       []map[string]any
	stable        []map[string]any
}

func newAreaClassifier(members []areaMember, cutoff time.Time) *areaClassifier {
	return &areaClassifier{
		cutoff:        cutoff,
		anomalies:     make([]map[string]any, 0, len(members)),
		active:        make([]map[string]any, 0, len(members)),
		recentChanges: make([]map[string]any, 0, len(members)),
		ambient:       make([]map[string]any, 0, len(members)),
		stable:        make([]map[string]any, 0, len(members)),
	}
}

// placeMissing records entities present in the registry but absent
// from the states pull. Treated as an anomaly so the model knows the
// entity exists but is not currently reporting.
func (c *areaClassifier) placeMissing(m areaMember, now time.Time) {
	c.anomalies = append(c.anomalies, map[string]any{
		"entity":    m.entry.EntityID,
		"available": false,
		"reason":    "no_state",
	})
}

func (c *areaClassifier) classify(m areaMember, state *homeassistant.State, now time.Time) {
	rendered := renderEntityForArea(state, now)

	if isSentinelState(state.State) {
		c.anomalies = append(c.anomalies, rendered)
		return
	}
	if isAlarmAnomaly(state, m.entry) {
		c.anomalies = append(c.anomalies, rendered)
		return
	}
	if isActive(state, m.entry) {
		c.active = append(c.active, rendered)
		return
	}
	if !state.LastChanged.IsZero() && state.LastChanged.After(c.cutoff) {
		c.recentChanges = append(c.recentChanges, rendered)
		return
	}
	if isAmbientNumeric(state, m.entry) {
		c.ambient = append(c.ambient, rendered)
		return
	}
	c.stable = append(c.stable, rendered)
}

// renderEntityForArea reuses the watchlist's formatEntityContext so
// the area tool emits the same per-entity shape the watchlist does
// — every device_class translation, sentinel handling, and domain
// formatter applies. The string is then unmarshalled back into a
// map so it can sit inside a bucket array.
func renderEntityForArea(state *homeassistant.State, now time.Time) map[string]any {
	raw := formatEntityContext(state, now)
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{"entity": state.EntityID}
	}
	return out
}

func isAlarmAnomaly(state *homeassistant.State, entry homeassistant.EntityRegistryEntry) bool {
	domain := entityDomain(state.EntityID)
	deviceClass := registryDeviceClass(entry, state)
	switch domain {
	case "binary_sensor":
		return state.State == "on" && alarmSecurityClasses[deviceClass]
	case "lock":
		return state.State == "jammed"
	case "vacuum":
		return state.State == "error"
	}
	return false
}

func isActive(state *homeassistant.State, entry homeassistant.EntityRegistryEntry) bool {
	domain := entityDomain(state.EntityID)
	switch domain {
	case "light", "switch", "fan":
		return state.State == "on"
	case "media_player":
		return hasActiveMedia(state.State)
	case "vacuum":
		return state.State == "cleaning" || state.State == "returning"
	case "cover":
		return state.State == "opening" || state.State == "closing"
	case "climate":
		action := attrString(state.Attributes, "hvac_action")
		switch action {
		case "heating", "cooling", "drying", "fan":
			return true
		}
	}
	return false
}

func isAmbientNumeric(state *homeassistant.State, entry homeassistant.EntityRegistryEntry) bool {
	if entityDomain(state.EntityID) != "sensor" {
		return false
	}
	if _, err := strconv.ParseFloat(state.State, 64); err != nil {
		return false
	}
	return ambientNumericClasses[registryDeviceClass(entry, state)]
}

// registryDeviceClass prefers the entity registry's device_class
// (which respects user overrides via OriginalDeviceClass fallback)
// then falls back to the live state attribute. Either may be empty
// when the integration didn't set one. Safe to call with nil state.
func registryDeviceClass(entry homeassistant.EntityRegistryEntry, state *homeassistant.State) string {
	if entry.DeviceClass != "" {
		return entry.DeviceClass
	}
	if entry.OriginalDeviceClass != "" {
		return entry.OriginalDeviceClass
	}
	if state == nil {
		return ""
	}
	return attrString(state.Attributes, "device_class")
}

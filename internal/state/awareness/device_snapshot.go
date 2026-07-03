package awareness

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// DeviceSnapshotClient is the slice of Home Assistant calls the
// ha_device tool needs. It extends the shared registry surface with a
// targeted single-entity state read: a device owns a handful of
// entities whose ids we already know from the registry, so we fetch
// each one directly rather than pulling the whole live state set
// (15k+ entities in production) just to keep a few. The concrete
// homeassistant.Client implements every method out of the box.
type DeviceSnapshotClient interface {
	HARegistryClient
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
}

// DeviceSnapshotRequest describes one ha_device invocation.
type DeviceSnapshotRequest struct {
	// Device is a device_id or a name (matched case-insensitively
	// against the user-assigned name, then the registry name, then by
	// substring).
	Device string
	// Include is the optional per-entity metadata projection applied to
	// each child entity, mirroring the other native HA tools.
	Include homeassistant.EntityMetadataIncludes
}

// maxDeviceNoisyGroupEntities caps the Configuration and Diagnostic groups so
// a device that exposes a long tail of tuning knobs and health counters
// can't flood the prompt. Controls and Sensors are inherently small and
// stay uncapped; overflow is reported via the per-group
// *_truncated_count fields. Mirrors area_activity's stable-bucket cap.
const maxDeviceNoisyGroupEntities = 15

// ComputeDeviceSnapshot resolves a device, gathers its child entities +
// states, groups them the way Home Assistant's device page does, and
// returns a single JSON object describing the device for model
// consumption. The output leads with device identity and an availability
// rollup, then the entities in HA's four device-page groups — Controls,
// Sensors, Configuration, Diagnostic — so the model reads a device as its
// whole instrument panel, the same structure a human sees in HA.
func ComputeDeviceSnapshot(ctx context.Context, client DeviceSnapshotClient, req DeviceSnapshotRequest, now time.Time) (string, error) {
	if client == nil {
		return "", fmt.Errorf("ha_device: client is required")
	}
	if strings.TrimSpace(req.Device) == "" {
		return "", fmt.Errorf("ha_device: device is required")
	}

	devices, err := client.GetDeviceRegistry(ctx)
	if err != nil {
		return "", fmt.Errorf("ha_device: get device registry: %w", err)
	}
	device, candidates, ok := resolveDevice(devices, req.Device)
	if !ok {
		if len(candidates) > 0 {
			return promptfmt.MarshalCompact(map[string]any{
				"query":      req.Device,
				"found":      false,
				"reason":     "ambiguous",
				"candidates": candidates,
				"note":       "multiple devices matched; re-call with a device_id from candidates",
			}), nil
		}
		return promptfmt.MarshalCompact(map[string]any{
			"query":  req.Device,
			"found":  false,
			"reason": "no_match",
			"note":   "no device matched by id, name, or substring",
		}), nil
	}

	entities, err := client.GetEntityRegistry(ctx)
	if err != nil {
		return "", fmt.Errorf("ha_device: get entity registry: %w", err)
	}

	// Child entities: every registry entry owned by this device.
	// Disabled entities are excluded from the buckets (HA doesn't load
	// them, so they have no live state and would render as misleading
	// "no_state" anomalies) and reported via disabled_count instead.
	deviceByID := indexDevices(devices)
	var children []areaMember
	disabled := 0
	for i := range entities {
		entry := entities[i]
		if entry.DeviceID != device.ID {
			continue
		}
		if entry.DisabledBy != "" {
			disabled++
			continue
		}
		children = append(children, areaMember{entry: entry, device: deviceByID[entry.DeviceID]})
	}

	// Targeted state read per child — exact ids, bounded count.
	statesByID := make(map[string]*homeassistant.State, len(children))
	for _, m := range children {
		state, stateErr := client.GetState(ctx, m.entry.EntityID)
		if stateErr != nil {
			// A failed read is surfaced as an unavailable child rather
			// than failing the whole snapshot — other children still
			// describe the device's health.
			continue
		}
		statesByID[m.entry.EntityID] = state
	}

	cls := classifyDeviceChildren(children, statesByID, now)

	resolver, err := buildDeviceResolver(ctx, client, devices, req.Include)
	if err != nil {
		return "", err
	}

	payload := map[string]any{
		"device":       deviceDisplayName(device),
		"device_id":    device.ID,
		"entity_count": len(children),
		"availability": map[string]any{
			"reporting": cls.reporting,
			"total":     len(children),
		},
	}
	if identity := resolver.DeviceMetadata(&device); identity != nil {
		payload["identity"] = identity
	}
	if integration := deviceIntegration(ctx, client, device); integration != "" {
		payload["integration"] = integration
	}
	if via := deviceByID[device.ViaDeviceID]; via != nil {
		payload["via_device"] = deviceDisplayName(*via)
	}
	if disabled > 0 {
		payload["disabled_count"] = disabled
	}
	if len(cls.unavailable) > 0 {
		payload["unavailable"] = cls.unavailable
	}

	// Configuration and Diagnostic are the noisy groups — a single
	// Z-Wave device can expose dozens of tuning knobs and health
	// counters. Cap them (with an honest per-group truncation count) so
	// the instrument panel stays legible; Controls and Sensors are
	// inherently small and stay uncapped.
	configTruncated := truncateGroup(&cls.configuration, maxDeviceNoisyGroupEntities)
	diagnosticTruncated := truncateGroup(&cls.diagnostic, maxDeviceNoisyGroupEntities)

	if req.Include.Any() {
		attachAreaEntityMetadata(resolver, req.Include, children, statesByID,
			cls.controls, cls.sensors, cls.configuration, cls.diagnostic)
	}

	if len(cls.controls) > 0 {
		payload["controls"] = cls.controls
	}
	if len(cls.sensors) > 0 {
		payload["sensors"] = cls.sensors
	}
	if len(cls.configuration) > 0 {
		payload["configuration"] = cls.configuration
	}
	if configTruncated > 0 {
		payload["configuration_truncated_count"] = configTruncated
	}
	if len(cls.diagnostic) > 0 {
		payload["diagnostic"] = cls.diagnostic
	}
	if diagnosticTruncated > 0 {
		payload["diagnostic_truncated_count"] = diagnosticTruncated
	}

	return promptfmt.MarshalCompact(payload), nil
}

// truncateGroup caps a device group in place, returning how many entries
// were dropped so the caller can advertise the overflow.
func truncateGroup(group *[]map[string]any, limit int) int {
	if len(*group) <= limit {
		return 0
	}
	dropped := len(*group) - limit
	*group = (*group)[:limit]
	return dropped
}

// buildDeviceResolver assembles the metadata resolver for the device
// snapshot. Areas and labels are always fetched so the device's own
// identity card can carry its resolved area name and label names by
// default (a device view is where those belong); floors are pulled only
// when the include projection asks for them. devices is reused from the
// caller's already-fetched device registry rather than re-fetched.
// Mirrors the gated-fetch pattern in ComputeAreaActivity.
func buildDeviceResolver(
	ctx context.Context,
	client DeviceSnapshotClient,
	devices []homeassistant.DeviceRegistryEntry,
	include homeassistant.EntityMetadataIncludes,
) (homeassistant.EntityMetadataResolver, error) {
	areas, err := client.GetAreas(ctx)
	if err != nil {
		return homeassistant.EntityMetadataResolver{}, fmt.Errorf("ha_device: get areas: %w", err)
	}
	labels, err := client.GetLabelRegistry(ctx)
	if err != nil {
		return homeassistant.EntityMetadataResolver{}, fmt.Errorf("ha_device: get labels: %w", err)
	}
	var floors []homeassistant.FloorRegistryEntry
	if include.Area {
		floors, err = client.GetFloorRegistry(ctx)
		if err != nil {
			return homeassistant.EntityMetadataResolver{}, fmt.Errorf("ha_device: get floors: %w", err)
		}
	}
	floorAlias := ""
	if p, ok := client.(interface{ FloorMetadataAlias() string }); ok {
		floorAlias = p.FloorMetadataAlias()
	}
	return homeassistant.NewEntityMetadataResolverWithFloorAlias(areas, labels, devices, floors, floorAlias), nil
}

// deviceControlDomains are the entity domains Home Assistant treats as
// device Controls — the actionable primaries. Uncategorized entities
// outside this set are read-only Sensors. Config/diagnostic entities go
// to their own groups regardless of domain (see [deviceGroupFor]).
var deviceControlDomains = map[string]bool{
	"light": true, "switch": true, "fan": true, "climate": true,
	"cover": true, "lock": true, "media_player": true, "vacuum": true,
	"number": true, "select": true, "button": true, "siren": true,
	"valve": true, "humidifier": true, "water_heater": true,
	"lawn_mower": true, "alarm_control_panel": true, "camera": true,
	"remote": true, "text": true, "date": true, "time": true,
	"datetime": true, "scene": true, "script": true, "automation": true,
	"input_boolean": true, "input_number": true, "input_select": true,
	"input_text": true, "input_datetime": true, "input_button": true,
	"update": true,
}

// deviceGroupFor places an entity into one of Home Assistant's device-
// page groups. Configuration and Diagnostic follow the registry's
// entity_category; the uncategorized primaries split into Controls
// (actionable domains) and Sensors (read-only) — the exact structure a
// human sees on a device page.
func deviceGroupFor(entry homeassistant.EntityRegistryEntry) string {
	switch entry.EntityCategory {
	case "config":
		return "configuration"
	case "diagnostic":
		return "diagnostic"
	}
	if deviceControlDomains[entityDomain(entry.EntityID)] {
		return "controls"
	}
	return "sensors"
}

// deviceChildGroups holds a device's child entities grouped the way HA's
// device page groups them, plus the availability rollup. Controls and
// Sensors are the primary affordances; Configuration and Diagnostic are
// the categorized entities HA keeps off its generated dashboards but
// shows on the device page — which is why a device view surfaces them.
type deviceChildGroups struct {
	controls      []map[string]any
	sensors       []map[string]any
	configuration []map[string]any
	diagnostic    []map[string]any
	reporting     int      // children with a live, non-sentinel state
	unavailable   []string // entity_ids missing a state or in a sentinel state
}

// classifyDeviceChildren groups a device's child entities into HA's four
// device-page groups, reusing the area snapshot's per-entity render so
// the two views stay consistent. It mirrors the HA device page: every
// entity the device exposes is shown (including hidden ones, marked, and
// the config/diagnostic entities the generated dashboard omits), because
// inspecting a device by name is explicit intent — you want its whole
// instrument panel, not the curated subset.
func classifyDeviceChildren(children []areaMember, statesByID map[string]*homeassistant.State, now time.Time) deviceChildGroups {
	var g deviceChildGroups
	for _, m := range children {
		state := statesByID[m.entry.EntityID]
		var rendered map[string]any
		if state == nil {
			rendered = map[string]any{
				"entity":    m.entry.EntityID,
				"available": false,
				"reason":    "no_state",
			}
			g.unavailable = append(g.unavailable, m.entry.EntityID)
		} else {
			rendered = renderEntityForArea(state, now)
			if isSentinelState(state.State) {
				g.unavailable = append(g.unavailable, m.entry.EntityID)
			} else {
				g.reporting++
			}
		}
		// Mark operator-hidden entities so the model can see this one is
		// off HA's generated surfaces even though the device view shows it.
		if m.entry.HiddenBy != "" {
			rendered["hidden"] = true
		}
		switch deviceGroupFor(m.entry) {
		case "controls":
			g.controls = append(g.controls, rendered)
		case "configuration":
			g.configuration = append(g.configuration, rendered)
		case "diagnostic":
			g.diagnostic = append(g.diagnostic, rendered)
		default:
			g.sensors = append(g.sensors, rendered)
		}
	}
	sort.Strings(g.unavailable)
	return g
}

// resolveDevice matches a device by id, then by case-insensitive
// user-assigned name or registry name, then by substring. It mirrors
// resolveArea's exact-first strategy and returns candidate device_ids
// when a substring query is ambiguous so the model can disambiguate.
func resolveDevice(devices []homeassistant.DeviceRegistryEntry, query string) (homeassistant.DeviceRegistryEntry, []string, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return homeassistant.DeviceRegistryEntry{}, nil, false
	}
	// Exact id.
	for i := range devices {
		if devices[i].ID == q {
			return devices[i], nil, true
		}
	}
	// Exact (case-insensitive) name or user-assigned name.
	for i := range devices {
		if strings.EqualFold(string(devices[i].NameByUser), q) || strings.EqualFold(string(devices[i].Name), q) {
			return devices[i], nil, true
		}
	}
	// Substring on either name; collect all matches for disambiguation.
	qLower := strings.ToLower(q)
	var matches []int
	for i := range devices {
		name := strings.ToLower(deviceDisplayName(devices[i]))
		if name != "" && strings.Contains(name, qLower) {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 0:
		return homeassistant.DeviceRegistryEntry{}, nil, false
	case 1:
		return devices[matches[0]], nil, true
	default:
		candidates := make([]string, 0, len(matches))
		for _, idx := range matches {
			candidates = append(candidates, fmt.Sprintf("%s (%s)", devices[idx].ID, deviceDisplayName(devices[idx])))
		}
		sort.Strings(candidates)
		return homeassistant.DeviceRegistryEntry{}, candidates, false
	}
}

// deviceDisplayName prefers the user-assigned name, then the registry
// name, then the device id so a device always renders with a stable
// label.
func deviceDisplayName(device homeassistant.DeviceRegistryEntry) string {
	if device.NameByUser != "" {
		return string(device.NameByUser)
	}
	if device.Name != "" {
		return string(device.Name)
	}
	return device.ID
}

// deviceIntegration resolves the device's owning integration (config
// entry domain) from the primary config entry, falling back to the
// first config entry. Returns "" when the device has no config entry or
// the registry can't be read — integration is enrichment, not load-
// bearing, so a fetch failure just omits it.
func deviceIntegration(ctx context.Context, client DeviceSnapshotClient, device homeassistant.DeviceRegistryEntry) string {
	entryID := device.PrimaryConfigEntry
	if entryID == "" {
		if len(device.ConfigEntries) == 0 {
			return ""
		}
		entryID = device.ConfigEntries[0]
	}
	entries, err := client.GetConfigEntries(ctx)
	if err != nil {
		return ""
	}
	for i := range entries {
		if entries[i].EntryID == entryID {
			return entries[i].Domain
		}
	}
	return ""
}

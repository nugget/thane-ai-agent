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

// maxDeviceOtherEntities caps the least-salient ("other") bucket so a
// device that exposes a long tail of config/diagnostic sensors can't
// flood the prompt. Anomalies, active, and ambient buckets are
// inherently small and stay uncapped; overflow on "other" is reported
// via other_truncated_count. Mirrors area_activity's stable-bucket cap.
const maxDeviceOtherEntities = 15

// ComputeDeviceSnapshot resolves a device, gathers its child entities +
// states, buckets them by salience, and returns a single JSON object
// describing the device for model consumption. The output leads with
// device identity and an availability rollup, then the entities grouped
// most-actionable-first (anomalies → active → ambient → other), matching
// the model-facing-context conventions used by the area snapshot.
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

	otherTruncated := 0
	if len(cls.other) > maxDeviceOtherEntities {
		otherTruncated = len(cls.other) - maxDeviceOtherEntities
		cls.other = cls.other[:maxDeviceOtherEntities]
	}

	if req.Include.Any() {
		attachAreaEntityMetadata(resolver, req.Include, children, statesByID,
			cls.anomalies, cls.active, cls.ambient, cls.other)
	}

	if len(cls.anomalies) > 0 {
		payload["anomalies"] = cls.anomalies
	}
	if len(cls.active) > 0 {
		payload["active"] = cls.active
	}
	if len(cls.ambient) > 0 {
		payload["ambient"] = cls.ambient
	}
	if len(cls.other) > 0 {
		payload["other"] = cls.other
	}
	if otherTruncated > 0 {
		payload["other_truncated_count"] = otherTruncated
	}

	return promptfmt.MarshalCompact(payload), nil
}

// buildDeviceResolver assembles the metadata resolver for the device
// snapshot. Areas are always fetched so device identity can carry the
// resolved area name; labels and floors are pulled only when the
// include projection asks for them. devices is reused from the caller's
// already-fetched device registry rather than re-fetched. Mirrors the
// gated-fetch pattern in ComputeAreaActivity.
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
	var labels []homeassistant.LabelRegistryEntry
	if include.Labels {
		labels, err = client.GetLabelRegistry(ctx)
		if err != nil {
			return homeassistant.EntityMetadataResolver{}, fmt.Errorf("ha_device: get labels: %w", err)
		}
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

// deviceChildBuckets holds the salience-grouped render of a device's
// child entities plus the availability rollup.
type deviceChildBuckets struct {
	anomalies   []map[string]any
	active      []map[string]any
	ambient     []map[string]any
	other       []map[string]any
	reporting   int      // children with a live, non-sentinel state
	unavailable []string // entity_ids missing a state or in a sentinel state
}

// classifyDeviceChildren buckets a device's child entities by salience,
// reusing the same per-entity render and classification predicates the
// area snapshot uses so the two views stay consistent. Unlike the area
// classifier there is no recent-changes window — a device view answers
// "what does this device expose and is it healthy" rather than "what
// happened in this room lately."
func classifyDeviceChildren(children []areaMember, statesByID map[string]*homeassistant.State, now time.Time) deviceChildBuckets {
	var b deviceChildBuckets
	for _, m := range children {
		state := statesByID[m.entry.EntityID]
		if state == nil {
			b.anomalies = append(b.anomalies, map[string]any{
				"entity":    m.entry.EntityID,
				"available": false,
				"reason":    "no_state",
			})
			b.unavailable = append(b.unavailable, m.entry.EntityID)
			continue
		}
		rendered := renderEntityForArea(state, now)
		switch {
		case isSentinelState(state.State):
			b.anomalies = append(b.anomalies, rendered)
			b.unavailable = append(b.unavailable, m.entry.EntityID)
		case isAlarmAnomaly(state, m.entry):
			b.anomalies = append(b.anomalies, rendered)
			b.reporting++
		case isActive(state, m.entry):
			b.active = append(b.active, rendered)
			b.reporting++
		case isAmbientNumeric(state, m.entry):
			b.ambient = append(b.ambient, rendered)
			b.reporting++
		default:
			b.other = append(b.other, rendered)
			b.reporting++
		}
	}
	sort.Strings(b.unavailable)
	return b
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

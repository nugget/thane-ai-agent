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

// homeSnapshotDefaultMaxPerSection caps each section so a large install
// can't flood the prompt. Overflow is reported per section via
// <section>_truncated_count, mirroring the area snapshot's bucket caps.
const homeSnapshotDefaultMaxPerSection = 12

// homeOpeningClasses are the binary_sensor device_classes whose "on"
// state means something is physically open — the security/openings view.
var homeOpeningClasses = map[string]bool{
	"door":        true,
	"window":      true,
	"garage_door": true,
	"opening":     true,
}

// homeEnergyClasses are the sensor device_classes surfaced in the
// optional energy section.
var homeEnergyClasses = map[string]bool{
	"power":  true,
	"energy": true,
}

// HomeSnapshotRequest describes one ha_home_snapshot invocation.
type HomeSnapshotRequest struct {
	IncludeDiagnostic bool                                 // include diagnostic/config entities in the scan
	IncludeHidden     bool                                 // include hidden-but-enabled entities
	IncludeEnergy     bool                                 // add the optional energy section
	MaxPerSection     int                                  // per-section cap; <= 0 uses the default
	Include           homeassistant.EntityMetadataIncludes // optional per-entity metadata projection
}

// ComputeHomeSnapshot renders a single curated "how's the house right
// now" view across domains: anomalies first (offline / alarm), then
// security/openings (open doors-windows, unlocked locks, armed/triggered
// alarm panels), then presence (who's home), climate (thermostats), and
// an optional energy section. It is the whole-home cousin of
// [ComputeAreaActivity]: one bulk GetStates, the same registry resolver
// and salience predicates, the same per-entity render and metadata
// attach, the same bounded output with explicit *_truncated_count
// fields. Unlike the area view it is deliberately curated — entities
// that don't fall into a section are skipped, not bucketed into a
// remainder — so the snapshot stays a glanceable overview.
func ComputeHomeSnapshot(ctx context.Context, client HARegistryClient, req HomeSnapshotRequest, now time.Time) (string, error) {
	if client == nil {
		return "", fmt.Errorf("ha_home_snapshot: client is required")
	}

	maxPerSection := req.MaxPerSection
	if maxPerSection <= 0 {
		maxPerSection = homeSnapshotDefaultMaxPerSection
	}

	allStates, err := client.GetStates(ctx)
	if err != nil {
		return "", fmt.Errorf("ha_home_snapshot: get states: %w", err)
	}
	entities, err := client.GetEntityRegistry(ctx)
	if err != nil {
		return "", fmt.Errorf("ha_home_snapshot: get entity registry: %w", err)
	}
	devices, err := client.GetDeviceRegistry(ctx)
	if err != nil {
		return "", fmt.Errorf("ha_home_snapshot: get device registry: %w", err)
	}

	resolver, err := buildHomeResolver(ctx, client, devices, req.Include)
	if err != nil {
		return "", err
	}

	deviceByID := indexDevices(devices)
	statesByID := indexStates(allStates)

	members, filters := selectHomeMembers(entities, deviceByID, req.IncludeDiagnostic, req.IncludeHidden)

	sections := classifyHomeMembers(members, statesByID, req.IncludeEnergy, now)

	payload := map[string]any{
		"summary": map[string]any{
			"anomalies": len(sections.anomalies),
			"security":  len(sections.security),
			"home":      sections.home,
			"away":      sections.away,
		},
		"entity_count": len(members),
	}
	if !sections.hasConcern() {
		// Nothing offline, open, unlocked, or armed: tell the model the
		// house is quiet up front so it doesn't scan empty sections to
		// infer it. Presence/climate still render below for context.
		payload["status"] = "quiet"
	}
	addAreaFilterCounts(payload, filters)

	if req.Include.Any() {
		attachAreaEntityMetadata(resolver, req.Include, members, statesByID,
			sections.anomalies, sections.security, sections.presence, sections.climate, sections.energy)
	}

	// Salience order: lead with what's actionable or anomalous.
	addHomeSection(payload, "anomalies", sections.anomalies, maxPerSection)
	addHomeSection(payload, "security", sections.security, maxPerSection)
	addHomeSection(payload, "presence", sections.presence, maxPerSection)
	addHomeSection(payload, "climate", sections.climate, maxPerSection)
	if req.IncludeEnergy {
		addHomeSection(payload, "energy", sections.energy, maxPerSection)
	}

	return promptfmt.MarshalCompact(payload), nil
}

// addHomeSection writes a section into the payload, capping it at
// maxPerSection and recording the overflow as <key>_truncated_count.
// Empty sections are omitted so the snapshot stays lean.
func addHomeSection(payload map[string]any, key string, items []map[string]any, maxPerSection int) {
	if len(items) == 0 {
		return
	}
	truncated := 0
	if len(items) > maxPerSection {
		truncated = len(items) - maxPerSection
		items = items[:maxPerSection]
	}
	payload[key] = items
	if truncated > 0 {
		payload[key+"_truncated_count"] = truncated
	}
}

// homeSections holds the curated cross-domain buckets plus the presence
// rollup counts.
type homeSections struct {
	anomalies []map[string]any
	security  []map[string]any
	presence  []map[string]any
	climate   []map[string]any
	energy    []map[string]any
	home      int // persons currently home
	away      int // persons not home (not_home or in another zone)
}

// hasConcern reports whether anything actionable is present — an
// anomaly or an open/unlocked/armed security item. Presence and climate
// alone are normal ambient state, not a concern.
func (s homeSections) hasConcern() bool {
	return len(s.anomalies) > 0 || len(s.security) > 0
}

// classifyHomeMembers routes each member into at most one curated
// section, reusing the area snapshot's per-entity render and the shared
// salience predicates. Entities that fit no section are skipped.
func classifyHomeMembers(members []areaMember, statesByID map[string]*homeassistant.State, includeEnergy bool, now time.Time) homeSections {
	var s homeSections
	for _, m := range members {
		state := statesByID[m.entry.EntityID]
		if state == nil {
			continue // no live state: not part of a "right now" view
		}
		domain := entityDomain(m.entry.EntityID)

		// Anomalies lead: unavailable/unknown, then genuine alarms
		// (smoke/CO/gas, lock jammed, vacuum error).
		if isSentinelState(state.State) {
			// Presence and energy sentinels are noise here; only surface
			// unavailability for entities a home view actually cares about.
			if homeAnomalyDomain(domain) {
				s.anomalies = append(s.anomalies, renderEntityForArea(state, now))
			}
			continue
		}
		if isAlarmAnomaly(state, m.entry) {
			s.anomalies = append(s.anomalies, renderEntityForArea(state, now))
			continue
		}

		switch domain {
		case "person":
			s.presence = append(s.presence, renderEntityForArea(state, now))
			if presenceIsHome(state.State) {
				s.home++
			} else {
				s.away++
			}
		case "lock":
			if state.State == "unlocked" {
				s.security = append(s.security, renderEntityForArea(state, now))
			}
		case "alarm_control_panel":
			if state.State != "disarmed" {
				s.security = append(s.security, renderEntityForArea(state, now))
			}
		case "cover":
			if state.State == "open" || state.State == "opening" {
				s.security = append(s.security, renderEntityForArea(state, now))
			}
		case "binary_sensor":
			if state.State == "on" && homeOpeningClasses[registryDeviceClass(m.entry, state)] {
				s.security = append(s.security, renderEntityForArea(state, now))
			}
		case "climate":
			s.climate = append(s.climate, renderEntityForArea(state, now))
		case "sensor":
			if includeEnergy && homeEnergyClasses[registryDeviceClass(m.entry, state)] {
				s.energy = append(s.energy, renderEntityForArea(state, now))
			}
		}
	}

	sortHomeSection(s.anomalies)
	sortHomeSection(s.security)
	sortHomeSection(s.presence)
	sortHomeSection(s.climate)
	sortHomeSection(s.energy)
	return s
}

// homeAnomalyDomain reports whether an unavailable entity in this domain
// is worth surfacing in the home view. Presence/energy/diagnostic noise
// going unavailable isn't actionable at a glance; locks, covers, climate,
// alarms, and contact/safety sensors are.
func homeAnomalyDomain(domain string) bool {
	switch domain {
	case "lock", "cover", "climate", "alarm_control_panel", "binary_sensor":
		return true
	default:
		return false
	}
}

// presenceIsHome reports whether a person entity's state means they are
// home. Any other value (not_home, or a named zone like "Work") counts
// as away.
func presenceIsHome(state string) bool {
	return strings.EqualFold(state, "home")
}

// sortHomeSection orders a section's entities by entity_id for stable,
// deterministic output.
func sortHomeSection(items []map[string]any) {
	sort.SliceStable(items, func(i, j int) bool {
		ai, _ := items[i]["entity"].(string)
		aj, _ := items[j]["entity"].(string)
		return ai < aj
	})
}

// selectHomeMembers returns every enabled, salient entity across the
// whole install, plus the filtered-out counts. It is selectAreaMembers
// without the area constraint, sharing the same salience filter.
func selectHomeMembers(
	entities []homeassistant.EntityRegistryEntry,
	deviceByID map[string]*homeassistant.DeviceRegistryEntry,
	includeDiagnostic bool,
	includeHidden bool,
) ([]areaMember, areaFilterCounts) {
	members := make([]areaMember, 0, len(entities))
	counts := areaFilterCounts{}
	for i := range entities {
		entry := entities[i]
		counts.TotalMatched++
		if !keepAfterSalienceFilter(entry, includeDiagnostic, includeHidden, &counts) {
			continue
		}
		var device *homeassistant.DeviceRegistryEntry
		if entry.DeviceID != "" {
			device = deviceByID[entry.DeviceID]
		}
		members = append(members, areaMember{entry: entry, device: device})
	}
	return members, counts
}

// buildHomeResolver assembles the metadata resolver for the home
// snapshot. Areas + devices are always included so per-entity area
// context resolves; labels and floors are pulled only when the include
// projection asks for them. Mirrors the gated build in
// ComputeAreaActivity.
func buildHomeResolver(
	ctx context.Context,
	client HARegistryClient,
	devices []homeassistant.DeviceRegistryEntry,
	include homeassistant.EntityMetadataIncludes,
) (homeassistant.EntityMetadataResolver, error) {
	areas, err := client.GetAreas(ctx)
	if err != nil {
		return homeassistant.EntityMetadataResolver{}, fmt.Errorf("ha_home_snapshot: get areas: %w", err)
	}
	var labels []homeassistant.LabelRegistryEntry
	if include.Labels {
		labels, err = client.GetLabelRegistry(ctx)
		if err != nil {
			return homeassistant.EntityMetadataResolver{}, fmt.Errorf("ha_home_snapshot: get labels: %w", err)
		}
	}
	var floors []homeassistant.FloorRegistryEntry
	if include.Area {
		floors, err = client.GetFloorRegistry(ctx)
		if err != nil {
			return homeassistant.EntityMetadataResolver{}, fmt.Errorf("ha_home_snapshot: get floors: %w", err)
		}
	}
	floorAlias := ""
	if p, ok := client.(interface{ FloorMetadataAlias() string }); ok {
		floorAlias = p.FloorMetadataAlias()
	}
	return homeassistant.NewEntityMetadataResolverWithFloorAlias(areas, labels, devices, floors, floorAlias), nil
}

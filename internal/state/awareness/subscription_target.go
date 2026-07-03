package awareness

import (
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// SubscriptionTargetKind classifies what a subscription's target
// addresses. Entity and glob resolve against live states; area, label,
// and floor resolve against the registry, so their membership follows
// the home as the registry changes — move a sensor into the office and
// an office subscription picks it up with no re-authoring.
type SubscriptionTargetKind int

const (
	// TargetEntity is a concrete entity_id (e.g. sensor.office_temperature).
	TargetEntity SubscriptionTargetKind = iota
	// TargetGlob is an entity_id glob (e.g. binary_sensor.*door*).
	TargetGlob
	// TargetArea is an area, addressed as "area:<area_id>".
	TargetArea
	// TargetLabel is a label, addressed as "label:<label_id>".
	TargetLabel
	// TargetFloor is a floor, addressed as "floor:<floor_id>".
	TargetFloor
)

// registryTargetPrefixes maps the stored-string prefix of a
// registry-backed target to its kind. Concrete entity IDs are
// domain.object_id (dot-separated) and globs carry wildcard runes, so a
// "kind:value" colon form is unambiguous against both — HA has no
// colon-bearing entity ids.
var registryTargetPrefixes = map[string]SubscriptionTargetKind{
	"area":  TargetArea,
	"label": TargetLabel,
	"floor": TargetFloor,
}

// SubscriptionTarget is the typed view of a subscription's addressing.
// It is parsed from the stored string form (which the watchlist row and
// the loop-spec entity_id both carry) so callers reason over a kind,
// not a string, while storage stays a single discriminated column.
type SubscriptionTarget struct {
	Kind  SubscriptionTargetKind
	Value string // entity_id, glob pattern, or bare registry id (no prefix)
}

// ParseSubscriptionTarget interprets a stored subscription string: a
// "area:"/"label:"/"floor:" prefix selects a registry target, a
// wildcard-bearing value is a glob, anything else is a concrete entity.
func ParseSubscriptionTarget(raw string) SubscriptionTarget {
	raw = strings.TrimSpace(raw)
	if prefix, value, ok := strings.Cut(raw, ":"); ok {
		if kind, known := registryTargetPrefixes[prefix]; known {
			// An empty id after the prefix ("area:") is a malformed
			// target, not a wildcard. Falling through renders it as a
			// bogus concrete entity (a clean not-found signal) rather
			// than silently matching every entity with no area/floor.
			if v := strings.TrimSpace(value); v != "" {
				return SubscriptionTarget{Kind: kind, Value: v}
			}
		}
	}
	if homeassistant.IsEntityGlob(raw) {
		return SubscriptionTarget{Kind: TargetGlob, Value: raw}
	}
	return SubscriptionTarget{Kind: TargetEntity, Value: raw}
}

// IsRegistryTarget reports whether the target resolves its members
// against the registry (area/label/floor) rather than against live
// entity ids (entity/glob).
func (t SubscriptionTarget) IsRegistryTarget() bool {
	switch t.Kind {
	case TargetArea, TargetLabel, TargetFloor:
		return true
	default:
		return false
	}
}

// membershipResolver answers "which entities belong to this registry
// target," walking the registry with HA's inheritance rules: an
// entity's effective area is its own area_id or, unset, its device's;
// an entity carries a label if it or its device carries it; an entity
// is on a floor if its effective area sits on that floor.
type membershipResolver struct {
	entitiesByID map[string]*homeassistant.EntityRegistryEntry
	devicesByID  map[string]*homeassistant.DeviceRegistryEntry
	areaToFloor  map[string]string // area_id → floor_id
}

func newMembershipResolver(registries *renderRegistries) (*membershipResolver, error) {
	entities, err := registries.entities()
	if err != nil {
		return nil, err
	}
	devices, err := registries.devices()
	if err != nil {
		return nil, err
	}
	areas, err := registries.areaEntries()
	if err != nil {
		return nil, err
	}
	areaToFloor := make(map[string]string, len(areas))
	for _, a := range areas {
		if a.FloorID != "" {
			areaToFloor[a.AreaID] = a.FloorID
		}
	}
	return &membershipResolver{entitiesByID: entities, devicesByID: devices, areaToFloor: areaToFloor}, nil
}

// entityArea returns an entity's effective area, inheriting from its
// device when the entity has no area of its own.
func (m *membershipResolver) entityArea(e *homeassistant.EntityRegistryEntry) string {
	if e.AreaID != "" {
		return e.AreaID
	}
	if e.DeviceID != "" {
		if d := m.devicesByID[e.DeviceID]; d != nil {
			return d.AreaID
		}
	}
	return ""
}

// entityHasLabel reports whether an entity carries a label directly or
// through its device.
func (m *membershipResolver) entityHasLabel(e *homeassistant.EntityRegistryEntry, labelID string) bool {
	for _, l := range e.Labels {
		if l == labelID {
			return true
		}
	}
	if e.DeviceID != "" {
		if d := m.devicesByID[e.DeviceID]; d != nil {
			for _, l := range d.Labels {
				if l == labelID {
					return true
				}
			}
		}
	}
	return false
}

// members returns the sorted entity ids belonging to a registry target.
func (m *membershipResolver) members(target SubscriptionTarget) []string {
	var out []string
	for id, e := range m.entitiesByID {
		var match bool
		switch target.Kind {
		case TargetArea:
			match = m.entityArea(e) == target.Value
		case TargetLabel:
			match = m.entityHasLabel(e, target.Value)
		case TargetFloor:
			match = m.areaToFloor[m.entityArea(e)] == target.Value && target.Value != ""
		}
		if match {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

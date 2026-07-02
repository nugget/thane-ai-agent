package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

const haCallServiceTruncationNote = "Result exceeded the tool byte cap; the call succeeded — changed-entity listing was clipped."

// maxHACallServiceChangedIDs bounds the changed-entity listing in the
// call response; the count is always exact even when the list is capped.
const maxHACallServiceChangedIDs = 20

// haCallServiceResult reports what a service call did: the resolved
// addressing it ran with and the states HA says changed. A zero
// changed_count on a target call is not an error — entities may already
// be in the requested state — but it is the signal to look, so the note
// says exactly that.
type haCallServiceResult struct {
	Called       string         `json:"called"`
	EntityID     string         `json:"entity_id,omitempty"`
	Target       map[string]any `json:"target,omitempty"`
	ChangedCount int            `json:"changed_count"`
	Changed      []string       `json:"changed,omitempty"`
	Truncated    bool           `json:"truncated,omitempty"`
	Note         string         `json:"note,omitempty"`
}

// haTargetKeys are the service-call target selectors HA accepts, in
// the order the resolver processes them.
var haTargetKeys = []string{"entity_id", "device_id", "area_id", "floor_id", "label_id"}

// resolveServiceTarget validates and resolves a target block. IDs pass
// through; for areas, floors, labels, and devices a human name (or area
// alias) resolves to its ID case-insensitively, because the model often
// holds the name ("Office") before the ID ("office"). Unknown
// references fail fast with the known names — HA itself silently
// no-ops an unknown area_id, which is the phantom-success failure mode
// this tool exists to prevent.
func (r *Registry) resolveServiceTarget(ctx context.Context, raw map[string]any) (map[string]any, error) {
	resolved := make(map[string]any, len(raw))
	for key := range raw {
		if !slicesContains(haTargetKeys, key) {
			return nil, fmt.Errorf("unknown target key %q; valid keys: %s", key, strings.Join(haTargetKeys, ", "))
		}
	}

	for _, key := range haTargetKeys {
		rawVal, ok := raw[key]
		if !ok {
			continue
		}
		values, err := stringOrList(rawVal)
		if err != nil {
			return nil, fmt.Errorf("target.%s: %w", key, err)
		}
		if len(values) == 0 {
			continue
		}
		out := make([]string, 0, len(values))
		for _, v := range values {
			id, err := r.resolveTargetValue(ctx, key, v)
			if err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		if len(out) == 1 {
			resolved[key] = out[0]
		} else {
			resolved[key] = out
		}
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("target must set at least one of: %s", strings.Join(haTargetKeys, ", "))
	}
	return resolved, nil
}

func (r *Registry) resolveTargetValue(ctx context.Context, key, value string) (string, error) {
	switch key {
	case "entity_id":
		// Same phantom-success guard as the single-entity path: HA
		// accepts unknown entity ids and silently no-ops.
		if _, err := r.ha.GetState(ctx, value); err != nil {
			if IsHAEntityNotFound(err) {
				return "", fmt.Errorf("target entity %q not found: %s", value, SuggestEntityNotFound(ctx, r.ha, value))
			}
			return "", fmt.Errorf("verify target entity %q: %w", value, err)
		}
		return value, nil
	case "area_id":
		areas, err := r.ha.GetAreas(ctx)
		if err != nil {
			return "", fmt.Errorf("resolve area %q: %w", value, err)
		}
		names := make([]string, 0, len(areas))
		for _, a := range areas {
			if a.AreaID == value {
				return value, nil
			}
			names = append(names, a.Name)
		}
		lower := strings.ToLower(value)
		for _, a := range areas {
			if strings.ToLower(a.Name) == lower {
				return a.AreaID, nil
			}
			for _, alias := range a.Aliases {
				if strings.ToLower(alias) == lower {
					return a.AreaID, nil
				}
			}
		}
		return "", fmt.Errorf("unknown area %q; known areas: %s", value, joinCapped(names, 15))
	case "floor_id":
		floors, err := r.ha.GetFloorRegistry(ctx)
		if err != nil {
			return "", fmt.Errorf("resolve floor %q: %w", value, err)
		}
		names := make([]string, 0, len(floors))
		for _, f := range floors {
			if f.FloorID == value {
				return value, nil
			}
			names = append(names, f.Name)
		}
		lower := strings.ToLower(value)
		for _, f := range floors {
			if strings.ToLower(f.Name) == lower {
				return f.FloorID, nil
			}
			for _, alias := range f.Aliases {
				if strings.ToLower(alias) == lower {
					return f.FloorID, nil
				}
			}
		}
		return "", fmt.Errorf("unknown floor %q; known floors: %s", value, joinCapped(names, 15))
	case "label_id":
		labels, err := r.ha.GetLabelRegistry(ctx)
		if err != nil {
			return "", fmt.Errorf("resolve label %q: %w", value, err)
		}
		names := make([]string, 0, len(labels))
		for _, l := range labels {
			if l.LabelID == value {
				return value, nil
			}
			names = append(names, l.Name)
		}
		lower := strings.ToLower(value)
		for _, l := range labels {
			if strings.ToLower(l.Name) == lower {
				return l.LabelID, nil
			}
		}
		return "", fmt.Errorf("unknown label %q; known labels: %s", value, joinCapped(names, 15))
	case "device_id":
		devices, err := r.ha.GetDeviceRegistry(ctx)
		if err != nil {
			return "", fmt.Errorf("resolve device %q: %w", value, err)
		}
		names := make([]string, 0, len(devices))
		for _, d := range devices {
			if d.ID == value {
				return value, nil
			}
			names = append(names, string(d.Name))
		}
		lower := strings.ToLower(value)
		for _, d := range devices {
			if strings.ToLower(string(d.Name)) == lower {
				return d.ID, nil
			}
		}
		return "", fmt.Errorf("unknown device %q; known devices: %s", value, joinCapped(names, 15))
	}
	return value, nil
}

func haCallServiceResponse(domain, service, entityID string, target map[string]any, changed []homeassistant.State) string {
	ids := make([]string, 0, len(changed))
	for _, st := range changed {
		ids = append(ids, st.EntityID)
	}
	sort.Strings(ids)
	out := haCallServiceResult{
		Called:       domain + "." + service,
		EntityID:     entityID,
		Target:       target,
		ChangedCount: len(ids),
	}
	if len(ids) > maxHACallServiceChangedIDs {
		out.Changed = ids[:maxHACallServiceChangedIDs]
		out.Truncated = true
	} else {
		out.Changed = ids
	}
	if len(ids) == 0 {
		if target != nil {
			out.Note = "HA reports no state changes. Entities may already be in the requested state — or the target matched nothing (hidden entities are skipped in area/floor/label targets)."
		} else {
			out.Note = "HA reports no state changes; the entity may already be in the requested state."
		}
	}
	return toIndentedJSONWithTruncationNote(out, haCallServiceTruncationNote)
}

func stringOrList(v any) ([]string, error) {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, nil
		}
		return []string{strings.TrimSpace(t)}, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("expected strings, got %T", item)
			}
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a string or array of strings, got %T", v)
	}
}

func joinCapped(names []string, limit int) string {
	sort.Strings(names)
	if len(names) > limit {
		return strings.Join(names[:limit], ", ") + fmt.Sprintf(", … (%d total)", len(names))
	}
	return strings.Join(names, ", ")
}

func slicesContains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

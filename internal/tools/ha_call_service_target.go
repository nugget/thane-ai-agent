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

// targetResolution is resolveServiceTarget's outcome. When Suggestion
// is non-empty the resolution stopped on an unknown target entity and
// Suggestion carries the same recoverable did-you-mean envelope the
// single-entity path returns — the handler returns it as the tool
// result (not an error), keeping typo recovery consistent across both
// addressing forms.
type targetResolution struct {
	Resolved   map[string]any
	Suggestion string
}

// resolveServiceTarget validates and resolves a target block. IDs pass
// through; for areas, floors, labels, and devices a human name (or
// area/floor alias) resolves to its ID case-insensitively, because the
// model often holds the name ("Office") before the ID ("office").
// Unknown references fail fast with the known names — HA itself
// silently no-ops an unknown area_id, which is the phantom-success
// failure mode this tool exists to prevent. Each registry is fetched
// once per call (and served from the client's TTL cache besides), no
// matter how many values a key carries.
func (r *Registry) resolveServiceTarget(ctx context.Context, raw map[string]any) (targetResolution, error) {
	for key := range raw {
		if !slicesContains(haTargetKeys, key) {
			return targetResolution{}, fmt.Errorf("unknown target key %q; valid keys: %s", key, strings.Join(haTargetKeys, ", "))
		}
	}

	resolved := make(map[string]any, len(raw))
	for _, key := range haTargetKeys {
		rawVal, ok := raw[key]
		if !ok {
			continue
		}
		values, err := stringOrList(rawVal)
		if err != nil {
			return targetResolution{}, fmt.Errorf("target.%s: %w", key, err)
		}
		if len(values) == 0 {
			continue
		}

		var out []string
		switch key {
		case "entity_id":
			for _, v := range values {
				// Same phantom-success guard as the single-entity
				// path, with the same recoverable outcome: HA accepts
				// unknown entity ids and silently no-ops.
				if _, err := r.ha.GetState(ctx, v); err != nil {
					if IsHAEntityNotFound(err) {
						return targetResolution{Suggestion: SuggestEntityNotFound(ctx, r.ha, v)}, nil
					}
					return targetResolution{}, fmt.Errorf("verify target entity %q: %w", v, err)
				}
				out = append(out, v)
			}
		default:
			out, err = r.resolveRegistryTargets(ctx, key, values)
			if err != nil {
				return targetResolution{}, err
			}
		}

		if len(out) == 1 {
			resolved[key] = out[0]
		} else {
			resolved[key] = out
		}
	}
	if len(resolved) == 0 {
		return targetResolution{}, fmt.Errorf("target must set at least one of: %s", strings.Join(haTargetKeys, ", "))
	}
	return targetResolution{Resolved: resolved}, nil
}

// registryTargetEntry is one resolvable row of a registry: its ID plus
// the names and aliases a caller may reference it by.
type registryTargetEntry struct {
	id      string
	matches []string
}

// resolveRegistryTargets resolves every value for one registry-backed
// target key against a single registry fetch.
func (r *Registry) resolveRegistryTargets(ctx context.Context, key string, values []string) ([]string, error) {
	entries, kind, err := r.registryTargetEntries(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("resolve %s %q: %w", kind, values[0], err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if len(e.matches) > 0 {
			names = append(names, e.matches[0])
		}
	}

	out := make([]string, 0, len(values))
	for _, v := range values {
		id, ok := matchRegistryTarget(entries, v)
		if !ok {
			return nil, fmt.Errorf("unknown %s %q; known %ss: %s", kind, v, kind, joinCapped(names, 15))
		}
		out = append(out, id)
	}
	return out, nil
}

func matchRegistryTarget(entries []registryTargetEntry, value string) (string, bool) {
	for _, e := range entries {
		if e.id == value {
			return e.id, true
		}
	}
	lower := strings.ToLower(value)
	for _, e := range entries {
		for _, m := range e.matches {
			if strings.ToLower(m) == lower {
				return e.id, true
			}
		}
	}
	return "", false
}

func (r *Registry) registryTargetEntries(ctx context.Context, key string) ([]registryTargetEntry, string, error) {
	switch key {
	case "area_id":
		areas, err := r.ha.GetAreas(ctx)
		if err != nil {
			return nil, "area", err
		}
		entries := make([]registryTargetEntry, 0, len(areas))
		for _, a := range areas {
			entries = append(entries, registryTargetEntry{id: a.AreaID, matches: append([]string{a.Name}, a.Aliases...)})
		}
		return entries, "area", nil
	case "floor_id":
		floors, err := r.ha.GetFloorRegistry(ctx)
		if err != nil {
			return nil, "floor", err
		}
		entries := make([]registryTargetEntry, 0, len(floors))
		for _, f := range floors {
			entries = append(entries, registryTargetEntry{id: f.FloorID, matches: append([]string{f.Name}, f.Aliases...)})
		}
		return entries, "floor", nil
	case "label_id":
		labels, err := r.ha.GetLabelRegistry(ctx)
		if err != nil {
			return nil, "label", err
		}
		entries := make([]registryTargetEntry, 0, len(labels))
		for _, l := range labels {
			entries = append(entries, registryTargetEntry{id: l.LabelID, matches: []string{l.Name}})
		}
		return entries, "label", nil
	case "device_id":
		devices, err := r.ha.GetDeviceRegistry(ctx)
		if err != nil {
			return nil, "device", err
		}
		entries := make([]registryTargetEntry, 0, len(devices))
		for _, d := range devices {
			entries = append(entries, registryTargetEntry{id: d.ID, matches: []string{string(d.Name)}})
		}
		return entries, "device", nil
	}
	return nil, key, fmt.Errorf("unsupported registry target key %q", key)
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

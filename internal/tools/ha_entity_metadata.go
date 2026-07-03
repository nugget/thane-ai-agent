package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

const maxHAListEntitiesLimit = 100

const haListEntitiesTruncationNote = "Result exceeded the tool byte cap; reduce limit, narrow the domain, or omit include metadata for a smaller payload."

type haEntityMetadataBundle struct {
	include  homeassistant.EntityMetadataIncludes
	entries  map[string]*homeassistant.EntityRegistryEntry
	resolver homeassistant.EntityMetadataResolver
}

type haListEntitiesResult struct {
	Domain    string `json:"domain,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
	Count     int    `json:"count"`
	Total     int    `json:"total"`
	Truncated bool   `json:"truncated,omitempty"`
	// HiddenExcluded counts entities dropped from this result because
	// the operator hid them (registry hidden_by). It advertises their
	// existence so the model knows more is inspectable via include_hidden
	// or an explicit read — the count is never silently zero-filled.
	HiddenExcluded int                `json:"hidden_excluded,omitempty"`
	Items          []haListEntityItem `json:"items"`
}

type haListEntityItem struct {
	EntityID     string `json:"entity_id"`
	FriendlyName string `json:"friendly_name,omitempty"`
	State        string `json:"state"`
	Since        string `json:"since,omitempty"`
	Updated      string `json:"updated,omitempty"`
	// Hidden is set when the operator has hidden this entity from HA's
	// generated surfaces (registry hidden_by). It is only populated on
	// a result that surfaced a hidden entity via include_hidden, so the
	// model can see the entity is off the default view — never silently
	// blended in with visible ones.
	Hidden   bool                          `json:"hidden,omitempty"`
	Metadata *homeassistant.EntityMetadata `json:"metadata,omitempty"`
}

// isEntityHidden reports whether a registry entry marks the entity
// hidden (hidden_by set). A nil entry — an entity with no registry row —
// is treated as visible: hidden is an explicit operator action, not a
// default.
func isEntityHidden(entry *homeassistant.EntityRegistryEntry) bool {
	return entry != nil && entry.HiddenBy != ""
}

// filterHiddenStates partitions candidate states by the operator's HA
// Visible flag, mirroring HA's own generated-surface behavior. When
// includeHidden is false, hidden entities are dropped and counted — the
// count is advertised so their existence is never silent, and the model
// can reach for an explicit read (ha_get_state, ha_device) to see them.
// When true, all pass and the caller marks the hidden ones. entries is
// the registry snapshot keyed by entity_id; a nil map (registry
// unavailable) leaves every candidate visible rather than hiding the
// whole world on a transient failure.
func filterHiddenStates(candidates []homeassistant.State, entries map[string]*homeassistant.EntityRegistryEntry, includeHidden bool) (kept []homeassistant.State, hiddenCount int) {
	if includeHidden || entries == nil {
		return candidates, 0
	}
	kept = candidates[:0]
	for _, s := range candidates {
		if isEntityHidden(entries[s.EntityID]) {
			hiddenCount++
			continue
		}
		kept = append(kept, s)
	}
	return kept, hiddenCount
}

// entityRegistryByID fetches the full entity registry as a lookup map,
// for visibility filtering independent of any metadata projection. The
// registry is TTL-cached, so this shares the fetch with a metadata
// bundle built in the same turn.
func entityRegistryByID(ctx context.Context, ha *homeassistant.Client) (map[string]*homeassistant.EntityRegistryEntry, error) {
	if ha == nil {
		return nil, nil
	}
	entries, err := ha.GetEntityRegistry(ctx)
	if err != nil {
		return nil, fmt.Errorf("get entity registry: %w", err)
	}
	byID := make(map[string]*homeassistant.EntityRegistryEntry, len(entries))
	for i := range entries {
		byID[entries[i].EntityID] = &entries[i]
	}
	return byID, nil
}

// haRecencyDelta returns the delta-formatted recency signals for an entity
// state, mirroring the canonical entity contextfmt projection
// (contextfmt/entity_format.go) so ha_search_states and ha_list_entities carry
// the same "how fresh is this?" signal the always-on context does. since is the
// time since the last state change; updated is set only when the last attribute
// update meaningfully post-dates that change.
func haRecencyDelta(s homeassistant.State, now time.Time) (since, updated string) {
	// Without a last-changed timestamp there is no meaningful recency signal:
	// return neither field rather than deriving a nonsense delta against the
	// zero time — which would also let `updated` populate while `since` stays
	// empty, since LastUpdated.Sub(zero) is an enormous duration.
	if s.LastChanged.IsZero() {
		return "", ""
	}
	since = promptfmt.FormatDeltaOnly(s.LastChanged, now)
	if !s.LastUpdated.IsZero() && s.LastUpdated.Sub(s.LastChanged) > time.Second {
		updated = promptfmt.FormatDeltaOnly(s.LastUpdated, now)
	}
	return since, updated
}

// EntityMetadataIncludeParameter returns the shared JSON schema
// fragment used by native HA tools and entity subscription surfaces.
func EntityMetadataIncludeParameter() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Optional HA registry metadata to include with each entity. Use all=true for every supported field, or enable area/device/labels/description/visibility individually.",
		"properties": map[string]any{
			"all": map[string]any{
				"type":        "boolean",
				"description": "Include every currently supported HA metadata projection.",
			},
			"area": map[string]any{
				"type":        "boolean",
				"description": "Include resolved area/floor/building context.",
			},
			"device": map[string]any{
				"type":        "boolean",
				"description": "Include owning device registry context.",
			},
			"labels": map[string]any{
				"type":        "boolean",
				"description": "Include resolved labels from the entity, device, and area.",
			},
			"description": map[string]any{
				"type":        "boolean",
				"description": "Include human-facing entity names, aliases, icon, device class, platform, category, translation key, and description when HA provides them.",
			},
			"visibility": map[string]any{
				"type":        "boolean",
				"description": "Include HA registry visibility and enabled state, including hidden_by/disabled_by and a context_role salience hint for default, hidden, diagnostic, config, or disabled entities.",
			},
		},
	}
}

// ParseEntityMetadataIncludesArg decodes an include object from a
// model-facing tool argument.
func ParseEntityMetadataIncludesArg(raw any, fieldName string) (homeassistant.EntityMetadataIncludes, error) {
	if raw == nil {
		return homeassistant.EntityMetadataIncludes{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return homeassistant.EntityMetadataIncludes{}, fmt.Errorf("%s must be an object with boolean metadata flags", fieldName)
	}
	if raw, ok := obj["all"]; ok && raw != nil {
		all, ok := raw.(bool)
		if !ok {
			return homeassistant.EntityMetadataIncludes{}, fmt.Errorf("%s.all must be boolean", fieldName)
		}
		if all {
			return homeassistant.AllEntityMetadataIncludes(), nil
		}
	}
	var include homeassistant.EntityMetadataIncludes
	var err error
	if include.Area, err = metadataIncludeFlag(obj, "area", fieldName); err != nil {
		return homeassistant.EntityMetadataIncludes{}, err
	}
	if include.Device, err = metadataIncludeFlag(obj, "device", fieldName); err != nil {
		return homeassistant.EntityMetadataIncludes{}, err
	}
	if include.Labels, err = metadataIncludeFlag(obj, "labels", fieldName); err != nil {
		return homeassistant.EntityMetadataIncludes{}, err
	}
	if include.Description, err = metadataIncludeFlag(obj, "description", fieldName); err != nil {
		return homeassistant.EntityMetadataIncludes{}, err
	}
	if include.Visibility, err = metadataIncludeFlag(obj, "visibility", fieldName); err != nil {
		return homeassistant.EntityMetadataIncludes{}, err
	}
	return include, nil
}

func metadataIncludeFlag(obj map[string]any, key, fieldName string) (bool, error) {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return false, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s.%s must be boolean", fieldName, key)
	}
	return b, nil
}

// EntityMetadataIncludesPointer returns nil for an empty include set
// and a copy pointer otherwise. It keeps optional subscription fields
// out of persisted JSON when no metadata was requested.
func EntityMetadataIncludesPointer(include homeassistant.EntityMetadataIncludes) *homeassistant.EntityMetadataIncludes {
	return (&include).Clone()
}

func fetchHAEntityMetadataBundle(ctx context.Context, ha *homeassistant.Client, include homeassistant.EntityMetadataIncludes) (*haEntityMetadataBundle, error) {
	if ha == nil || !include.Any() {
		return nil, nil
	}

	entries, err := ha.GetEntityRegistry(ctx)
	if err != nil {
		return nil, fmt.Errorf("get entity registry: %w", err)
	}
	return newHAEntityMetadataBundle(ctx, ha, include, entries)
}

func fetchHAEntityMetadataBundleForEntityIDs(ctx context.Context, ha *homeassistant.Client, include homeassistant.EntityMetadataIncludes, entityIDs []string) (*haEntityMetadataBundle, error) {
	if ha == nil || !include.Any() {
		return nil, nil
	}
	entityIDs = uniqueEntityIDs(entityIDs)
	if len(entityIDs) == 0 {
		return newHAEntityMetadataBundle(ctx, ha, include, nil)
	}

	entries := make([]homeassistant.EntityRegistryEntry, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		entry, err := ha.GetEntityRegistryEntry(ctx, entityID)
		if err != nil {
			if isEntityRegistryNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("get entity registry entry %s: %w", entityID, err)
		}
		if entry == nil {
			continue
		}
		entries = append(entries, *entry)
	}
	return newHAEntityMetadataBundle(ctx, ha, include, entries)
}

func uniqueEntityIDs(entityIDs []string) []string {
	seen := make(map[string]struct{}, len(entityIDs))
	out := make([]string, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		if entityID == "" {
			continue
		}
		if _, ok := seen[entityID]; ok {
			continue
		}
		seen[entityID] = struct{}{}
		out = append(out, entityID)
	}
	return out
}

func newHAEntityMetadataBundle(ctx context.Context, ha *homeassistant.Client, include homeassistant.EntityMetadataIncludes, entries []homeassistant.EntityRegistryEntry) (*haEntityMetadataBundle, error) {
	entryMap := make(map[string]*homeassistant.EntityRegistryEntry, len(entries))
	for i := range entries {
		entryMap[entries[i].EntityID] = &entries[i]
	}

	var areas []homeassistant.Area
	var err error
	hasRegistryEntries := len(entries) > 0
	if hasRegistryEntries && (include.Area || include.Labels || include.Device) {
		areas, err = ha.GetAreas(ctx)
		if err != nil {
			return nil, fmt.Errorf("get areas: %w", err)
		}
	}

	var floors []homeassistant.FloorRegistryEntry
	if hasRegistryEntries && include.Area {
		floors, err = ha.GetFloorRegistry(ctx)
		if err != nil {
			return nil, fmt.Errorf("get floors: %w", err)
		}
	}

	var labels []homeassistant.LabelRegistryEntry
	if hasRegistryEntries && include.Labels {
		labels, err = ha.GetLabelRegistry(ctx)
		if err != nil {
			return nil, fmt.Errorf("get labels: %w", err)
		}
	}

	var devices []homeassistant.DeviceRegistryEntry
	if hasRegistryEntries && (include.Device || include.Area || include.Labels) {
		devices, err = ha.GetDeviceRegistry(ctx)
		if err != nil {
			return nil, fmt.Errorf("get devices: %w", err)
		}
	}

	resolver := homeassistant.NewEntityMetadataResolverWithFloorAlias(areas, labels, devices, floors, ha.FloorMetadataAlias())
	return &haEntityMetadataBundle{
		include:  include,
		entries:  entryMap,
		resolver: resolver,
	}, nil
}

func (b *haEntityMetadataBundle) metadata(entityID string, state *homeassistant.State) *homeassistant.EntityMetadata {
	if b == nil || !b.include.Any() {
		return nil
	}
	return b.resolver.MetadataForEntity(b.entries[entityID], state, b.include)
}

func (b *haEntityMetadataBundle) metadataFromInfo(info homeassistant.EntityInfo) *homeassistant.EntityMetadata {
	if b == nil || !b.include.Any() {
		return nil
	}
	state := &homeassistant.State{
		EntityID: info.EntityID,
		State:    info.State,
		Attributes: map[string]any{
			"friendly_name": info.FriendlyName,
		},
	}
	return b.metadata(info.EntityID, state)
}

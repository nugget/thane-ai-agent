package tools

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

const maxHAListEntitiesLimit = 100

const haListEntitiesTruncationNote = "Result exceeded the tool byte cap; reduce limit, narrow the domain, or omit include metadata for a smaller payload."

type haEntityMetadataBundle struct {
	include  homeassistant.EntityMetadataIncludes
	entries  map[string]*homeassistant.EntityRegistryEntry
	resolver homeassistant.EntityMetadataResolver
}

type haListEntitiesResult struct {
	Domain    string             `json:"domain"`
	Count     int                `json:"count"`
	Total     int                `json:"total"`
	Truncated bool               `json:"truncated,omitempty"`
	Items     []haListEntityItem `json:"items"`
}

type haListEntityItem struct {
	EntityID     string                        `json:"entity_id"`
	FriendlyName string                        `json:"friendly_name,omitempty"`
	State        string                        `json:"state"`
	Metadata     *homeassistant.EntityMetadata `json:"metadata,omitempty"`
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
				"description": "Include human-facing entity names, aliases, icon, device class, platform, category, and description when HA provides them.",
			},
			"visibility": map[string]any{
				"type":        "boolean",
				"description": "Include HA registry visibility and enabled state, including hidden_by/disabled_by and whether the entity is default-dashboard material.",
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
	entryMap := make(map[string]*homeassistant.EntityRegistryEntry, len(entries))
	for i := range entries {
		entryMap[entries[i].EntityID] = &entries[i]
	}

	var areas []homeassistant.Area
	if include.Area || include.Labels || include.Device {
		areas, err = ha.GetAreas(ctx)
		if err != nil {
			return nil, fmt.Errorf("get areas: %w", err)
		}
	}

	var floors []homeassistant.FloorRegistryEntry
	if include.Area {
		floors, err = ha.GetFloorRegistry(ctx)
		if err != nil {
			return nil, fmt.Errorf("get floors: %w", err)
		}
	}

	var labels []homeassistant.LabelRegistryEntry
	if include.Labels {
		labels, err = ha.GetLabelRegistry(ctx)
		if err != nil {
			return nil, fmt.Errorf("get labels: %w", err)
		}
	}

	var devices []homeassistant.DeviceRegistryEntry
	if include.Device || include.Area || include.Labels {
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

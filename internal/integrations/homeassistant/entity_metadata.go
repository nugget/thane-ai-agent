package homeassistant

import "sort"

// EntityMetadataIncludes declares which Home Assistant registry
// relationships should be included with an entity representation.
// Zero value means "state only."
type EntityMetadataIncludes struct {
	Area        bool `yaml:"area,omitempty" json:"area,omitempty"`
	Device      bool `yaml:"device,omitempty" json:"device,omitempty"`
	Labels      bool `yaml:"labels,omitempty" json:"labels,omitempty"`
	Description bool `yaml:"description,omitempty" json:"description,omitempty"`
}

// Any reports whether at least one metadata relationship is requested.
func (i EntityMetadataIncludes) Any() bool {
	return i.Area || i.Device || i.Labels || i.Description
}

// Clone returns a detached copy of the include flags, or nil when no
// metadata relationships are requested.
func (i *EntityMetadataIncludes) Clone() *EntityMetadataIncludes {
	if i == nil || !i.Any() {
		return nil
	}
	cp := *i
	return &cp
}

// AllEntityMetadataIncludes returns the full currently-supported
// entity metadata projection.
func AllEntityMetadataIncludes() EntityMetadataIncludes {
	return EntityMetadataIncludes{
		Area:        true,
		Device:      true,
		Labels:      true,
		Description: true,
	}
}

// EntityMetadata is the model-facing registry context attached to a
// Home Assistant entity state or search result.
type EntityMetadata struct {
	FriendlyName   string                `json:"friendly_name,omitempty"`
	Name           string                `json:"name,omitempty"`
	OriginalName   string                `json:"original_name,omitempty"`
	Aliases        []string              `json:"aliases,omitempty"`
	Description    string                `json:"description,omitempty"`
	Icon           string                `json:"icon,omitempty"`
	EntityCategory string                `json:"entity_category,omitempty"`
	Platform       string                `json:"platform,omitempty"`
	DeviceClass    string                `json:"device_class,omitempty"`
	Area           *EntityAreaMetadata   `json:"area,omitempty"`
	Device         *EntityDeviceMetadata `json:"device,omitempty"`
	Labels         []EntityLabelMetadata `json:"labels,omitempty"`
	Capabilities   map[string]any        `json:"capabilities,omitempty"`
	Options        map[string]any        `json:"options,omitempty"`
	Categories     map[string]string     `json:"categories,omitempty"`
}

// Empty reports whether the metadata object carries no projected fields.
func (m *EntityMetadata) Empty() bool {
	if m == nil {
		return true
	}
	return m.FriendlyName == "" &&
		m.Name == "" &&
		m.OriginalName == "" &&
		len(m.Aliases) == 0 &&
		m.Description == "" &&
		m.Icon == "" &&
		m.EntityCategory == "" &&
		m.Platform == "" &&
		m.DeviceClass == "" &&
		m.Area == nil &&
		m.Device == nil &&
		len(m.Labels) == 0 &&
		len(m.Capabilities) == 0 &&
		len(m.Options) == 0 &&
		len(m.Categories) == 0
}

// EntityAreaMetadata is the area/floor context Home Assistant knows
// for an entity, resolved through direct entity area first and device
// area second.
type EntityAreaMetadata struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name,omitempty"`
	Aliases             []string `json:"aliases,omitempty"`
	FloorID             string   `json:"floor_id,omitempty"`
	Icon                string   `json:"icon,omitempty"`
	TemperatureEntityID string   `json:"temperature_entity_id,omitempty"`
	HumidityEntityID    string   `json:"humidity_entity_id,omitempty"`
}

// EntityDeviceMetadata is the device registry context for an entity.
type EntityDeviceMetadata struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	NameByUser       string `json:"name_by_user,omitempty"`
	Manufacturer     string `json:"manufacturer,omitempty"`
	Model            string `json:"model,omitempty"`
	ModelID          string `json:"model_id,omitempty"`
	SWVersion        string `json:"sw_version,omitempty"`
	HWVersion        string `json:"hw_version,omitempty"`
	SerialNumber     string `json:"serial_number,omitempty"`
	AreaID           string `json:"area_id,omitempty"`
	AreaName         string `json:"area_name,omitempty"`
	ViaDeviceID      string `json:"via_device_id,omitempty"`
	EntryType        string `json:"entry_type,omitempty"`
	DisabledBy       string `json:"disabled_by,omitempty"`
	ConfigurationURL string `json:"configuration_url,omitempty"`
}

// EntityLabelMetadata is one resolved HA label with its source
// relationship to the entity.
type EntityLabelMetadata struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Color       string   `json:"color,omitempty"`
	Sources     []string `json:"sources,omitempty"`
}

// EntityMetadataResolver joins HA area, label, and device registries
// into model-facing entity metadata.
type EntityMetadataResolver struct {
	areasByID   map[string]*Area
	labelsByID  map[string]*LabelRegistryEntry
	devicesByID map[string]*DeviceRegistryEntry
}

// NewEntityMetadataResolver builds a resolver for a registry snapshot.
func NewEntityMetadataResolver(areas []Area, labels []LabelRegistryEntry, devices []DeviceRegistryEntry) EntityMetadataResolver {
	r := EntityMetadataResolver{
		areasByID:   make(map[string]*Area, len(areas)),
		labelsByID:  make(map[string]*LabelRegistryEntry, len(labels)),
		devicesByID: make(map[string]*DeviceRegistryEntry, len(devices)),
	}
	for i := range areas {
		r.areasByID[areas[i].AreaID] = &areas[i]
	}
	for i := range labels {
		r.labelsByID[labels[i].LabelID] = &labels[i]
	}
	for i := range devices {
		r.devicesByID[devices[i].ID] = &devices[i]
	}
	return r
}

// MetadataForEntity projects the requested registry metadata for one
// entity. It returns nil when no requested metadata is available.
func (r EntityMetadataResolver) MetadataForEntity(entry *EntityRegistryEntry, state *State, include EntityMetadataIncludes) *EntityMetadata {
	if !include.Any() {
		return nil
	}
	meta := &EntityMetadata{}
	device := r.deviceForEntity(entry)
	area := r.areaForEntity(entry, device)

	if include.Description {
		applyEntityDescription(meta, entry, state)
	}
	if include.Area && area != nil {
		meta.Area = areaMetadata(area)
	} else if include.Area {
		if areaID := entityAreaID(entry, device); areaID != "" {
			meta.Area = &EntityAreaMetadata{ID: areaID}
		}
	}
	if include.Device && device != nil {
		meta.Device = r.deviceMetadata(device)
	} else if include.Device && entry != nil && entry.DeviceID != "" {
		meta.Device = &EntityDeviceMetadata{ID: entry.DeviceID}
	}
	if include.Labels {
		meta.Labels = r.labelsForEntity(entry, device, area)
	}

	if meta.Empty() {
		return nil
	}
	return meta
}

func entityAreaID(entry *EntityRegistryEntry, device *DeviceRegistryEntry) string {
	if entry != nil && entry.AreaID != "" {
		return entry.AreaID
	}
	if device != nil {
		return device.AreaID
	}
	return ""
}

func applyEntityDescription(meta *EntityMetadata, entry *EntityRegistryEntry, state *State) {
	if state != nil {
		if friendly, ok := state.Attributes["friendly_name"].(string); ok {
			meta.FriendlyName = friendly
		}
		if desc, ok := state.Attributes["description"].(string); ok {
			meta.Description = desc
		}
		if dc, ok := state.Attributes["device_class"].(string); ok {
			meta.DeviceClass = dc
		}
	}
	if entry == nil {
		return
	}
	meta.Name = entry.Name
	meta.OriginalName = entry.OriginalName
	meta.Aliases = append([]string(nil), entry.Aliases...)
	if entry.Description != "" {
		meta.Description = entry.Description
	}
	if entry.Icon != "" {
		meta.Icon = entry.Icon
	} else {
		meta.Icon = entry.OriginalIcon
	}
	meta.EntityCategory = entry.EntityCategory
	meta.Platform = entry.Platform
	if entry.DeviceClass != "" {
		meta.DeviceClass = entry.DeviceClass
	} else if entry.OriginalDeviceClass != "" {
		meta.DeviceClass = entry.OriginalDeviceClass
	}
	if len(entry.Capabilities) > 0 {
		meta.Capabilities = cloneAnyMap(entry.Capabilities)
	}
	if len(entry.Options) > 0 {
		meta.Options = cloneAnyMap(entry.Options)
	}
	if len(entry.Categories) > 0 {
		meta.Categories = cloneStringMap(entry.Categories)
	}
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (r EntityMetadataResolver) deviceForEntity(entry *EntityRegistryEntry) *DeviceRegistryEntry {
	if entry == nil || entry.DeviceID == "" {
		return nil
	}
	return r.devicesByID[entry.DeviceID]
}

func (r EntityMetadataResolver) areaForEntity(entry *EntityRegistryEntry, device *DeviceRegistryEntry) *Area {
	areaID := ""
	if entry != nil {
		areaID = entry.AreaID
	}
	if areaID == "" && device != nil {
		areaID = device.AreaID
	}
	if areaID == "" {
		return nil
	}
	return r.areasByID[areaID]
}

func areaMetadata(area *Area) *EntityAreaMetadata {
	return &EntityAreaMetadata{
		ID:                  area.AreaID,
		Name:                area.Name,
		Aliases:             append([]string(nil), area.Aliases...),
		FloorID:             area.FloorID,
		Icon:                area.Icon,
		TemperatureEntityID: area.TemperatureEntityID,
		HumidityEntityID:    area.HumidityEntityID,
	}
}

func (r EntityMetadataResolver) deviceMetadata(device *DeviceRegistryEntry) *EntityDeviceMetadata {
	out := &EntityDeviceMetadata{
		ID:               device.ID,
		Name:             device.Name,
		NameByUser:       device.NameByUser,
		Manufacturer:     device.Manufacturer,
		Model:            device.Model,
		ModelID:          device.ModelID,
		SWVersion:        device.SWVersion,
		HWVersion:        device.HWVersion,
		SerialNumber:     device.SerialNumber,
		AreaID:           device.AreaID,
		ViaDeviceID:      device.ViaDeviceID,
		EntryType:        device.EntryType,
		DisabledBy:       device.DisabledBy,
		ConfigurationURL: device.ConfigurationURL,
	}
	if area := r.areasByID[device.AreaID]; area != nil {
		out.AreaName = area.Name
	}
	return out
}

func (r EntityMetadataResolver) labelsForEntity(entry *EntityRegistryEntry, device *DeviceRegistryEntry, area *Area) []EntityLabelMetadata {
	byID := make(map[string]*EntityLabelMetadata)
	add := func(ids []string, source string) {
		for _, id := range ids {
			if id == "" {
				continue
			}
			label := byID[id]
			if label == nil {
				label = &EntityLabelMetadata{ID: id}
				if registry := r.labelsByID[id]; registry != nil {
					label.Name = registry.Name
					label.Description = registry.Description
					label.Icon = registry.Icon
					label.Color = registry.Color
				}
				byID[id] = label
			}
			if !containsLabelSource(label.Sources, source) {
				label.Sources = append(label.Sources, source)
			}
		}
	}
	if entry != nil {
		add(entry.Labels, "entity")
	}
	if device != nil {
		add(device.Labels, "device")
	}
	if area != nil {
		add(area.Labels, "area")
	}
	if len(byID) == 0 {
		return nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]EntityLabelMetadata, 0, len(ids))
	for _, id := range ids {
		out = append(out, *byID[id])
	}
	return out
}

func containsLabelSource(sources []string, want string) bool {
	for _, source := range sources {
		if source == want {
			return true
		}
	}
	return false
}

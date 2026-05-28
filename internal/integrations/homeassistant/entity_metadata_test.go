package homeassistant

import "testing"

func TestEntityMetadataIncludesClone(t *testing.T) {
	t.Parallel()

	if got := (*EntityMetadataIncludes)(nil).Clone(); got != nil {
		t.Fatalf("nil Clone() = %#v, want nil", got)
	}
	if got := (&EntityMetadataIncludes{}).Clone(); got != nil {
		t.Fatalf("empty Clone() = %#v, want nil", got)
	}

	include := &EntityMetadataIncludes{Area: true, Labels: true, Visibility: true}
	got := include.Clone()
	if got == nil {
		t.Fatal("Clone() returned nil")
	}
	if got == include {
		t.Fatal("Clone() returned the original pointer")
	}
	if !got.Area || !got.Labels || !got.Visibility || got.Device || got.Description {
		t.Fatalf("Clone() = %#v, want copied flags", got)
	}
}

func TestEntityMetadataResolverJoinsPhysicalContext(t *testing.T) {
	t.Parallel()

	resolver := NewEntityMetadataResolver(
		[]Area{{
			AreaID:  "office",
			Name:    "Office",
			Aliases: []string{"work room"},
			FloorID: "building_a",
			Labels:  []string{"label_work"},
		}},
		[]LabelRegistryEntry{
			{LabelID: "label_work", Name: "Work"},
			{LabelID: "label_env", Name: "Environment", Description: "Ambient environmental signals"},
			{LabelID: "label_device", Name: "Device Health"},
		},
		[]DeviceRegistryEntry{{
			ID:           "device_1",
			NameByUser:   "Office Climate Hub",
			Manufacturer: "Acme",
			Model:        "Enviro",
			AreaID:       "office",
			Labels:       []string{"label_device"},
		}},
		FloorRegistryEntry{
			FloorID: "building_a",
			Name:    "Building A",
			Aliases: []string{"main building"},
		},
	)

	entry := &EntityRegistryEntry{
		EntityID:       "sensor.office_temperature",
		Name:           "Temperature",
		Description:    "Ambient office temperature",
		Aliases:        []string{"office temp"},
		AreaID:         "",
		DeviceID:       "device_1",
		Labels:         []string{"label_env"},
		Platform:       "zwave_js",
		DeviceClass:    "temperature",
		TranslationKey: "temperature",
		HasEntityName:  true,
	}
	state := &State{
		EntityID: "sensor.office_temperature",
		State:    "72.1",
		Attributes: map[string]any{
			"friendly_name": "Office Temperature",
		},
	}

	got := resolver.MetadataForEntity(entry, state, AllEntityMetadataIncludes())
	if got == nil {
		t.Fatal("MetadataForEntity returned nil")
	}
	if got.Description != "Ambient office temperature" {
		t.Errorf("Description = %q, want registry description", got.Description)
	}
	if got.FriendlyName != "Office Temperature" {
		t.Errorf("FriendlyName = %q, want state friendly name", got.FriendlyName)
	}
	if got.Area == nil || got.Area.ID != "office" || got.Area.Name != "Office" {
		t.Fatalf("Area = %#v, want resolved office area", got.Area)
	}
	if got.Area.Floor == nil || got.Area.Floor.ID != "building_a" || got.Area.Floor.Name != "Building A" {
		t.Fatalf("Floor = %#v, want resolved Building A floor", got.Area.Floor)
	}
	if got.Device == nil || got.Device.ID != "device_1" || got.Device.NameByUser != "Office Climate Hub" {
		t.Fatalf("Device = %#v, want resolved device", got.Device)
	}
	if got.TranslationKey != "temperature" || !got.HasEntityName {
		t.Fatalf("TranslationKey/HasEntityName = %q/%v, want temperature/true", got.TranslationKey, got.HasEntityName)
	}
	if got.Visibility == nil || !got.Visibility.Enabled || !got.Visibility.Visible {
		t.Fatalf("Visibility = %#v, want enabled and visible", got.Visibility)
	}
	if got.Visibility.ContextRole != "default" || !got.Visibility.DefaultContext {
		t.Fatalf("Visibility role/default = %q/%v, want default/true", got.Visibility.ContextRole, got.Visibility.DefaultContext)
	}

	wantLabels := map[string]string{
		"label_device": "device",
		"label_env":    "entity",
		"label_work":   "area",
	}
	if len(got.Labels) != len(wantLabels) {
		t.Fatalf("Labels = %#v, want %d labels", got.Labels, len(wantLabels))
	}
	for _, label := range got.Labels {
		wantSource := wantLabels[label.ID]
		if wantSource == "" {
			t.Fatalf("unexpected label %#v", label)
		}
		if len(label.Sources) != 1 || label.Sources[0] != wantSource {
			t.Errorf("label %s sources = %v, want [%s]", label.ID, label.Sources, wantSource)
		}
	}
}

func TestEntityMetadataResolverProjectsVisibility(t *testing.T) {
	t.Parallel()

	resolver := NewEntityMetadataResolver(nil, nil, nil)
	got := resolver.MetadataForEntity(&EntityRegistryEntry{
		EntityID:       "sensor.switch_amperage",
		HiddenBy:       "user",
		DisabledBy:     "",
		EntityCategory: "diagnostic",
	}, nil, EntityMetadataIncludes{Visibility: true})
	if got == nil {
		t.Fatal("MetadataForEntity returned nil")
	}
	if got.Visibility == nil {
		t.Fatal("Visibility metadata missing")
	}
	if !got.Visibility.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if got.Visibility.Visible {
		t.Errorf("Visible = true, want false for hidden entity")
	}
	if got.Visibility.DefaultContext {
		t.Errorf("DefaultContext = true, want false for hidden entity")
	}
	if got.Visibility.ContextRole != "hidden" {
		t.Errorf("ContextRole = %q, want hidden", got.Visibility.ContextRole)
	}
	if got.Visibility.HiddenBy != "user" {
		t.Errorf("HiddenBy = %q, want user", got.Visibility.HiddenBy)
	}
	if got.Visibility.DisabledBy != "" {
		t.Errorf("DisabledBy = %q, want empty", got.Visibility.DisabledBy)
	}
	if got.Visibility.EntityCategory != "diagnostic" {
		t.Errorf("EntityCategory = %q, want diagnostic", got.Visibility.EntityCategory)
	}
	if got.EntityCategory != "" {
		t.Errorf("top-level EntityCategory = %q, want empty for visibility-only include", got.EntityCategory)
	}
}

func TestEntityMetadataResolverVisibilityContextRoles(t *testing.T) {
	t.Parallel()

	resolver := NewEntityMetadataResolver(nil, nil, nil)
	tests := []struct {
		name        string
		entry       EntityRegistryEntry
		wantRole    string
		wantDefault bool
	}{
		{
			name:        "default",
			entry:       EntityRegistryEntry{EntityID: "light.office"},
			wantRole:    "default",
			wantDefault: true,
		},
		{
			name:     "diagnostic",
			entry:    EntityRegistryEntry{EntityID: "sensor.office_rssi", EntityCategory: "diagnostic"},
			wantRole: "diagnostic",
		},
		{
			name:     "config",
			entry:    EntityRegistryEntry{EntityID: "switch.office_led", EntityCategory: "config"},
			wantRole: "config",
		},
		{
			name:     "disabled",
			entry:    EntityRegistryEntry{EntityID: "sensor.office_old", DisabledBy: "user"},
			wantRole: "disabled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolver.MetadataForEntity(&tt.entry, nil, EntityMetadataIncludes{Visibility: true})
			if got == nil || got.Visibility == nil {
				t.Fatalf("MetadataForEntity returned %#v, want visibility", got)
			}
			if got.Visibility.ContextRole != tt.wantRole || got.Visibility.DefaultContext != tt.wantDefault {
				t.Fatalf("role/default = %q/%v, want %q/%v", got.Visibility.ContextRole, got.Visibility.DefaultContext, tt.wantRole, tt.wantDefault)
			}
		})
	}
}

func TestEntityMetadataResolverCopiesDescriptionMaps(t *testing.T) {
	t.Parallel()

	resolver := NewEntityMetadataResolver(nil, nil, nil)
	entry := &EntityRegistryEntry{
		EntityID:     "light.office",
		Options:      map[string]any{"restore": true},
		Capabilities: map[string]any{"brightness": true},
		Categories:   map[string]string{"diagnostic": "health"},
	}

	got := resolver.MetadataForEntity(entry, nil, EntityMetadataIncludes{Description: true})
	if got == nil {
		t.Fatal("MetadataForEntity returned nil")
	}

	entry.Options["restore"] = false
	entry.Capabilities["brightness"] = false
	entry.Categories["diagnostic"] = "changed"
	got.Options["new"] = true
	got.Capabilities["new"] = true
	got.Categories["new"] = "value"

	if got.Options["restore"] != true {
		t.Fatalf("Options shared source map; got %#v", got.Options)
	}
	if got.Capabilities["brightness"] != true {
		t.Fatalf("Capabilities shared source map; got %#v", got.Capabilities)
	}
	if got.Categories["diagnostic"] != "health" {
		t.Fatalf("Categories shared source map; got %#v", got.Categories)
	}
	if _, ok := entry.Options["new"]; ok {
		t.Fatalf("Options shared metadata map; source %#v", entry.Options)
	}
	if _, ok := entry.Capabilities["new"]; ok {
		t.Fatalf("Capabilities shared metadata map; source %#v", entry.Capabilities)
	}
	if _, ok := entry.Categories["new"]; ok {
		t.Fatalf("Categories shared metadata map; source %#v", entry.Categories)
	}
}

func TestEntityMetadataResolverAppliesFloorAlias(t *testing.T) {
	t.Parallel()

	resolver := NewEntityMetadataResolverWithFloorAlias(
		[]Area{{AreaID: "office", Name: "Office", FloorID: "building_a"}},
		nil,
		nil,
		[]FloorRegistryEntry{{FloorID: "building_a", Name: "Building A"}},
		"building",
	)

	got := resolver.MetadataForEntity(&EntityRegistryEntry{
		EntityID: "sensor.office",
		AreaID:   "office",
	}, nil, EntityMetadataIncludes{Area: true})
	if got == nil || got.Area == nil {
		t.Fatalf("MetadataForEntity returned %#v, want area metadata", got)
	}
	if got.Area.Floor != nil {
		t.Fatalf("Floor = %#v, want nil when floor is aliased as building", got.Area.Floor)
	}
	if got.Area.Building == nil || got.Area.Building.Name != "Building A" {
		t.Fatalf("Building = %#v, want floor metadata exposed as building", got.Area.Building)
	}
}

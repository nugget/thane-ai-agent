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

	include := &EntityMetadataIncludes{Area: true, Labels: true}
	got := include.Clone()
	if got == nil {
		t.Fatal("Clone() returned nil")
	}
	if got == include {
		t.Fatal("Clone() returned the original pointer")
	}
	if !got.Area || !got.Labels || got.Device || got.Description {
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
	)

	entry := &EntityRegistryEntry{
		EntityID:    "sensor.office_temperature",
		Name:        "Temperature",
		Description: "Ambient office temperature",
		Aliases:     []string{"office temp"},
		AreaID:      "",
		DeviceID:    "device_1",
		Labels:      []string{"label_env"},
		Platform:    "zwave_js",
		DeviceClass: "temperature",
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
	if got.Device == nil || got.Device.ID != "device_1" || got.Device.NameByUser != "Office Climate Hub" {
		t.Fatalf("Device = %#v, want resolved device", got.Device)
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

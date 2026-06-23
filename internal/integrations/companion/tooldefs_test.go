package companion

import (
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// TestCapabilityToolsDecodeAndBackCompat verifies the additive tools[]
// field decodes when present and is absent (nil + omitted) otherwise, so
// new Macs work against old servers and vice versa.
func TestCapabilityToolsDecodeAndBackCompat(t *testing.T) {
	withTools := `{
		"name": "macos.contacts",
		"version": "1",
		"methods": ["search_contacts"],
		"tools": [{
			"name": "macos_search_contacts",
			"description": "Search the user's macOS Contacts.",
			"method": "search_contacts",
			"tags": ["companion", "people"],
			"input_schema": {"type":"object","properties":{"query":{"type":"string"}}}
		}]
	}`
	var cap Capability
	if err := json.Unmarshal([]byte(withTools), &cap); err != nil {
		t.Fatalf("decode capability with tools: %v", err)
	}
	if len(cap.Tools) != 1 {
		t.Fatalf("tools length: got %d, want 1", len(cap.Tools))
	}
	td := cap.Tools[0]
	if td.Name != "macos_search_contacts" || td.Method != "search_contacts" {
		t.Errorf("tool def fields: got name=%q method=%q", td.Name, td.Method)
	}
	if td.InputSchema["type"] != "object" {
		t.Errorf("input_schema not preserved: %v", td.InputSchema)
	}

	// Old Mac: no tools field — must decode cleanly with nil Tools.
	legacy := `{"name":"macos.calendar","version":"1","methods":["list_events"]}`
	var legacyCap Capability
	if err := json.Unmarshal([]byte(legacy), &legacyCap); err != nil {
		t.Fatalf("decode legacy capability: %v", err)
	}
	if legacyCap.Tools != nil {
		t.Errorf("legacy capability Tools: got %v, want nil", legacyCap.Tools)
	}

	// omitempty: a capability with no tools must not emit the field, so an
	// old server decoding a new server's echo never sees an empty tools key.
	out, err := json.Marshal(Capability{Name: "macos.calendar", Methods: []string{"list_events"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if _, ok := m["tools"]; ok {
		t.Errorf("empty Tools should be omitted, got %s", out)
	}
}

// TestSetCapabilitiesNormalizesTools covers trim, drop-empty (name/method),
// dedup-by-name, and tag normalization, and that the result survives the
// snapshot the registrar reads.
func TestSetCapabilitiesNormalizesTools(t *testing.T) {
	p := &Provider{done: make(chan struct{})}
	p.setCapabilities([]Capability{{
		Name:    "macos.calendar",
		Version: "1",
		Methods: []string{"list_events"},
		Tools: []ToolDefinition{
			{Name: "  macos_calendar_events ", Method: " list_events ", Description: "  List events ", Tags: []string{" companion ", "companion", "", "calendar"}},
			{Name: "", Method: "list_events"},                      // dropped: no name
			{Name: "macos_no_method", Method: "  "},                // dropped: no method
			{Name: "macos_calendar_events", Method: "list_events"}, // dropped: duplicate name
		},
	}})

	snap := p.capabilitiesSnapshot()
	if len(snap) != 1 {
		t.Fatalf("capabilities: got %d, want 1", len(snap))
	}
	tools := snap[0].Tools
	if len(tools) != 1 {
		t.Fatalf("normalized tools: got %d, want 1", len(tools))
	}
	td := tools[0]
	if td.Name != "macos_calendar_events" {
		t.Errorf("name not trimmed: %q", td.Name)
	}
	if td.Method != "list_events" {
		t.Errorf("method not trimmed: %q", td.Method)
	}
	if td.Description != "List events" {
		t.Errorf("description not trimmed: %q", td.Description)
	}
	if want := []string{"companion", "calendar"}; !reflect.DeepEqual(td.Tags, want) {
		t.Errorf("tags: got %v, want %v", td.Tags, want)
	}
}

// TestCapabilitiesSnapshotIsolation verifies a snapshot caller cannot
// mutate provider state through the returned tags slice.
func TestCapabilitiesSnapshotIsolation(t *testing.T) {
	p := &Provider{done: make(chan struct{})}
	p.setCapabilities([]Capability{{
		Name:    "macos.calendar",
		Methods: []string{"list_events"},
		Tools:   []ToolDefinition{{Name: "macos_calendar_events", Method: "list_events", Tags: []string{"companion"}}},
	}})

	snap := p.capabilitiesSnapshot()
	snap[0].Tools[0].Tags[0] = "mutated"

	again := p.capabilitiesSnapshot()
	if again[0].Tools[0].Tags[0] != "companion" {
		t.Errorf("snapshot mutation leaked into provider state: %q", again[0].Tools[0].Tags[0])
	}
}

// TestRegistryOnChangeFires verifies the change hook fires on capability
// registration and disconnect, but not on bare connect or a no-op remove.
func TestRegistryOnChangeFires(t *testing.T) {
	r := NewRegistry(nil)
	var calls atomic.Int64
	r.SetOnChange(func() { calls.Add(1) })

	p := &Provider{ID: "prov_x", Account: "nugget", ConnectedAt: time.Now(), done: make(chan struct{})}
	r.Add(p) // bare connect carries no capabilities yet -> no fire
	if got := calls.Load(); got != 0 {
		t.Fatalf("onChange after Add: got %d, want 0", got)
	}

	if err := r.RegisterCapabilities("prov_x", []Capability{{Name: "macos.calendar", Methods: []string{"list_events"}}}); err != nil {
		t.Fatalf("RegisterCapabilities: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("onChange after RegisterCapabilities: got %d, want 1", got)
	}

	r.Remove("prov_x")
	if got := calls.Load(); got != 2 {
		t.Fatalf("onChange after Remove: got %d, want 2", got)
	}

	r.Remove("prov_doesnotexist") // no-op
	if got := calls.Load(); got != 2 {
		t.Fatalf("onChange after no-op Remove: got %d, want 2", got)
	}
}

// TestRegistryOnChangeMayCallRegistry asserts the callback runs outside the
// registry lock, so a registrar rebuild that calls back into List does not
// deadlock — and the freshly-registered tools are visible from inside it.
func TestRegistryOnChangeMayCallRegistry(t *testing.T) {
	r := NewRegistry(nil)
	seen := make(chan int, 1)
	r.SetOnChange(func() {
		infos := r.List()
		n := 0
		for _, info := range infos {
			for _, cap := range info.Capabilities {
				n += len(cap.Tools)
			}
		}
		seen <- n
	})

	p := &Provider{ID: "prov_y", Account: "nugget", ConnectedAt: time.Now(), done: make(chan struct{})}
	r.Add(p)
	if err := r.RegisterCapabilities("prov_y", []Capability{{
		Name:    "macos.calendar",
		Methods: []string{"list_events"},
		Tools:   []ToolDefinition{{Name: "macos_calendar_events", Method: "list_events"}},
	}}); err != nil {
		t.Fatalf("RegisterCapabilities: %v", err)
	}

	select {
	case n := <-seen:
		if n != 1 {
			t.Errorf("tools visible inside callback: got %d, want 1", n)
		}
	default:
		t.Fatal("onChange did not fire synchronously")
	}
}

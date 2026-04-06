package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// mockCapabilityManager records capability requests and drops.
type mockCapabilityManager struct {
	activeTags map[string]bool
	allTags    map[string]bool // valid tag names
	baseline   map[string]bool
}

func newMockCapabilityManager(validTags ...string) *mockCapabilityManager {
	m := &mockCapabilityManager{
		activeTags: make(map[string]bool),
		allTags:    make(map[string]bool),
	}
	for _, tag := range validTags {
		m.allTags[tag] = true
	}
	return m
}

func (m *mockCapabilityManager) RequestCapability(_ context.Context, tag string) error {
	if !m.allTags[tag] {
		return fmt.Errorf("unknown capability tag: %q", tag)
	}
	m.activeTags[tag] = true
	return nil
}

func (m *mockCapabilityManager) DropCapability(_ context.Context, tag string) error {
	if !m.allTags[tag] {
		return fmt.Errorf("unknown capability tag: %q", tag)
	}
	delete(m.activeTags, tag)
	return nil
}

func (m *mockCapabilityManager) ResetCapabilities(_ context.Context) ([]string, error) {
	var dropped []string
	for tag := range m.activeTags {
		if m.baseline[tag] {
			continue
		}
		dropped = append(dropped, tag)
		delete(m.activeTags, tag)
	}
	sort.Strings(dropped)
	return dropped, nil
}

func (m *mockCapabilityManager) ActiveTags(_ context.Context) map[string]bool {
	return m.activeTags
}

func TestActivateCapability(t *testing.T) {
	mgr := newMockCapabilityManager("ha", "web")
	manifest := []CapabilityManifest{
		{Tag: "ha", Description: "Home Assistant", Tools: []string{"get_state"}, AlwaysActive: false},
		{Tag: "web", Description: "Web retrieval", Tools: []string{"web_search"}, AlwaysActive: false},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("activate_capability")
	if tool == nil {
		t.Fatal("activate_capability not registered")
	}

	// Activate a valid tag.
	result, err := tool.Handler(context.Background(), map[string]any{"tag": "ha"})
	if err != nil {
		t.Fatalf("activate_capability error: %v", err)
	}
	if !strings.Contains(result, "activated") {
		t.Errorf("result = %q, want to contain 'activated'", result)
	}
	if !strings.Contains(result, "1 tools now available") {
		t.Errorf("result = %q, want to mention tools count", result)
	}
	if !mgr.activeTags["ha"] {
		t.Error("ha tag should be active after request")
	}

	// Try an unknown tag.
	_, err = tool.Handler(context.Background(), map[string]any{"tag": "nonexistent"})
	if err == nil {
		t.Error("expected error for unknown tag")
	}
}

func TestDeactivateCapability(t *testing.T) {
	mgr := newMockCapabilityManager("ha", "web")
	mgr.activeTags["ha"] = true
	mgr.activeTags["web"] = true

	manifest := []CapabilityManifest{
		{Tag: "ha", Description: "Home Assistant", Tools: []string{"get_state", "call_service"}, AlwaysActive: false},
		{Tag: "web", Description: "Web retrieval", Tools: []string{"web_search"}, AlwaysActive: false},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("deactivate_capability")
	if tool == nil {
		t.Fatal("deactivate_capability not registered")
	}

	// Drop an active tag.
	result, err := tool.Handler(context.Background(), map[string]any{"tag": "ha"})
	if err != nil {
		t.Fatalf("deactivate_capability error: %v", err)
	}
	if !strings.Contains(result, "deactivated") {
		t.Errorf("result = %q, want to contain 'deactivated'", result)
	}
	if !strings.Contains(result, "2 tools removed") {
		t.Errorf("result = %q, want to mention tool count removed", result)
	}
	if mgr.activeTags["ha"] {
		t.Error("ha tag should be inactive after drop")
	}

	// Response should list remaining active tags.
	if !strings.Contains(result, "Active: web") {
		t.Errorf("result = %q, want to list remaining active tags", result)
	}

	// web should still be active.
	if !mgr.activeTags["web"] {
		t.Error("web tag should still be active")
	}
}

func TestListLoadedCapabilities(t *testing.T) {
	mgr := newMockCapabilityManager("forge", "ha")
	mgr.activeTags["forge"] = true
	mgr.activeTags["ha"] = true

	manifest := []CapabilityManifest{
		{Tag: "forge", Description: "Forge tools", Tools: []string{"forge_pr_get"}},
		{Tag: "ha", Description: "Home Assistant tools", Tools: []string{"get_state"}, AlwaysActive: true},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("list_loaded_capabilities")
	if tool == nil {
		t.Fatal("list_loaded_capabilities not registered")
	}

	result, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("list_loaded_capabilities error: %v", err)
	}
	if !strings.Contains(result, "\"loaded_capabilities\"") {
		t.Fatalf("result = %q, want loaded_capabilities payload", result)
	}
	if !strings.Contains(result, "\"tag\":\"forge\"") {
		t.Fatalf("result = %q, want forge tag", result)
	}
	if !strings.Contains(result, "\"tag\":\"ha\"") {
		t.Fatalf("result = %q, want ha tag", result)
	}
	if !strings.Contains(result, "\"always_active\":true") {
		t.Fatalf("result = %q, want always_active metadata", result)
	}
}

func TestResetCapabilities(t *testing.T) {
	mgr := newMockCapabilityManager("forge", "web", "core")
	mgr.activeTags["forge"] = true
	mgr.activeTags["web"] = true
	mgr.activeTags["core"] = true
	mgr.baseline = map[string]bool{"core": true}

	manifest := []CapabilityManifest{
		{Tag: "forge", Description: "Forge tools", Tools: []string{"forge_pr_get"}},
		{Tag: "web", Description: "Web tools", Tools: []string{"web_fetch"}},
		{Tag: "core", Description: "Core tools", Tools: []string{"thane_delegate"}, AlwaysActive: true},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("reset_capabilities")
	if tool == nil {
		t.Fatal("reset_capabilities not registered")
	}

	result, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("reset_capabilities error: %v", err)
	}
	if !strings.Contains(result, "Capability state reset to baseline.") {
		t.Fatalf("result = %q, want baseline-reset confirmation", result)
	}
	if !strings.Contains(result, "Deactivated: forge, web.") {
		t.Fatalf("result = %q, want dropped tag list", result)
	}
	if !strings.Contains(result, "Active: core.") {
		t.Fatalf("result = %q, want remaining baseline tag list", result)
	}
	if mgr.activeTags["forge"] || mgr.activeTags["web"] {
		t.Fatalf("activeTags = %#v, want only baseline tags left", mgr.activeTags)
	}
	if !mgr.activeTags["core"] {
		t.Fatalf("activeTags = %#v, want core to remain active", mgr.activeTags)
	}
}

func TestResetCapabilities_TruncatesRemovedTools(t *testing.T) {
	mgr := newMockCapabilityManager("alpha", "beta", "core")
	mgr.activeTags["alpha"] = true
	mgr.activeTags["beta"] = true
	mgr.activeTags["core"] = true
	mgr.baseline = map[string]bool{"core": true}

	manifest := []CapabilityManifest{
		{Tag: "alpha", Tools: []string{"a1", "a2", "a3", "a4", "a5"}},
		{Tag: "beta", Tools: []string{"b1", "b2", "b3", "b4", "b5"}},
		{Tag: "core", Tools: []string{"thane_delegate"}, AlwaysActive: true},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("reset_capabilities")
	if tool == nil {
		t.Fatal("reset_capabilities not registered")
	}

	result, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("reset_capabilities error: %v", err)
	}
	if !strings.Contains(result, "Tools removed: a1, a2, a3, a4, a5, b1, b2, b3, and 2 more.") {
		t.Fatalf("result = %q, want truncated tool list", result)
	}
}

func TestActivateCapability_EmptyTag(t *testing.T) {
	mgr := newMockCapabilityManager("ha")
	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, nil)

	tool := reg.Get("activate_capability")
	_, err := tool.Handler(context.Background(), map[string]any{"tag": ""})
	if err == nil {
		t.Error("expected error for empty tag")
	}
}

func TestDeactivateCapability_EmptyTag(t *testing.T) {
	mgr := newMockCapabilityManager("ha")
	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, nil)

	tool := reg.Get("deactivate_capability")
	_, err := tool.Handler(context.Background(), map[string]any{"tag": ""})
	if err == nil {
		t.Error("expected error for empty tag")
	}
}

func TestActivateCapability_DescriptionContainsManifest(t *testing.T) {
	mgr := newMockCapabilityManager("ha", "web")
	manifest := []CapabilityManifest{
		{Tag: "ha", Description: "Home Assistant tools", Tools: []string{"get_state", "call_service"}, AlwaysActive: true},
		{Tag: "web", Description: "Web retrieval tools", Tools: []string{"web_search"}, AlwaysActive: false},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("activate_capability")

	// Always-active tags should NOT appear in the description (they can't be toggled).
	if strings.Contains(tool.Description, "**ha**") {
		t.Error("always-active tag 'ha' should not appear in description")
	}

	// Non-always-active tags should appear.
	if !strings.Contains(tool.Description, "**web**") {
		t.Errorf("description should mention 'web': %s", tool.Description)
	}
	if !strings.Contains(tool.Description, "(1 tools)") {
		t.Errorf("description should list tool count: %s", tool.Description)
	}
}

func TestBuildCapabilityManifest(t *testing.T) {
	tags := map[string][]string{
		"ha":  {"get_state", "call_service"},
		"web": {"web_search"},
	}
	descriptions := map[string]string{
		"ha":  "Home Assistant",
		"web": "Web retrieval",
	}
	alwaysActive := map[string]bool{
		"ha": true,
	}

	manifest := BuildCapabilityManifest(tags, descriptions, alwaysActive, nil)

	if len(manifest) != 2 {
		t.Fatalf("len(manifest) = %d, want 2", len(manifest))
	}

	// Should be sorted by tag name.
	if manifest[0].Tag != "ha" {
		t.Errorf("manifest[0].Tag = %q, want %q", manifest[0].Tag, "ha")
	}
	if manifest[1].Tag != "web" {
		t.Errorf("manifest[1].Tag = %q, want %q", manifest[1].Tag, "web")
	}
	if !manifest[0].AlwaysActive {
		t.Error("ha should be always_active")
	}
	if manifest[1].AlwaysActive {
		t.Error("web should not be always_active")
	}
}

func TestRegistryFilterByTags(t *testing.T) {
	reg := NewEmptyRegistry()
	reg.Register(&Tool{Name: "get_state", Description: "HA state"})
	reg.Register(&Tool{Name: "call_service", Description: "HA service"})
	reg.Register(&Tool{Name: "web_search", Description: "Search"})
	reg.Register(&Tool{Name: "remember_fact", Description: "Memory"})

	reg.SetTagIndex(map[string][]string{
		"ha":     {"get_state", "call_service"},
		"web":    {"web_search"},
		"memory": {"remember_fact"},
	})

	tests := []struct {
		name    string
		tags    []string
		wantIn  []string
		wantOut []string
	}{
		{
			name:   "nil tags returns all",
			tags:   nil,
			wantIn: []string{"get_state", "call_service", "web_search", "remember_fact"},
		},
		{
			name:   "empty tags returns all",
			tags:   []string{},
			wantIn: []string{"get_state", "call_service", "web_search", "remember_fact"},
		},
		{
			name:    "ha tag only",
			tags:    []string{"ha"},
			wantIn:  []string{"get_state", "call_service"},
			wantOut: []string{"web_search", "remember_fact"},
		},
		{
			name:    "web tag only",
			tags:    []string{"web"},
			wantIn:  []string{"web_search"},
			wantOut: []string{"get_state", "call_service", "remember_fact"},
		},
		{
			name:    "multiple tags",
			tags:    []string{"ha", "web"},
			wantIn:  []string{"get_state", "call_service", "web_search"},
			wantOut: []string{"remember_fact"},
		},
		{
			name:    "unknown tag filters to tagged-only",
			tags:    []string{"nonexistent"},
			wantOut: []string{"get_state", "call_service", "web_search", "remember_fact"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := reg.FilterByTags(tt.tags)
			for _, name := range tt.wantIn {
				if filtered.Get(name) == nil {
					t.Errorf("filtered registry missing %q", name)
				}
			}
			for _, name := range tt.wantOut {
				if filtered.Get(name) != nil {
					t.Errorf("filtered registry should not contain %q", name)
				}
			}
		})
	}
}

func TestRegistryFilterByTags_AlwaysAvailable(t *testing.T) {
	reg := NewEmptyRegistry()
	// Tagged tools
	reg.Register(&Tool{Name: "get_state", Description: "HA state"})
	reg.Register(&Tool{Name: "web_search", Description: "Search"})
	// AlwaysAvailable meta-tools (like activate_capability, deactivate_capability,
	// list_loaded_capabilities, and reset_capabilities)
	reg.Register(&Tool{Name: "activate_capability", Description: "Activate a tag", AlwaysAvailable: true})
	reg.Register(&Tool{Name: "deactivate_capability", Description: "Deactivate a tag", AlwaysAvailable: true})
	reg.Register(&Tool{Name: "list_loaded_capabilities", Description: "List loaded tags", AlwaysAvailable: true})
	reg.Register(&Tool{Name: "reset_capabilities", Description: "Reset capability state", AlwaysAvailable: true})
	// Untagged tool WITHOUT AlwaysAvailable — should be filtered out
	reg.Register(&Tool{Name: "plain_untagged", Description: "Not tagged, not meta"})

	reg.SetTagIndex(map[string][]string{
		"ha":  {"get_state"},
		"web": {"web_search"},
	})

	tests := []struct {
		name    string
		tags    []string
		wantIn  []string
		wantOut []string
	}{
		{
			name:    "always-available tools survive ha-only filter",
			tags:    []string{"ha"},
			wantIn:  []string{"get_state", "activate_capability", "deactivate_capability", "list_loaded_capabilities", "reset_capabilities"},
			wantOut: []string{"web_search", "plain_untagged"},
		},
		{
			name:    "always-available tools survive web-only filter",
			tags:    []string{"web"},
			wantIn:  []string{"web_search", "activate_capability", "deactivate_capability", "list_loaded_capabilities", "reset_capabilities"},
			wantOut: []string{"get_state", "plain_untagged"},
		},
		{
			name:    "always-available tools survive unknown-tag filter",
			tags:    []string{"nonexistent"},
			wantIn:  []string{"activate_capability", "deactivate_capability", "list_loaded_capabilities", "reset_capabilities"},
			wantOut: []string{"get_state", "web_search", "plain_untagged"},
		},
		{
			name:   "nil tags returns everything",
			tags:   nil,
			wantIn: []string{"get_state", "web_search", "activate_capability", "deactivate_capability", "list_loaded_capabilities", "reset_capabilities", "plain_untagged"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := reg.FilterByTags(tt.tags)
			for _, name := range tt.wantIn {
				if filtered.Get(name) == nil {
					t.Errorf("filtered registry missing %q", name)
				}
			}
			for _, name := range tt.wantOut {
				if filtered.Get(name) != nil {
					t.Errorf("filtered registry should not contain %q", name)
				}
			}
		})
	}
}

func TestRegistryTaggedToolNames(t *testing.T) {
	reg := NewEmptyRegistry()
	reg.SetTagIndex(map[string][]string{
		"ha":  {"get_state", "call_service"},
		"web": {"web_search"},
	})

	if names := reg.TaggedToolNames("ha"); len(names) != 2 {
		t.Errorf("TaggedToolNames(ha) = %v, want 2 items", names)
	}
	if names := reg.TaggedToolNames("unknown"); names != nil {
		t.Errorf("TaggedToolNames(unknown) = %v, want nil", names)
	}
}

func TestRegistryFilterByTags_NoTagIndex(t *testing.T) {
	reg := NewEmptyRegistry()
	reg.Register(&Tool{Name: "test_tool"})

	// No tag index set — FilterByTags should return all tools.
	filtered := reg.FilterByTags([]string{"ha"})
	if filtered.Get("test_tool") == nil {
		t.Error("FilterByTags with no tag index should return all tools")
	}
}

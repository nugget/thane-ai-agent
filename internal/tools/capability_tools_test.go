package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockCapabilityManager records capability requests and drops.
type mockCapabilityManager struct {
	activeTags map[string]bool
	allTags    map[string]bool // valid tag names
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

func (m *mockCapabilityManager) RequestCapability(tag string) error {
	if !m.allTags[tag] {
		return fmt.Errorf("unknown capability tag: %q", tag)
	}
	m.activeTags[tag] = true
	return nil
}

func (m *mockCapabilityManager) DropCapability(tag string) error {
	if !m.allTags[tag] {
		return fmt.Errorf("unknown capability tag: %q", tag)
	}
	delete(m.activeTags, tag)
	return nil
}

func (m *mockCapabilityManager) ActiveTags() map[string]bool {
	return m.activeTags
}

func TestRequestCapability(t *testing.T) {
	mgr := newMockCapabilityManager("ha", "search")
	manifest := []CapabilityManifest{
		{Tag: "ha", Description: "Home Assistant", Tools: []string{"get_state"}, AlwaysActive: false},
		{Tag: "search", Description: "Web search", Tools: []string{"web_search"}, AlwaysActive: false},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("request_capability")
	if tool == nil {
		t.Fatal("request_capability not registered")
	}

	// Activate a valid tag.
	result, err := tool.Handler(context.Background(), map[string]any{"tag": "ha"})
	if err != nil {
		t.Fatalf("request_capability error: %v", err)
	}
	if !strings.Contains(result, "activated") {
		t.Errorf("result = %q, want to contain 'activated'", result)
	}
	if !strings.Contains(result, "get_state") {
		t.Errorf("result = %q, want to list tools like 'get_state'", result)
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

func TestDropCapability(t *testing.T) {
	mgr := newMockCapabilityManager("ha", "search")
	mgr.activeTags["ha"] = true
	mgr.activeTags["search"] = true

	manifest := []CapabilityManifest{
		{Tag: "ha", Description: "Home Assistant", Tools: []string{"get_state", "call_service"}, AlwaysActive: false},
		{Tag: "search", Description: "Web search", Tools: []string{"web_search"}, AlwaysActive: false},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("drop_capability")
	if tool == nil {
		t.Fatal("drop_capability not registered")
	}

	// Drop an active tag.
	result, err := tool.Handler(context.Background(), map[string]any{"tag": "ha"})
	if err != nil {
		t.Fatalf("drop_capability error: %v", err)
	}
	if !strings.Contains(result, "deactivated") {
		t.Errorf("result = %q, want to contain 'deactivated'", result)
	}
	if !strings.Contains(result, "get_state") {
		t.Errorf("result = %q, want to list removed tools like 'get_state'", result)
	}
	if mgr.activeTags["ha"] {
		t.Error("ha tag should be inactive after drop")
	}

	// Response should list remaining active tags.
	if !strings.Contains(result, "Active tags: search") {
		t.Errorf("result = %q, want to list remaining active tags", result)
	}

	// search should still be active.
	if !mgr.activeTags["search"] {
		t.Error("search tag should still be active")
	}
}

func TestRequestCapability_EmptyTag(t *testing.T) {
	mgr := newMockCapabilityManager("ha")
	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, nil)

	tool := reg.Get("request_capability")
	_, err := tool.Handler(context.Background(), map[string]any{"tag": ""})
	if err == nil {
		t.Error("expected error for empty tag")
	}
}

func TestDropCapability_EmptyTag(t *testing.T) {
	mgr := newMockCapabilityManager("ha")
	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, nil)

	tool := reg.Get("drop_capability")
	_, err := tool.Handler(context.Background(), map[string]any{"tag": ""})
	if err == nil {
		t.Error("expected error for empty tag")
	}
}

func TestRequestCapability_DescriptionContainsManifest(t *testing.T) {
	mgr := newMockCapabilityManager("ha", "search")
	manifest := []CapabilityManifest{
		{Tag: "ha", Description: "Home Assistant tools", Tools: []string{"get_state", "call_service"}, AlwaysActive: true},
		{Tag: "search", Description: "Web search tools", Tools: []string{"web_search"}, AlwaysActive: false},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("request_capability")

	// Always-active tags should NOT appear in the description (they can't be toggled).
	if strings.Contains(tool.Description, "**ha**") {
		t.Error("always-active tag 'ha' should not appear in description")
	}

	// Non-always-active tags should appear.
	if !strings.Contains(tool.Description, "**search**") {
		t.Errorf("description should mention 'search': %s", tool.Description)
	}
	if !strings.Contains(tool.Description, "web_search") {
		t.Errorf("description should list tools: %s", tool.Description)
	}
}

func TestBuildCapabilityManifest(t *testing.T) {
	tags := map[string][]string{
		"ha":     {"get_state", "call_service"},
		"search": {"web_search"},
	}
	descriptions := map[string]string{
		"ha":     "Home Assistant",
		"search": "Web search",
	}
	alwaysActive := map[string]bool{
		"ha": true,
	}

	contextFiles := map[string][]string{
		"ha": {"/path/to/ha-guide.md", "/path/to/automations.md"},
	}

	manifest := BuildCapabilityManifest(tags, descriptions, alwaysActive, contextFiles)

	if len(manifest) != 2 {
		t.Fatalf("len(manifest) = %d, want 2", len(manifest))
	}

	// Should be sorted by tag name.
	if manifest[0].Tag != "ha" {
		t.Errorf("manifest[0].Tag = %q, want %q", manifest[0].Tag, "ha")
	}
	if manifest[1].Tag != "search" {
		t.Errorf("manifest[1].Tag = %q, want %q", manifest[1].Tag, "search")
	}
	if !manifest[0].AlwaysActive {
		t.Error("ha should be always_active")
	}
	if manifest[1].AlwaysActive {
		t.Error("search should not be always_active")
	}

	// Context files should be propagated.
	if len(manifest[0].Context) != 2 {
		t.Errorf("manifest[0].Context = %v, want 2 files", manifest[0].Context)
	}
	if len(manifest[1].Context) != 0 {
		t.Errorf("manifest[1].Context = %v, want empty", manifest[1].Context)
	}
}

func TestBuildCapabilityManifest_NilContextFiles(t *testing.T) {
	tags := map[string][]string{
		"ha": {"get_state"},
	}
	descriptions := map[string]string{
		"ha": "Home Assistant",
	}
	alwaysActive := map[string]bool{}

	manifest := BuildCapabilityManifest(tags, descriptions, alwaysActive, nil)

	if len(manifest) != 1 {
		t.Fatalf("len(manifest) = %d, want 1", len(manifest))
	}
	if len(manifest[0].Context) != 0 {
		t.Errorf("manifest[0].Context = %v, want empty with nil contextFiles", manifest[0].Context)
	}
}

func TestRequestCapability_ContextInMessage(t *testing.T) {
	mgr := newMockCapabilityManager("forge")
	manifest := []CapabilityManifest{
		{Tag: "forge", Description: "Code generation", Tools: []string{"forge_run"}, Context: []string{"/docs/arch.md", "/docs/style.md"}},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("request_capability")
	result, err := tool.Handler(context.Background(), map[string]any{"tag": "forge"})
	if err != nil {
		t.Fatalf("request_capability error: %v", err)
	}
	if !strings.Contains(result, "Context loaded: 2 files") {
		t.Errorf("result = %q, want to mention context loaded", result)
	}
}

func TestRequestCapability_DescriptionShowsContext(t *testing.T) {
	mgr := newMockCapabilityManager("forge")
	manifest := []CapabilityManifest{
		{Tag: "forge", Description: "Code generation", Tools: []string{"forge_run"}, Context: []string{"/docs/arch.md"}},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("request_capability")
	if !strings.Contains(tool.Description, "context: 1 file") {
		t.Errorf("description should mention context file: %s", tool.Description)
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
		"search": {"web_search"},
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
			name:    "search tag only",
			tags:    []string{"search"},
			wantIn:  []string{"web_search"},
			wantOut: []string{"get_state", "call_service", "remember_fact"},
		},
		{
			name:    "multiple tags",
			tags:    []string{"ha", "search"},
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
	// AlwaysAvailable meta-tools (like request_capability, drop_capability)
	reg.Register(&Tool{Name: "request_capability", Description: "Activate a tag", AlwaysAvailable: true})
	reg.Register(&Tool{Name: "drop_capability", Description: "Deactivate a tag", AlwaysAvailable: true})
	// Untagged tool WITHOUT AlwaysAvailable — should be filtered out
	reg.Register(&Tool{Name: "plain_untagged", Description: "Not tagged, not meta"})

	reg.SetTagIndex(map[string][]string{
		"ha":     {"get_state"},
		"search": {"web_search"},
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
			wantIn:  []string{"get_state", "request_capability", "drop_capability"},
			wantOut: []string{"web_search", "plain_untagged"},
		},
		{
			name:    "always-available tools survive search-only filter",
			tags:    []string{"search"},
			wantIn:  []string{"web_search", "request_capability", "drop_capability"},
			wantOut: []string{"get_state", "plain_untagged"},
		},
		{
			name:    "always-available tools survive unknown-tag filter",
			tags:    []string{"nonexistent"},
			wantIn:  []string{"request_capability", "drop_capability"},
			wantOut: []string{"get_state", "web_search", "plain_untagged"},
		},
		{
			name:   "nil tags returns everything",
			tags:   nil,
			wantIn: []string{"get_state", "web_search", "request_capability", "drop_capability", "plain_untagged"},
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
		"ha":     {"get_state", "call_service"},
		"search": {"web_search"},
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

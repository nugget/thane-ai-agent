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
		{Tag: "ha", Description: "Home Assistant", Tools: []string{"ha_get_state"}, Core: false},
		{Tag: "web", Description: "Web retrieval", Tools: []string{"web_search"}, Core: false},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("tag_activate")
	if tool == nil {
		t.Fatal("tag_activate not registered")
	}

	// Activate a valid tag.
	result, err := tool.Handler(context.Background(), map[string]any{"tag": "ha"})
	if err != nil {
		t.Fatalf("tag_activate error: %v", err)
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
		{Tag: "ha", Description: "Home Assistant", Tools: []string{"ha_get_state", "ha_call_service"}, Core: false},
		{Tag: "web", Description: "Web retrieval", Tools: []string{"web_search"}, Core: false},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("tag_deactivate")
	if tool == nil {
		t.Fatal("tag_deactivate not registered")
	}

	// Drop an active tag.
	result, err := tool.Handler(context.Background(), map[string]any{"tag": "ha"})
	if err != nil {
		t.Fatalf("tag_deactivate error: %v", err)
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

func TestResetCapabilities(t *testing.T) {
	mgr := newMockCapabilityManager("forge", "web", "core")
	mgr.activeTags["forge"] = true
	mgr.activeTags["web"] = true
	mgr.activeTags["core"] = true
	mgr.baseline = map[string]bool{"core": true}

	manifest := []CapabilityManifest{
		{Tag: "forge", Description: "Forge tools", Tools: []string{"forge_pr_get"}},
		{Tag: "web", Description: "Web tools", Tools: []string{"web_fetch"}},
		{Tag: "core", Description: "Core tools", Tools: []string{"thane_now"}, Core: true},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("tag_reset")
	if tool == nil {
		t.Fatal("tag_reset not registered")
	}

	result, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("tag_reset error: %v", err)
	}
	if !strings.Contains(result, "Tag state reset to baseline.") {
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
		{Tag: "core", Tools: []string{"thane_now"}, Core: true},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("tag_reset")
	if tool == nil {
		t.Fatal("tag_reset not registered")
	}

	result, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("tag_reset error: %v", err)
	}
	if !strings.Contains(result, "Tools removed: a1, a2, a3, a4, a5, b1, b2, b3, and 2 more.") {
		t.Fatalf("result = %q, want truncated tool list", result)
	}
}

func TestActivateCapability_EmptyTag(t *testing.T) {
	mgr := newMockCapabilityManager("ha")
	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, nil)

	tool := reg.Get("tag_activate")
	_, err := tool.Handler(context.Background(), map[string]any{"tag": ""})
	if err == nil {
		t.Error("expected error for empty tag")
	}
}

func TestDeactivateCapability_EmptyTag(t *testing.T) {
	mgr := newMockCapabilityManager("ha")
	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, nil)

	tool := reg.Get("tag_deactivate")
	_, err := tool.Handler(context.Background(), map[string]any{"tag": ""})
	if err == nil {
		t.Error("expected error for empty tag")
	}
}

func TestActivateCapability_DescriptionContainsManifest(t *testing.T) {
	mgr := newMockCapabilityManager("ha", "web")
	manifest := []CapabilityManifest{
		{Tag: "ha", Description: "Home Assistant tools", Tools: []string{"ha_get_state", "ha_call_service"}, Core: true},
		{Tag: "web", Description: "Web retrieval tools", Tools: []string{"web_search"}, Core: false},
	}

	reg := NewEmptyRegistry()
	reg.SetCapabilityTools(mgr, manifest)

	tool := reg.Get("tag_activate")

	// Always-active tags should NOT appear in the description (they can't be toggled).
	if strings.Contains(tool.Description, "**ha**") {
		t.Error("core tag 'ha' should not appear in description")
	}

	// Non-core tags should appear.
	if !strings.Contains(tool.Description, "**web**") {
		t.Errorf("description should mention 'web': %s", tool.Description)
	}
	if !strings.Contains(tool.Description, "(1 tools)") {
		t.Errorf("description should list tool count: %s", tool.Description)
	}
}

func TestBuildCapabilityManifest(t *testing.T) {
	tags := map[string][]string{
		"ha":  {"ha_get_state", "ha_call_service"},
		"web": {"web_search"},
	}
	descriptions := map[string]string{
		"ha":  "Home Assistant",
		"web": "Web retrieval",
	}
	core := map[string]bool{
		"ha": true,
	}

	manifest := BuildCapabilityManifest(tags, descriptions, core, nil)

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
	if !manifest[0].Core {
		t.Error("ha should be core")
	}
	if manifest[1].Core {
		t.Error("web should not be core")
	}
}

func TestRegistryFilterByTags(t *testing.T) {
	reg := NewEmptyRegistry()
	reg.Register(&Tool{Name: "ha_get_state", Description: "HA state"})
	reg.Register(&Tool{Name: "ha_call_service", Description: "HA service"})
	reg.Register(&Tool{Name: "web_search", Description: "Search"})
	reg.Register(&Tool{Name: "remember_fact", Description: "Memory"})

	reg.SetTagIndex(map[string][]string{
		"ha":     {"ha_get_state", "ha_call_service"},
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
			wantIn: []string{"ha_get_state", "ha_call_service", "web_search", "remember_fact"},
		},
		{
			name:   "empty tags returns all",
			tags:   []string{},
			wantIn: []string{"ha_get_state", "ha_call_service", "web_search", "remember_fact"},
		},
		{
			name:    "ha tag only",
			tags:    []string{"ha"},
			wantIn:  []string{"ha_get_state", "ha_call_service"},
			wantOut: []string{"web_search", "remember_fact"},
		},
		{
			name:    "web tag only",
			tags:    []string{"web"},
			wantIn:  []string{"web_search"},
			wantOut: []string{"ha_get_state", "ha_call_service", "remember_fact"},
		},
		{
			name:    "multiple tags",
			tags:    []string{"ha", "web"},
			wantIn:  []string{"ha_get_state", "ha_call_service", "web_search"},
			wantOut: []string{"remember_fact"},
		},
		{
			name:    "unknown tag filters to tagged-only",
			tags:    []string{"nonexistent"},
			wantOut: []string{"ha_get_state", "ha_call_service", "web_search", "remember_fact"},
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

func TestRegistryFilterByTags_CoreTools(t *testing.T) {
	reg := NewEmptyRegistry()
	// Tagged tools
	reg.Register(&Tool{Name: "ha_get_state", Description: "HA state"})
	reg.Register(&Tool{Name: "web_search", Description: "Search"})
	reg.Register(&Tool{Name: "loop_status", Description: "Inspect running loops"})
	reg.Register(&Tool{Name: "set_next_sleep", Description: "Adjust service loop sleep"})
	reg.Register(&Tool{Name: "send_notification", Description: "Send a notification"})
	reg.Register(&Tool{Name: "request_human_decision", Description: "Request a decision"})
	reg.Register(&Tool{Name: "macos_calendar_events", Description: "Read macOS calendar events"})
	// Core meta-tools (like tag_activate, tag_deactivate,
	// and tag_reset)
	reg.Register(&Tool{Name: "tag_activate", Description: "Activate a tag", Core: true})
	reg.Register(&Tool{Name: "tag_deactivate", Description: "Deactivate a tag", Core: true})
	reg.Register(&Tool{Name: "tag_reset", Description: "Reset tag state", Core: true})
	// Untagged tool WITHOUT Core — should be filtered out
	reg.Register(&Tool{Name: "plain_untagged", Description: "Not tagged, not meta"})

	reg.SetTagIndex(map[string][]string{
		"ha":            {"ha_get_state"},
		"web":           {"web_search"},
		"core":          {"loop_status"},
		"loops":         {"loop_status", "set_next_sleep"},
		"notifications": {"send_notification", "request_human_decision"},
		"companion":     {"macos_calendar_events"},
	})

	tests := []struct {
		name    string
		tags    []string
		wantIn  []string
		wantOut []string
	}{
		{
			name:    "core tools survive ha-only filter",
			tags:    []string{"ha"},
			wantIn:  []string{"ha_get_state", "tag_activate", "tag_deactivate", "tag_reset"},
			wantOut: []string{"web_search", "plain_untagged"},
		},
		{
			name:    "core tools survive web-only filter",
			tags:    []string{"web"},
			wantIn:  []string{"web_search", "tag_activate", "tag_deactivate", "tag_reset"},
			wantOut: []string{"ha_get_state", "plain_untagged"},
		},
		{
			name:    "core tools survive unknown-tag filter",
			tags:    []string{"nonexistent"},
			wantIn:  []string{"tag_activate", "tag_deactivate", "tag_reset"},
			wantOut: []string{"ha_get_state", "web_search", "loop_status", "set_next_sleep", "send_notification", "request_human_decision", "macos_calendar_events", "plain_untagged"},
		},
		{
			name:   "core filter does not leak tagged non-core tools",
			tags:   []string{"core"},
			wantIn: []string{"loop_status", "tag_activate", "tag_deactivate", "tag_reset"},
			wantOut: []string{
				"ha_get_state",
				"web_search",
				"set_next_sleep",
				"send_notification",
				"request_human_decision",
				"macos_calendar_events",
				"plain_untagged",
			},
		},
		{
			name:   "nil tags returns everything",
			tags:   nil,
			wantIn: []string{"ha_get_state", "web_search", "loop_status", "set_next_sleep", "send_notification", "request_human_decision", "macos_calendar_events", "tag_activate", "tag_deactivate", "tag_reset", "plain_untagged"},
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
		"ha":  {"ha_get_state", "ha_call_service"},
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

// TestRegistryFilterByTags_PreservesTagIndex pins the convention every
// shallow-copy method on Registry follows: tagIndex propagates to the
// returned copy. Without it, tag-aware operations on the result
// misbehave in two ways — TaggedToolNames returns nil for every tag
// (the failure exercised below), and a chained FilterByTags call
// becomes a no-op early return that surfaces the full tool set
// instead of the narrower subset the caller asked for. The v0.9.3
// code-path audit caught this; FilterByTags was the lone outlier
// among the shallow-copy methods. Two paths matter: the no-filter
// early return and the active-filter path.
func TestRegistryFilterByTags_PreservesTagIndex(t *testing.T) {
	reg := NewEmptyRegistry()
	reg.Register(&Tool{Name: "ha_get_state"})
	reg.Register(&Tool{Name: "web_search"})
	index := map[string][]string{
		"ha":  {"ha_get_state"},
		"web": {"web_search"},
	}
	reg.SetTagIndex(index)

	t.Run("active-filter path", func(t *testing.T) {
		filtered := reg.FilterByTags([]string{"ha"})
		got := filtered.TaggedToolNames("ha")
		if len(got) == 0 {
			t.Fatalf("TaggedToolNames(ha) on filtered registry = %v, want non-empty (tagIndex lost across FilterByTags)", got)
		}
		if got[0] != "ha_get_state" {
			t.Errorf("TaggedToolNames(ha)[0] = %q, want ha_get_state", got[0])
		}
	})

	t.Run("no-filter path (empty tags slice)", func(t *testing.T) {
		filtered := reg.FilterByTags(nil)
		got := filtered.TaggedToolNames("ha")
		if len(got) == 0 {
			t.Fatalf("TaggedToolNames(ha) on no-filter copy = %v, want non-empty (tagIndex lost on no-op path)", got)
		}
	})
}

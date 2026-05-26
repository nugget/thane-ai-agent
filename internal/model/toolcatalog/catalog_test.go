package toolcatalog

import (
	"strings"
	"testing"
)

func TestBuildCapabilitySurface_SortsTagsAndTools(t *testing.T) {
	surface := BuildCapabilitySurface(
		map[string][]string{
			"interactive": nil,
			"web":         {"web_search", "web_fetch"},
			"forge":       {"forge_pr_get", "forge_search"},
		},
		map[string]string{
			"interactive": "Interactive loop guidance",
			"web":         "Web tools",
			"forge":       "Forge tools",
		},
		map[string]bool{
			"forge": true,
		},
		map[string]bool{
			"web": true,
		},
	)

	if len(surface) != 3 {
		t.Fatalf("len(surface) = %d, want 3", len(surface))
	}
	if got := []string{surface[0].Tag, surface[1].Tag, surface[2].Tag}; strings.Join(got, ",") != "forge,interactive,web" {
		t.Fatalf("tags = %v, want [forge interactive web]", got)
	}
	if !surface[0].Core {
		t.Fatal("forge should be always active")
	}
	if !surface[1].Menu {
		t.Fatal("interactive should be a menu tag")
	}
	if !surface[2].Protected {
		t.Fatal("web should be protected")
	}
	if got := strings.Join(surface[2].Tools, ","); got != "web_fetch,web_search" {
		t.Fatalf("web tools = %q", got)
	}
}

func TestBuiltinToolCatalogIncludesCoreTools(t *testing.T) {
	// The canonical core set: 11 tools that survive
	// capability-tag filtering regardless of scope. list_loaded_capabilities
	// was previously a 12th member but was demoted because its output is
	// a strict subset of the "## Active Capabilities" section already
	// rendered into every prompt (see capability_tools.go's
	// registerListLoadedCapabilities Godoc for the full rationale).
	names := []string{
		"activate_capability",
		"deactivate_capability",
		"reset_capabilities",
		"inspect_capability",
		"activate_lens",
		"deactivate_lens",
		"list_lenses",
		"thane_now",
		"thane_assign",
		"request_core_attention",
		"logs_query",
	}
	for _, name := range names {
		spec, ok := LookupBuiltinToolSpec(name)
		if !ok {
			t.Fatalf("LookupBuiltinToolSpec(%q) not found", name)
		}
		if spec.CanonicalID != "native:"+name {
			t.Fatalf("LookupBuiltinToolSpec(%q).CanonicalID = %q, want native:%s", name, spec.CanonicalID, name)
		}
		if spec.Source != NativeToolSource {
			t.Fatalf("LookupBuiltinToolSpec(%q).Source = %q, want %q", name, spec.Source, NativeToolSource)
		}
	}
}

func TestBuiltinToolCatalogIncludesLoopIntentFrontDoors(t *testing.T) {
	spec, ok := LookupBuiltinToolSpec("thane_create_container")
	if !ok {
		t.Fatal("thane_create_container missing from builtin tool catalog")
	}
	if !containsString(spec.Tags, "loops") {
		t.Fatalf("thane_create_container tags = %v, want loops", spec.Tags)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRenderLoadedCapabilitySummary_UsesDescriptionsAndFallsBackForUnknownTags(t *testing.T) {
	summary := RenderLoadedCapabilitySummary([]CapabilitySurface{
		{
			Tag:         "forge",
			Description: "Forge and code review tools.",
			Tools:       []string{"forge_pr_get", "forge_search"},
		},
	}, map[string]bool{
		"forge":   true,
		"unknown": true,
	})

	if !strings.Contains(summary, "\"kind\":\"loaded_capabilities\"") {
		t.Fatalf("summary = %q, want loaded-capabilities kind", summary)
	}
	if !strings.Contains(summary, "\"tag\":\"forge\"") {
		t.Fatalf("summary = %q, want forge detail", summary)
	}
	if !strings.Contains(summary, "\"description\":\"Forge and code review tools.\"") {
		t.Fatalf("summary = %q, want forge description", summary)
	}
	if !strings.Contains(summary, "\"tool_count\":2") {
		t.Fatalf("summary = %q, want forge tool_count", summary)
	}
	if !strings.Contains(summary, "\"tag\":\"unknown\"") {
		t.Fatalf("summary = %q, want unknown-tag fallback", summary)
	}
}

func TestRenderLoadedCapabilitySummary_EmptyStateExplainsAvailability(t *testing.T) {
	summary := RenderLoadedCapabilitySummary(nil, nil)
	if !strings.Contains(summary, "\"kind\":\"loaded_capabilities\"") {
		t.Fatalf("summary = %q, want loaded-capabilities kind", summary)
	}
	if !strings.Contains(summary, "\"loaded_capabilities\":[]") {
		t.Fatalf("summary = %q, want empty loaded_capabilities array", summary)
	}
	if strings.Contains(summary, "\"activation_tools\"") {
		t.Fatalf("summary = %q, want loaded-capabilities summary to stay state-only", summary)
	}
}

func TestRenderCapabilityManifestMarkdown_UsesExactToolNames(t *testing.T) {
	manifest := RenderCapabilityManifestMarkdown([]CapabilitySurface{
		{Tag: "development", Description: "Development trailhead.", Teaser: "Activate when the next move is about code or repos.", NextTags: []string{"forge", "files", "web"}, Menu: true},
		{Tag: "forge", Description: "Forge tools.", Tools: []string{"forge_pr_get"}},
	})
	if !strings.Contains(manifest, "\"kind\":\"capability_menu\"") {
		t.Fatalf("manifest = %q, want capability_menu kind", manifest)
	}
	if !strings.Contains(manifest, "\"activate\":\"activate_capability\"") {
		t.Fatalf("manifest = %q, want activate_capability example", manifest)
	}
	if !strings.Contains(manifest, "\"reset\":\"reset_capabilities\"") {
		t.Fatalf("manifest = %q, want reset_capabilities example", manifest)
	}
	if !strings.Contains(manifest, "\"inspect\":\"inspect_capability\"") {
		t.Fatalf("manifest = %q, want inspect_capability example", manifest)
	}
	if !strings.Contains(manifest, "\"delegate\":\"thane_now\"") {
		t.Fatalf("manifest = %q, want thane_now example", manifest)
	}
	if !strings.Contains(manifest, "\"development\"") {
		t.Fatalf("manifest = %q, want development menu entry", manifest)
	}
	if !strings.Contains(manifest, "\"teaser\":\"Activate when the next move is about code or repos.\"") {
		t.Fatalf("manifest = %q, want teaser", manifest)
	}
	if !strings.Contains(manifest, "\"next_tags\":[\"forge\",\"files\",\"web\"]") {
		t.Fatalf("manifest = %q, want next_tags", manifest)
	}
	if strings.Contains(manifest, "\"tag\":\"\"") {
		t.Fatalf("manifest = %q, want menu entries keyed by tag without empty inner tag fields", manifest)
	}
	if strings.Contains(manifest, "\"forge\":{") {
		t.Fatalf("manifest = %q, want non-menu forge hidden from menu entries", manifest)
	}
}

func TestRenderCapabilityActivationDescription_ShowsMenuTags(t *testing.T) {
	desc := RenderCapabilityActivationDescription([]CapabilitySurface{
		{Tag: "development", Description: "Development trailhead.", Teaser: "Activate when the next move is about code or repos.", NextTags: []string{"forge", "files", "web"}, Menu: true},
		{Tag: "forge", Description: "Forge tools.", Tools: []string{"forge_pr_get"}},
		{Tag: "owner", Description: "Owner guidance.", Menu: true, Protected: true},
	})

	if !strings.Contains(desc, "coarse-to-fine menu") {
		t.Fatalf("description = %q, want coarse-to-fine guidance", desc)
	}
	if !strings.Contains(desc, "`reset_capabilities`") {
		t.Fatalf("description = %q, want reset_capabilities exact tool name", desc)
	}
	if !strings.Contains(desc, "**development**") {
		t.Fatalf("description = %q, want development menu bullet", desc)
	}
	if !strings.Contains(desc, "Activate when the next move is about code or repos.") {
		t.Fatalf("description = %q, want teaser wording", desc)
	}
	if !strings.Contains(desc, "next: forge, files, web") {
		t.Fatalf("description = %q, want next-tags hint", desc)
	}
	if strings.Contains(desc, "**forge**") {
		t.Fatalf("description = %q, want forge omitted from top-level menu", desc)
	}
	if !strings.Contains(desc, "**owner**") {
		t.Fatalf("description = %q, want protected owner menu bullet", desc)
	}
	if !strings.Contains(desc, "protected, trustworthy when present, not manually activatable") {
		t.Fatalf("description = %q, want protected owner status note", desc)
	}
}

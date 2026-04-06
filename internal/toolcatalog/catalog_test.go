package toolcatalog

import (
	"strings"
	"testing"
)

func TestBuildCapabilitySurface_SortsTagsAndTools(t *testing.T) {
	surface := BuildCapabilitySurface(
		map[string][]string{
			"interactive": nil,
			"search":      {"web_search", "web_fetch"},
			"forge":       {"forge_pr_get", "forge_search"},
		},
		map[string]string{
			"interactive": "Interactive loop guidance",
			"search":      "Search tools",
			"forge":       "Forge tools",
		},
		map[string]bool{
			"forge": true,
		},
		map[string]bool{
			"search": true,
		},
	)

	if len(surface) != 3 {
		t.Fatalf("len(surface) = %d, want 3", len(surface))
	}
	if got := []string{surface[0].Tag, surface[1].Tag, surface[2].Tag}; strings.Join(got, ",") != "forge,interactive,search" {
		t.Fatalf("tags = %v, want [forge interactive search]", got)
	}
	if !surface[0].AlwaysActive {
		t.Fatal("forge should be always active")
	}
	if !surface[1].Menu {
		t.Fatal("interactive should be a menu tag")
	}
	if !surface[2].Protected {
		t.Fatal("search should be protected")
	}
	if got := strings.Join(surface[2].Tools, ","); got != "web_fetch,web_search" {
		t.Fatalf("search tools = %q", got)
	}
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
	if !strings.Contains(summary, "\"list\":\"list_loaded_capabilities\"") {
		t.Fatalf("summary = %q, want list_loaded_capabilities helper", summary)
	}
}

func TestRenderCapabilityManifestMarkdown_UsesExactToolNames(t *testing.T) {
	manifest := RenderCapabilityManifestMarkdown([]CapabilitySurface{
		{Tag: "development", Description: "Development entry point.", Teaser: "Activate when the next move is about code or repos.", NextTags: []string{"forge", "files", "search"}, Menu: true},
		{Tag: "forge", Description: "Forge tools.", Tools: []string{"forge_pr_get"}},
	})
	if !strings.Contains(manifest, "\"kind\":\"capability_menu\"") {
		t.Fatalf("manifest = %q, want capability_menu kind", manifest)
	}
	if !strings.Contains(manifest, "\"activate\":\"activate_capability\"") {
		t.Fatalf("manifest = %q, want activate_capability example", manifest)
	}
	if !strings.Contains(manifest, "\"list\":\"list_loaded_capabilities\"") {
		t.Fatalf("manifest = %q, want list_loaded_capabilities example", manifest)
	}
	if !strings.Contains(manifest, "\"delegate\":\"thane_delegate\"") {
		t.Fatalf("manifest = %q, want thane_delegate example", manifest)
	}
	if !strings.Contains(manifest, "\"menu_entries_are_not_loaded\":true") {
		t.Fatalf("manifest = %q, want menu-vs-loaded flag", manifest)
	}
	if !strings.Contains(manifest, "\"invented_capability_tool_names_are_invalid\":true") {
		t.Fatalf("manifest = %q, want anti-invention flag", manifest)
	}
	if !strings.Contains(manifest, "\"development\"") {
		t.Fatalf("manifest = %q, want development menu entry", manifest)
	}
	if !strings.Contains(manifest, "\"teaser\":\"Activate when the next move is about code or repos.\"") {
		t.Fatalf("manifest = %q, want teaser", manifest)
	}
	if !strings.Contains(manifest, "\"next_tags\":[\"forge\",\"files\",\"search\"]") {
		t.Fatalf("manifest = %q, want next_tags", manifest)
	}
	if strings.Contains(manifest, "\"forge\":{") {
		t.Fatalf("manifest = %q, want non-menu forge hidden from menu entries", manifest)
	}
}

func TestRenderCapabilityActivationDescription_ShowsMenuTags(t *testing.T) {
	desc := RenderCapabilityActivationDescription([]CapabilitySurface{
		{Tag: "development", Description: "Development entry point.", Teaser: "Activate when the next move is about code or repos.", NextTags: []string{"forge", "files", "search"}, Menu: true},
		{Tag: "forge", Description: "Forge tools.", Tools: []string{"forge_pr_get"}},
		{Tag: "owner", Description: "Owner guidance.", Menu: true, Protected: true},
	})

	if !strings.Contains(desc, "coarse-to-fine menu") {
		t.Fatalf("description = %q, want coarse-to-fine guidance", desc)
	}
	if !strings.Contains(desc, "**development**") {
		t.Fatalf("description = %q, want development menu bullet", desc)
	}
	if !strings.Contains(desc, "Activate when the next move is about code or repos.") {
		t.Fatalf("description = %q, want teaser wording", desc)
	}
	if !strings.Contains(desc, "next: forge, files, search") {
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

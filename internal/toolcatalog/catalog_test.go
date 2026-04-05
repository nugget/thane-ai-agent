package toolcatalog

import (
	"strings"
	"testing"
)

func TestBuildCapabilitySurface_SortsTagsAndTools(t *testing.T) {
	surface := BuildCapabilitySurface(
		map[string][]string{
			"search": {"web_search", "web_fetch"},
			"forge":  {"forge_pr_get", "forge_search"},
		},
		map[string]string{
			"search": "Search tools",
			"forge":  "Forge tools",
		},
		map[string]bool{
			"forge": true,
		},
	)

	if len(surface) != 2 {
		t.Fatalf("len(surface) = %d, want 2", len(surface))
	}
	if surface[0].Tag != "forge" || surface[1].Tag != "search" {
		t.Fatalf("tags = [%s %s], want [forge search]", surface[0].Tag, surface[1].Tag)
	}
	if !surface[0].AlwaysActive {
		t.Fatal("forge should be always active")
	}
	if got := strings.Join(surface[1].Tools, ","); got != "web_fetch,web_search" {
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
}

func TestRenderCapabilityManifestMarkdown_UsesExactToolNames(t *testing.T) {
	manifest := RenderCapabilityManifestMarkdown([]CapabilitySurface{
		{Tag: "forge", Description: "Forge tools.", Tools: []string{"forge_pr_get"}},
	})
	if !strings.Contains(manifest, "\"kind\":\"capability_catalog\"") {
		t.Fatalf("manifest = %q, want capability_catalog kind", manifest)
	}
	if !strings.Contains(manifest, "\"activate\":\"activate_capability\"") {
		t.Fatalf("manifest = %q, want activate_capability example", manifest)
	}
	if !strings.Contains(manifest, "\"delegate\":\"thane_delegate\"") {
		t.Fatalf("manifest = %q, want thane_delegate example", manifest)
	}
	if !strings.Contains(manifest, "\"catalog_entries_are_not_loaded\":true") {
		t.Fatalf("manifest = %q, want available-vs-loaded flag", manifest)
	}
	if !strings.Contains(manifest, "\"invented_capability_tool_names_are_invalid\":true") {
		t.Fatalf("manifest = %q, want anti-invention flag", manifest)
	}
}

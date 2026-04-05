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

	if !strings.Contains(summary, "`forge`: Forge and code review tools. (2 tools loaded)") {
		t.Fatalf("summary = %q, want forge detail", summary)
	}
	if !strings.Contains(summary, "`unknown`: active capability tag.") {
		t.Fatalf("summary = %q, want unknown-tag fallback", summary)
	}
}

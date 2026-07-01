package agent

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

func TestBuildSystemPrompt_ActiveCapabilitiesUseSharedSurface(t *testing.T) {
	l := newTagTestLoop()
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"forge": {
			Description: "Forge and code review tools.",
			Tools:       []string{"forge_pr_get", "forge_search"},
			Core:        true,
		},
	}, nil)
	l.UseCapabilitySurface([]toolcatalog.CapabilitySurface{
		{
			Tag:         "forge",
			Description: "Forge and code review tools.",
			Tools:       []string{"forge_pr_get", "forge_search"},
			Core:        true,
		},
	})

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello")
	if !strings.Contains(prompt, "## Active Tags") {
		t.Fatalf("prompt missing Active Tags section: %s", prompt)
	}
	if !strings.Contains(prompt, "\"kind\":\"loaded_capabilities\"") {
		t.Fatalf("prompt missing loaded-capabilities kind: %s", prompt)
	}
	if !strings.Contains(prompt, "\"tag\":\"forge\"") {
		t.Fatalf("prompt missing forge tag in shared surface summary: %s", prompt)
	}
	if !strings.Contains(prompt, "\"tool_count\":2") {
		t.Fatalf("prompt missing shared surface summary: %s", prompt)
	}
}

func TestBuildSystemPrompt_ActiveCapabilitiesEmptyStateUsesSharedSurface(t *testing.T) {
	l := newTagTestLoop()
	l.UseCapabilitySurface([]toolcatalog.CapabilitySurface{
		{
			Tag:         "forge",
			Description: "Forge and code review tools.",
			Tools:       []string{"forge_pr_get", "forge_search"},
		},
	})

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello")
	if !strings.Contains(prompt, "## Active Tags") {
		t.Fatalf("prompt missing Active Tags section: %s", prompt)
	}
	if !strings.Contains(prompt, "\"kind\":\"loaded_capabilities\"") {
		t.Fatalf("prompt missing loaded-capabilities kind: %s", prompt)
	}
	if !strings.Contains(prompt, "\"loaded_capabilities\":[]") {
		t.Fatalf("prompt missing empty loaded_capabilities array: %s", prompt)
	}
}

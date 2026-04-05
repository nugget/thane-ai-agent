package agent

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/toolcatalog"
)

func TestBuildSystemPrompt_ActiveCapabilitiesUseSharedSurface(t *testing.T) {
	l := newTagTestLoop()
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"forge": {
			Description:  "Forge and code review tools.",
			Tools:        []string{"forge_pr_get", "forge_search"},
			AlwaysActive: true,
		},
	}, nil)
	l.UseCapabilitySurface([]toolcatalog.CapabilitySurface{
		{
			Tag:          "forge",
			Description:  "Forge and code review tools.",
			Tools:        []string{"forge_pr_get", "forge_search"},
			AlwaysActive: true,
		},
	})

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)
	if !strings.Contains(prompt, "## Active Capabilities") {
		t.Fatalf("prompt missing Active Capabilities section: %s", prompt)
	}
	if !strings.Contains(prompt, "`forge`: Forge and code review tools. (2 tools loaded)") {
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

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)
	if !strings.Contains(prompt, "## Active Capabilities") {
		t.Fatalf("prompt missing Active Capabilities section: %s", prompt)
	}
	if !strings.Contains(prompt, "None loaded right now") {
		t.Fatalf("prompt missing empty-state guidance: %s", prompt)
	}
	if !strings.Contains(prompt, "`activate_capability`") {
		t.Fatalf("prompt missing activate_capability guidance: %s", prompt)
	}
}

package app

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestResolveCapabilityTags_UsesRegistryMetadataAsBaseline(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.Tool{
		Name:        "web_search",
		Description: "Search the web",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", nil
		},
	})
	reg.Register(&tools.Tool{
		Name:        "exec",
		Description: "Run shell commands",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", nil
		},
	})

	resolved := resolveCapabilityTags(reg, nil)
	if _, ok := resolved["search"]; !ok {
		t.Fatalf("expected search tag in resolved catalog")
	}
	if _, ok := resolved["shell"]; !ok {
		t.Fatalf("expected shell tag in resolved catalog")
	}
	if len(resolved["search"].Tools) != 1 || resolved["search"].Tools[0] != "web_search" {
		t.Fatalf("search tools = %#v", resolved["search"].Tools)
	}
	if resolved["search"].Description == "" {
		t.Fatal("search description should be populated")
	}
}

func TestResolveCapabilityTags_ConfigOverridesReplaceToolsAndDescription(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.Tool{
		Name:        "web_search",
		Description: "Search the web",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", nil
		},
	})

	resolved := resolveCapabilityTags(reg, map[string]config.CapabilityTagConfig{
		"search": {
			Description: "Custom search surface",
			Tools:       []string{"web_fetch"},
		},
		"review": {
			Description: "Custom review tools",
			Tools:       []string{"file_read", "file_search"},
		},
	})

	if resolved["search"].Description != "Custom search surface" {
		t.Fatalf("search description = %q", resolved["search"].Description)
	}
	if len(resolved["search"].Tools) != 1 || resolved["search"].Tools[0] != "web_fetch" {
		t.Fatalf("search tools = %#v", resolved["search"].Tools)
	}
	if len(resolved["review"].Tools) != 2 {
		t.Fatalf("review tools = %#v", resolved["review"].Tools)
	}
}

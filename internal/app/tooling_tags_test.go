package app

import (
	"context"
	"slices"
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
	if _, ok := resolved["web"]; !ok {
		t.Fatalf("expected web tag in resolved catalog")
	}
	if _, ok := resolved["shell"]; !ok {
		t.Fatalf("expected shell tag in resolved catalog")
	}
	if len(resolved["web"].Tools) != 1 || resolved["web"].Tools[0] != "web_search" {
		t.Fatalf("web tools = %#v", resolved["web"].Tools)
	}
	if resolved["web"].Description == "" {
		t.Fatal("web description should be populated")
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
		"web": {
			Description: "Custom web surface",
			Tools:       []string{"web_fetch"},
		},
		"review": {
			Description: "Custom review tools",
			Tools:       []string{"file_read", "file_search"},
		},
	})

	if resolved["web"].Description != "Custom web surface" {
		t.Fatalf("web description = %q", resolved["web"].Description)
	}
	if len(resolved["web"].Tools) != 1 || resolved["web"].Tools[0] != "web_fetch" {
		t.Fatalf("web tools = %#v", resolved["web"].Tools)
	}
	if len(resolved["review"].Tools) != 2 {
		t.Fatalf("review tools = %#v", resolved["review"].Tools)
	}
}

func TestResolveCapabilityTags_SortsBaselineTools(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.Tool{
		Name:        "web_search",
		Description: "Search the web",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", nil
		},
	})
	reg.Register(&tools.Tool{
		Name:        "web_fetch",
		Description: "Fetch a page",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", nil
		},
	})

	got := resolvedToolNames(resolveCapabilityTags(reg, nil), "web")
	want := append([]string(nil), got...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("web tools = %#v, want sorted %#v", got, want)
	}
}

func resolvedToolNames(resolved map[string]config.CapabilityTagConfig, tag string) []string {
	spec, ok := resolved[tag]
	if !ok {
		return nil
	}
	return spec.Tools
}

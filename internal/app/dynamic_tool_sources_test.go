package app

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/integrations/mcp"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestCapabilityTagExcludes(t *testing.T) {
	t.Parallel()
	if got := capabilityTagExcludes(&config.Config{}); got != nil {
		t.Fatalf("no capability tags = %v, want nil", got)
	}
	cfg := &config.Config{CapabilityTags: map[string]config.CapabilityTagConfig{
		"game":  {Exclude: []string{" mcp_game_reset_world ", "", "mcp_game_quit"}},
		"web":   {Include: []string{"web_fetch"}},
		"blank": {Exclude: []string{"  "}},
	}}
	got := capabilityTagExcludes(cfg)
	if len(got) != 1 {
		t.Fatalf("excludes = %v, want only the game tag", got)
	}
	for _, name := range []string{"mcp_game_reset_world", "mcp_game_quit"} {
		if _, ok := got["game"][name]; !ok {
			t.Errorf("game excludes %v missing %q", got["game"], name)
		}
	}
	if len(got["game"]) != 2 {
		t.Errorf("game excludes = %v, want two trimmed names", got["game"])
	}
}

func TestDropExcludedTagNames(t *testing.T) {
	t.Parallel()
	excludes := map[string]map[string]struct{}{
		"game":  {"mcp_game_reset_world": {}},
		"admin": {"mcp_game_reset_world": {}},
	}
	names := []string{"mcp_game_move", "mcp_game_reset_world"}
	tags := map[string][]string{
		"game":  names,
		"admin": {"mcp_game_reset_world"},
		"fun":   {"mcp_game_reset_world"},
	}
	got := dropExcludedTagNames(tags, excludes)
	if !slices.Equal(got["game"], []string{"mcp_game_move"}) {
		t.Errorf("game = %v, want the excluded name dropped", got["game"])
	}
	if _, ok := got["admin"]; ok {
		t.Errorf("admin = %v, want the tag removed once empty", got["admin"])
	}
	if !slices.Equal(got["fun"], []string{"mcp_game_reset_world"}) {
		t.Errorf("fun = %v, want it kept: excludes are per tag", got["fun"])
	}
	if !slices.Equal(names, []string{"mcp_game_move", "mcp_game_reset_world"}) {
		t.Errorf("input name slice written into: %v", names)
	}
	if got := dropExcludedTagNames(nil, excludes); got != nil {
		t.Errorf("nil tags = %v, want nil", got)
	}
}

// TestDynamicToolSourcesHonorTagExcludes drives the installed overlay
// source end to end: a tool the operator excluded from a tag must not
// come back under that tag through the per-run dynamic overlay.
func TestDynamicToolSourcesHonorTagExcludes(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)
	registry := companion.NewRegistry(logger)
	registry.Add(&companion.Provider{ID: "p1", Account: "mac", ClientID: "c1"})
	if err := registry.RegisterCapabilities("p1", []companion.Capability{{
		Name: "contacts",
		Tools: []companion.ToolDefinition{
			{Name: "macos_find_person", Method: "find", Tags: []string{"people"}},
			{Name: "macos_delete_person", Method: "delete", Tags: []string{"people"}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{CapabilityTags: map[string]config.CapabilityTagConfig{
		"people": {Exclude: []string{"macos_delete_person"}},
	}}
	src := newDynamicToolSources(cfg, mcp.NewManager(nil, logger), tools.NewCompanionRegistrar(registry, time.UTC, logger))

	got, tags := src.Snapshot()
	if len(got) != 2 {
		t.Fatalf("snapshot tools = %d, want both companion tools", len(got))
	}
	if !slices.Equal(tags["people"], []string{"macos_find_person"}) {
		t.Errorf("people = %v, want the excluded tool dropped", tags["people"])
	}
	if !slices.Contains(tags["companion"], "macos_delete_person") {
		t.Errorf("companion = %v, want the tool kept under tags that do not exclude it", tags["companion"])
	}

	// The run registry the loop builds from this snapshot.
	base := tools.NewEmptyRegistry()
	base.SetTagIndex(map[string][]string{"people": nil})
	names := base.WithDynamicTools(got, tags).FilterByTags([]string{"people"}).AllToolNames()
	if slices.Contains(names, "macos_delete_person") || !slices.Contains(names, "macos_find_person") {
		t.Errorf("people run tools = %v, want only macos_find_person", names)
	}
}

// TestMCPOverlayTagExcludesHoldInRunRegistry mirrors what the loop does
// with the MCP overlay: the static tag index comes from capability-tag
// resolution, then the per-run overlay merges the dynamic tag
// additions. An operator exclude must hold both when a static tool's
// definition changed after startup (the overlay re-adds it under the
// same name) and when the server first came up after startup.
func TestMCPOverlayTagExcludesHoldInRunRegistry(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		CapabilityTags: map[string]config.CapabilityTagConfig{
			"game": {Exclude: []string{"mcp_game_reset_world"}},
		},
		MCP: config.MCPConfig{Servers: []config.MCPServerConfig{{Name: "game", Tags: []string{"game"}}}},
	}
	mcpTool := func(name, desc string) *tools.Tool {
		return &tools.Tool{
			Name: name, Description: desc, Source: "mcp", Origin: "game", Tags: []string{"game"},
			Handler: func(context.Context, map[string]any) (string, error) { return "", nil },
		}
	}
	// What the MCP overlay emits for these tools: each name under its tags.
	overlayTags := func(ts ...*tools.Tool) map[string][]string {
		out := map[string][]string{}
		for _, tool := range ts {
			for _, tag := range tool.Tags {
				out[tag] = append(out[tag], tool.Name)
			}
		}
		return out
	}
	// The startup registry with the tag index Loop.SetCapabilityTags installs.
	staticRegistry := func(ts ...*tools.Tool) *tools.Registry {
		reg := tools.NewEmptyRegistry()
		for _, tool := range ts {
			reg.Register(tool)
		}
		resolved := resolveCapabilityTags(reg, mcpCapabilityTagOverrides(cfg))
		idx := make(map[string][]string, len(resolved.Configs))
		for tag, tagCfg := range resolved.Configs {
			idx[tag] = tagCfg.Tools
		}
		reg.SetTagIndex(idx)
		return reg
	}
	// The overlay as dynamicToolSources.Snapshot hands it to the loop.
	overlay := func(ts ...*tools.Tool) ([]*tools.Tool, map[string][]string) {
		merged, tags := mergeDynamicSnapshots(ts, overlayTags(ts...), nil, nil)
		return merged, dropExcludedTagNames(tags, capabilityTagExcludes(cfg))
	}

	t.Run("drifted static tool", func(t *testing.T) {
		t.Parallel()
		reg := staticRegistry(mcpTool("mcp_game_move", "move"), mcpTool("mcp_game_reset_world", "reset"))
		if names := reg.FilterByTags([]string{"game"}).AllToolNames(); slices.Contains(names, "mcp_game_reset_world") {
			t.Fatalf("static game tools = %v, want the exclude applied at startup", names)
		}
		names := reg.WithDynamicTools(overlay(mcpTool("mcp_game_reset_world", "reworded"))).
			FilterByTags([]string{"game"}).AllToolNames()
		if slices.Contains(names, "mcp_game_reset_world") || !slices.Contains(names, "mcp_game_move") {
			t.Errorf("game run tools after drift = %v, want mcp_game_move without mcp_game_reset_world", names)
		}
	})
	t.Run("server up after startup", func(t *testing.T) {
		t.Parallel()
		names := staticRegistry().
			WithDynamicTools(overlay(mcpTool("mcp_game_move", "move"), mcpTool("mcp_game_reset_world", "reset"))).
			FilterByTags([]string{"game"}).AllToolNames()
		if slices.Contains(names, "mcp_game_reset_world") || !slices.Contains(names, "mcp_game_move") {
			t.Errorf("game run tools = %v, want mcp_game_move without mcp_game_reset_world", names)
		}
	})
}

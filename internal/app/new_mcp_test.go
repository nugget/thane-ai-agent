package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/integrations/mcp"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestMergeDynamicSnapshots(t *testing.T) {
	t.Parallel()

	if got, tags := mergeDynamicSnapshots(nil, nil, nil, nil); got != nil || tags != nil {
		t.Fatalf("empty merge = %v, %v; want nil, nil", got, tags)
	}

	mcpTool := &tools.Tool{Name: "mcp_s_late"}
	macTool := &tools.Tool{Name: "macos_thing"}
	mcpTools := []*tools.Tool{mcpTool}
	mcpTags := map[string][]string{"shared": {"mcp_s_late"}, "work": {"mcp_s_late"}}
	macTools := make([]*tools.Tool, 1, 4) // spare capacity must not be appended into
	macTools[0] = macTool
	macTags := map[string][]string{"shared": {"macos_thing"}, "companion": {"macos_thing"}}

	got, tags := mergeDynamicSnapshots(mcpTools, mcpTags, macTools, macTags)
	if len(got) != 2 || got[0] != mcpTool || got[1] != macTool {
		t.Fatalf("merged tools = %v", got)
	}
	if !slices.Equal(tags["shared"], []string{"mcp_s_late", "macos_thing"}) ||
		!slices.Equal(tags["work"], []string{"mcp_s_late"}) ||
		!slices.Equal(tags["companion"], []string{"macos_thing"}) {
		t.Fatalf("merged tags = %v", tags)
	}
	// Sources are untouched.
	if len(mcpTools) != 1 || len(macTools) != 1 || len(mcpTags["shared"]) != 1 || len(macTags["shared"]) != 1 {
		t.Fatal("merge mutated a source snapshot")
	}
	tags["shared"][0] = "changed"
	if mcpTags["shared"][0] != "mcp_s_late" {
		t.Fatal("merged tag slices alias a source")
	}
}

func TestDynamicToolSourcesNilAndEmpty(t *testing.T) {
	t.Parallel()
	if got, tags := (&dynamicToolSources{}).Snapshot(); got != nil || tags != nil {
		t.Fatalf("no sources = %v, %v", got, tags)
	}
	reg := tools.NewCompanionRegistrar(companion.NewRegistry(slog.Default()), time.UTC, slog.Default())
	mgr := mcp.NewManager(nil, slog.Default())
	if got, tags := (&dynamicToolSources{mcp: mgr, companion: reg}).Snapshot(); got != nil || tags != nil {
		t.Fatalf("empty sources = %v, %v", got, tags)
	}
}

func TestMCPServerConfigs(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	yes := true
	in := []config.MCPServerConfig{
		{
			Name: "files", Transport: "stdio", Command: "npx", Args: []string{"a"}, Env: []string{"K=V"},
			Cwd: "~/work", Protocol: "legacy", ConnectTimeout: time.Second, CallTimeout: time.Minute,
			IncludeTools: []string{"read"}, Tags: []string{"files"},
			Tools: map[string]config.MCPToolConfig{"read": {Enabled: &yes, Tags: []string{"x"}, Description: "d"}},
		},
		{Name: "web", Transport: "http", URL: "http://localhost:1/mcp", Cwd: "relative-ignored-for-http"},
	}
	out, err := mcpServerConfigs(in, nil)
	if err != nil {
		t.Fatalf("mcpServerConfigs: %v", err)
	}
	files, web := out[0], out[1]
	if files.Dir != filepath.Join(home, "work") || files.Protocol != "legacy" || files.ConnectTimeout != time.Second ||
		files.CallTimeout != time.Minute || !slices.Equal(files.Include, []string{"read"}) {
		t.Errorf("files = %+v", files)
	}
	if o := files.ToolOverrides["read"]; o.Enabled == nil || !*o.Enabled || o.Description != "d" || !slices.Equal(o.Tags, []string{"x"}) {
		t.Errorf("override = %+v", o)
	}
	if web.Dir != "" || web.URL != "http://localhost:1/mcp" {
		t.Errorf("web = %+v", web)
	}
	in[0].Args[0] = "changed"
	if files.Args[0] != "a" {
		t.Error("mapped config aliases the config's slices")
	}

	_, err = mcpServerConfigs([]config.MCPServerConfig{{Name: "rel", Transport: "stdio", Command: "x", Cwd: "relative/dir"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative cwd = %v, want an absolute-path error", err)
	}
}

func TestMCPCapabilityTagOverrides(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		CapabilityTags: map[string]config.CapabilityTagConfig{"ha": {Description: "home"}},
		MCP: config.MCPConfig{Servers: []config.MCPServerConfig{
			{Name: "s", Tags: []string{" game ", "", "ha"}, Tools: map[string]config.MCPToolConfig{"t": {Tags: []string{"admin"}}}},
		}},
	}
	got := mcpCapabilityTagOverrides(cfg)
	if got["ha"].Description != "home" {
		t.Errorf("configured tag lost: %+v", got["ha"])
	}
	for _, tag := range []string{"game", "admin"} {
		if _, ok := got[tag]; !ok {
			t.Errorf("MCP tag %q missing from overrides %v", tag, got)
		}
	}
	if _, ok := got[""]; ok {
		t.Error("empty tag added")
	}
	if len(cfg.CapabilityTags) != 1 {
		t.Fatalf("config mutated: %v", cfg.CapabilityTags)
	}
}

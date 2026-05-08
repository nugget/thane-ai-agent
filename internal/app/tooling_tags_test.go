package app

import (
	"context"
	"database/sql"
	"log/slog"
	"slices"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/state/awareness"
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
	if _, ok := resolved.Configs["web"]; !ok {
		t.Fatalf("expected web tag in resolved catalog")
	}
	if _, ok := resolved.Configs["shell"]; !ok {
		t.Fatalf("expected shell tag in resolved catalog")
	}
	if got := resolved.Configs["web"].Tools; len(got) != 1 || got[0] != "web_search" {
		t.Fatalf("web tools = %#v", got)
	}
	if resolved.Configs["web"].Description == "" {
		t.Fatal("web description should be populated")
	}
	// Source attribution: native catalog declared web_search under web.
	entries := resolved.ToolEntries["web"]
	if len(entries) != 1 || entries[0].Name != "web_search" {
		t.Fatalf("web tool entries = %#v", entries)
	}
	if entries[0].Source.Kind != toolcatalog.ToolSourceNative {
		t.Errorf("web_search source kind = %q, want native", entries[0].Source.Kind)
	}
	if entries[0].Source.Origin != "" {
		t.Errorf("web_search source origin = %q, want empty for native", entries[0].Source.Origin)
	}
	if entries[0].State != nil {
		t.Errorf("active tool should have nil State, got %#v", entries[0].State)
	}
}

func TestResolveCapabilityTags_OverlayIncludeAddsTool(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.Tool{
		Name:        "web_search",
		Description: "Search the web",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", nil
		},
	})
	// Register a tool that does NOT declare the "web" tag in its catalog
	// metadata. The operator overlay should be able to add it via include.
	reg.Register(&tools.Tool{
		Name:        "extra_tool",
		Description: "An extra tool with no native tag",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", nil
		},
	})

	resolved := resolveCapabilityTags(reg, map[string]config.CapabilityTagConfig{
		"web": {
			Description: "Custom web surface",
			Include:     []string{"extra_tool"},
		},
	})

	if got := resolved.Configs["web"].Description; got != "Custom web surface" {
		t.Fatalf("web description = %q, want %q", got, "Custom web surface")
	}
	if got := resolved.Configs["web"].Tools; !slices.Contains(got, "web_search") {
		t.Fatalf("web tools missing native web_search; got %#v", got)
	}
	if got := resolved.Configs["web"].Tools; !slices.Contains(got, "extra_tool") {
		t.Fatalf("web tools missing overlay-included extra_tool; got %#v", got)
	}

	var extraEntry *toolcatalog.CapabilityToolEntry
	for i, e := range resolved.ToolEntries["web"] {
		if e.Name == "extra_tool" {
			extraEntry = &resolved.ToolEntries["web"][i]
			break
		}
	}
	if extraEntry == nil {
		t.Fatal("expected extra_tool entry under web tag")
	}
	if extraEntry.Source.Kind != toolcatalog.ToolSourceOverlay {
		t.Errorf("extra_tool source kind = %q, want overlay", extraEntry.Source.Kind)
	}
	if extraEntry.Source.Origin != "capability_tags.web.include" {
		t.Errorf("extra_tool source origin = %q, want capability_tags.web.include", extraEntry.Source.Origin)
	}
}

func TestResolveCapabilityTags_OverlayExcludeRemovesTool(t *testing.T) {
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

	resolved := resolveCapabilityTags(reg, map[string]config.CapabilityTagConfig{
		"web": {
			Exclude: []string{"web_fetch"},
		},
	})

	if got := resolved.Configs["web"].Tools; slices.Contains(got, "web_fetch") {
		t.Fatalf("web tools should not contain excluded web_fetch; got %#v", got)
	}
	if got := resolved.Configs["web"].Tools; !slices.Contains(got, "web_search") {
		t.Fatalf("web tools should still contain web_search; got %#v", got)
	}

	excluded := resolved.ExcludedTools["web"]
	if len(excluded) != 1 || excluded[0].Name != "web_fetch" {
		t.Fatalf("excluded entries = %#v, want web_fetch", excluded)
	}
	if excluded[0].State == nil || excluded[0].State.Status != toolcatalog.ToolStateExcluded {
		t.Errorf("excluded tool should have Status %q, got state %#v", toolcatalog.ToolStateExcluded, excluded[0].State)
	}
	if excluded[0].State.Reason != "capability_tags.web.exclude" {
		t.Errorf("excluded reason = %q, want capability_tags.web.exclude", excluded[0].State.Reason)
	}
	// Source attribution survives the move into excluded.
	if excluded[0].Source.Kind != toolcatalog.ToolSourceNative {
		t.Errorf("excluded source kind = %q, want native", excluded[0].Source.Kind)
	}
}

func TestResolveCapabilityTags_MCPSourceAttribution(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.Tool{
		Name:        "mcp_demo_search",
		Description: "Bridged MCP search tool",
		Source:      "mcp",
		Origin:      "demo",
		Tags:        []string{"web"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", nil
		},
	})

	resolved := resolveCapabilityTags(reg, nil)
	entries := resolved.ToolEntries["web"]
	if len(entries) != 1 || entries[0].Name != "mcp_demo_search" {
		t.Fatalf("expected single mcp_demo_search entry, got %#v", entries)
	}
	if entries[0].Source.Kind != toolcatalog.ToolSourceMCP {
		t.Errorf("source kind = %q, want mcp", entries[0].Source.Kind)
	}
	if entries[0].Source.Origin != "demo" {
		t.Errorf("source origin = %q, want demo", entries[0].Source.Origin)
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

	got := resolveCapabilityTags(reg, nil).Configs["web"].Tools
	want := append([]string(nil), got...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("web tools = %#v, want sorted %#v", got, want)
	}
}

// TestResolveCapabilityTags_IncludesWatchlistToolsAfterProvider is the
// regression test for issue #733. The watchlist tool provider
// contributes three tools (add/list/remove_context_entity) tagged
// "awareness" in the builtin catalog. resolveCapabilityTags must
// include them under that tag — but only if the snapshot is taken
// *after* the provider is registered. Pre-fix, initDelegation ran
// before initAwareness and the watchlist tools silently vanished from
// the awareness capability.
func TestResolveCapabilityTags_IncludesWatchlistToolsAfterProvider(t *testing.T) {
	reg := tools.NewEmptyRegistry()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := awareness.NewWatchlistStore(db, nil)
	if err != nil {
		t.Fatalf("new watchlist store: %v", err)
	}

	// Precondition: without the provider registered, the three
	// watchlist tools do not appear under the awareness tag. On an
	// empty registry, resolveCapabilityTags may not even surface an
	// "awareness" entry at all; either way the three tool names must
	// be absent. Record the baseline so the post-registration check
	// demonstrates a clean delta.
	before := resolveCapabilityTags(reg, nil)
	for _, name := range []string{"add_context_entity", "list_context_entities", "remove_context_entity"} {
		if slices.Contains(before.Configs["awareness"].Tools, name) {
			t.Fatalf("precondition: %q should not appear in awareness tag before provider registration", name)
		}
	}

	reg.RegisterProvider(awareness.NewWatchlistTools(awareness.WatchlistToolsConfig{Store: store}))

	after := resolveCapabilityTags(reg, nil)
	wantTools := []string{"add_context_entity", "list_context_entities", "remove_context_entity"}
	for _, name := range wantTools {
		if !slices.Contains(after.Configs["awareness"].Tools, name) {
			t.Errorf("awareness tag missing %q after provider registration; got %v",
				name, after.Configs["awareness"].Tools)
		}
	}
}

// TestResolveCapabilityTags_IncludesMQTTWakeToolsAfterSetSubscriptionTools
// is the other half of the #733 regression: mqtt_wake_* tools are
// registered in initServers, which runs after initDelegation but
// before finalizeCapabilityTags. Like the watchlist case, the tools
// must appear under their default tag ("mqtt") once the snapshot is
// taken at the right moment.
//
// This unit test exercises only the Registry ↔ resolver primitive;
// the init-phase ordering is enforced by the [finalizeCapabilityTags]
// function itself (see new.go) and documented in its doc comment.
func TestResolveCapabilityTags_IncludesMQTTWakeToolsAfterSetSubscriptionTools(t *testing.T) {
	reg := tools.NewEmptyRegistry()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	subStore, err := mqtt.NewSubscriptionStore(db, slog.Default())
	if err != nil {
		t.Fatalf("new mqtt subscription store: %v", err)
	}
	reg.RegisterProvider(mqtt.NewWakeTools(mqtt.NewTools(subStore)))

	resolved := resolveCapabilityTags(reg, nil)
	wantTools := []string{"mqtt_wake_add", "mqtt_wake_list", "mqtt_wake_remove"}
	for _, name := range wantTools {
		if !slices.Contains(resolved.Configs["mqtt"].Tools, name) {
			t.Errorf("mqtt tag missing %q after SetMQTTSubscriptionTools; got %v",
				name, resolved.Configs["mqtt"].Tools)
		}
	}
}

func TestMergeTalentMenuHints_UsesEntryPointFrontmatter(t *testing.T) {
	hints := mergeTalentMenuHints(nil, []talents.Talent{
		{
			Kind:     "entry_point",
			Tags:     []string{"development"},
			Teaser:   "Open when the work touches code, repos, issues, or PRs.",
			NextTags: []string{"forge", "files", "web"},
		},
		{
			Kind:   "doctrine",
			Tags:   []string{"forge"},
			Teaser: "should not surface",
		},
	})

	hint, ok := hints["development"]
	if !ok {
		t.Fatal("development hint missing")
	}
	if hint.Teaser != "Open when the work touches code, repos, issues, or PRs." {
		t.Fatalf("teaser = %q", hint.Teaser)
	}
	if !slices.Equal(hint.NextTags, []string{"forge", "files", "web"}) {
		t.Fatalf("next_tags = %#v, want forge/files/web", hint.NextTags)
	}
	if _, ok := hints["forge"]; ok {
		t.Fatalf("non-entry-point forge hint should not be present: %#v", hints["forge"])
	}
}

func TestMergeTalentMenuHints_PreservesExistingKBHint(t *testing.T) {
	hints := mergeTalentMenuHints(map[string]agent.KBMenuHint{
		"knowledge": {
			Teaser:   "KB hint wins.",
			NextTags: []string{"documents"},
		},
	}, []talents.Talent{
		{
			Kind:     "entry_point",
			Tags:     []string{"knowledge"},
			Teaser:   "Talent hint should not replace KB.",
			NextTags: []string{"files"},
		},
	})

	hint := hints["knowledge"]
	if hint.Teaser != "KB hint wins." {
		t.Fatalf("teaser = %q, want existing KB hint", hint.Teaser)
	}
	if !slices.Equal(hint.NextTags, []string{"documents"}) {
		t.Fatalf("next_tags = %#v, want existing KB next_tags", hint.NextTags)
	}
}

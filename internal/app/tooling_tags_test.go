package app

import (
	"context"
	"database/sql"
	"log/slog"
	"slices"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/nugget/thane-ai-agent/internal/awareness"
	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
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

	store, err := awareness.NewWatchlistStore(db)
	if err != nil {
		t.Fatalf("new watchlist store: %v", err)
	}

	// Before registration: the awareness tag exists but the watchlist
	// tools are absent from it because they haven't been registered
	// yet. Record the baseline so we can show the delta.
	before := resolveCapabilityTags(reg, nil)
	for _, name := range []string{"add_context_entity", "list_context_entities", "remove_context_entity"} {
		if slices.Contains(before["awareness"].Tools, name) {
			t.Fatalf("precondition: %q should not appear in awareness tag before provider registration", name)
		}
	}

	reg.RegisterProvider(awareness.NewWatchlistTools(awareness.WatchlistToolsConfig{Store: store}))

	after := resolveCapabilityTags(reg, nil)
	wantTools := []string{"add_context_entity", "list_context_entities", "remove_context_entity"}
	for _, name := range wantTools {
		if !slices.Contains(after["awareness"].Tools, name) {
			t.Errorf("awareness tag missing %q after provider registration; got %v",
				name, after["awareness"].Tools)
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
		if !slices.Contains(resolved["mqtt"].Tools, name) {
			t.Errorf("mqtt tag missing %q after SetMQTTSubscriptionTools; got %v",
				name, resolved["mqtt"].Tools)
		}
	}
}

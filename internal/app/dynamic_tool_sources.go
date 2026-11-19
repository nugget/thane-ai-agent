package app

import (
	"github.com/nugget/thane-ai-agent/internal/integrations/mcp"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// dynamicToolSources is the single value installed with
// Loop.SetDynamicToolSource. The loop has one slot; this merges the MCP
// overlay (tools that appeared after boot) and the companion registrar
// (Mac-authored tools). Either source may be nil.
//
// tagExcludes holds the operator's capability_tags.<tag>.exclude names
// (see capabilityTagExcludes). Startup tag resolution applies them to
// the static tag index only, so Snapshot applies them to the overlay's
// tag additions too; otherwise an excluded tool would rejoin its tag
// whenever it reached a run through the overlay.
type dynamicToolSources struct {
	mcp         *mcp.Manager
	companion   *tools.CompanionRegistrar
	tagExcludes map[string]map[string]struct{}
}

var _ agent.DynamicToolSource = (*dynamicToolSources)(nil)

// newDynamicToolSources builds the value initServers installs with
// Loop.SetDynamicToolSource from the MCP manager and the companion
// registrar (either may be nil), carrying the operator's
// capability_tags excludes so they apply to the overlay's tag additions
// as they do to the static tag index.
func newDynamicToolSources(cfg *config.Config, m *mcp.Manager, r *tools.CompanionRegistrar) *dynamicToolSources {
	return &dynamicToolSources{
		mcp:         m,
		companion:   r,
		tagExcludes: capabilityTagExcludes(cfg),
	}
}

// Snapshot merges both sources into a freshly allocated slice and map,
// MCP entries first, never appending into a source's own values, and
// drops the operator-excluded names from the merged tag additions. It
// returns (nil, nil) when both sources are empty, so the per-run overlay
// stays a no-op.
func (d *dynamicToolSources) Snapshot() ([]*tools.Tool, map[string][]string) {
	var mcpTools, companionTools []*tools.Tool
	var mcpTags, companionTags map[string][]string
	if d.mcp != nil {
		mcpTools, mcpTags = d.mcp.Snapshot()
	}
	if d.companion != nil {
		companionTools, companionTags = d.companion.Snapshot()
	}
	merged, tags := mergeDynamicSnapshots(mcpTools, mcpTags, companionTools, companionTags)
	return merged, dropExcludedTagNames(tags, d.tagExcludes)
}

// mergeDynamicSnapshots concatenates two snapshots into fresh values
// (first's tools before second's; per-tag names first then second). It
// returns (nil, nil) when both are empty.
func mergeDynamicSnapshots(firstTools []*tools.Tool, firstTags map[string][]string, secondTools []*tools.Tool, secondTags map[string][]string) ([]*tools.Tool, map[string][]string) {
	if len(firstTools) == 0 && len(secondTools) == 0 && len(firstTags) == 0 && len(secondTags) == 0 {
		return nil, nil
	}
	merged := make([]*tools.Tool, 0, len(firstTools)+len(secondTools))
	merged = append(merged, firstTools...)
	merged = append(merged, secondTools...)

	tags := make(map[string][]string, len(firstTags)+len(secondTags))
	for _, src := range []map[string][]string{firstTags, secondTags} {
		for tag, names := range src {
			combined := make([]string, 0, len(tags[tag])+len(names))
			combined = append(combined, tags[tag]...)
			combined = append(combined, names...)
			tags[tag] = combined
		}
	}
	return merged, tags
}

// dropExcludedTagNames removes excludes[tag] names from tags[tag] and
// deletes tags left with no names. It replaces entries of tags in place,
// so tags must be a map the caller owns (mergeDynamicSnapshots returns a
// fresh one), and returns it. Name slices are never written into. With
// no excludes it returns tags unchanged.
func dropExcludedTagNames(tags map[string][]string, excludes map[string]map[string]struct{}) map[string][]string {
	if len(tags) == 0 || len(excludes) == 0 {
		return tags
	}
	for tag, names := range tags {
		drop := excludes[tag]
		if len(drop) == 0 {
			continue
		}
		kept := make([]string, 0, len(names))
		for _, name := range names {
			if _, excluded := drop[name]; !excluded {
				kept = append(kept, name)
			}
		}
		if len(kept) == 0 {
			delete(tags, tag)
			continue
		}
		tags[tag] = kept
	}
	return tags
}

package app

import (
	"github.com/nugget/thane-ai-agent/internal/integrations/mcp"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// dynamicToolSources is the single value installed with
// Loop.SetDynamicToolSource. The loop has one slot; this merges the MCP
// overlay (tools that appeared after boot) and the companion registrar
// (Mac-authored tools). Either field may be nil.
type dynamicToolSources struct {
	mcp       *mcp.Manager
	companion *tools.CompanionRegistrar
}

var _ agent.DynamicToolSource = (*dynamicToolSources)(nil)

// Snapshot merges both sources into a freshly allocated slice and map,
// MCP entries first, never appending into a source's own values. It
// returns (nil, nil) when both are empty, so the per-run overlay stays a
// no-op.
func (d *dynamicToolSources) Snapshot() ([]*tools.Tool, map[string][]string) {
	var mcpTools, companionTools []*tools.Tool
	var mcpTags, companionTags map[string][]string
	if d.mcp != nil {
		mcpTools, mcpTags = d.mcp.Snapshot()
	}
	if d.companion != nil {
		companionTools, companionTags = d.companion.Snapshot()
	}
	return mergeDynamicSnapshots(mcpTools, mcpTags, companionTools, companionTags)
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

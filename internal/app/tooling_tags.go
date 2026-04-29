package app

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// resolvedCapabilityTags is the full output of [resolveCapabilityTags].
// Configs is the runtime-consumable map (with .Tools populated) used
// downstream by [agent.Loop.SetCapabilityTags] and the surface
// builder. ToolEntries and ExcludedTools carry per-tag, per-tool
// source attribution for the rich view consumed by inspect_capability,
// the CLI, and the /api/capabilities endpoints.
type resolvedCapabilityTags struct {
	Configs       map[string]config.CapabilityTagConfig
	ToolEntries   map[string][]toolcatalog.CapabilityToolEntry
	ExcludedTools map[string][]toolcatalog.CapabilityToolEntry
}

// resolveCapabilityTags computes the membership and attribution of
// each capability tag from three sources, in deterministic order:
//
//  1. Native catalog declarations: every registered native tool whose
//     [tools.Tool.Tags] mentions the tag contributes membership with
//     CapabilityToolSource{Kind: "native"}.
//
//  2. MCP server bindings: bridged tools inherit their server's tag
//     list (or per-tool override). They contribute membership with
//     CapabilityToolSource{Kind: "mcp", Origin: <server name>}.
//
//  3. Operator overlay: per-tag Include adds tools that did not
//     self-declare; Exclude removes tools the operator forbids at this
//     site. Overlay-added entries carry CapabilityToolSource{Kind:
//     "overlay", Origin: "capability_tags.<tag>.include"}.
//
// Final active membership for tag T is:
//
//	member(T) = native(T) ∪ mcp(T) ∪ Include(T) − Exclude(T)
//
// Excluded tools are surfaced in resolvedCapabilityTags.ExcludedTools
// with State{Status: "excluded"} so views that opt in (?include=excluded
// or thane caps show --excluded) can render them.
func resolveCapabilityTags(reg *tools.Registry, overrides map[string]config.CapabilityTagConfig) resolvedCapabilityTags {
	out := resolvedCapabilityTags{
		Configs:       make(map[string]config.CapabilityTagConfig),
		ToolEntries:   make(map[string][]toolcatalog.CapabilityToolEntry),
		ExcludedTools: make(map[string][]toolcatalog.CapabilityToolEntry),
	}
	builtinTags := toolcatalog.BuiltinTagSpecs()

	// Seed coarse menu/protected tags so they exist as catalog entries
	// even when no tools self-declare them. These are the entry-point
	// tags for delegation menus.
	for tag, spec := range builtinTags {
		if !shouldSeedBuiltinTag(tag, spec) {
			continue
		}
		out.Configs[tag] = config.CapabilityTagConfig{
			Description:  firstNonEmpty(strings.TrimSpace(spec.Description), generatedTagDescription(tag)),
			AlwaysActive: spec.AlwaysActive,
			Protected:    spec.Protected,
		}
	}

	// Build per-tag source attribution from the registered tool set.
	perTagSources := buildToolSourceAttribution(reg)

	// Determine the union of tags we need to emit: builtin-seeded,
	// registry-declared, and overlay-named.
	allTags := make(map[string]bool, len(out.Configs)+len(perTagSources)+len(overrides))
	for tag := range out.Configs {
		allTags[tag] = true
	}
	for tag := range perTagSources {
		allTags[tag] = true
	}
	for tag := range overrides {
		allTags[tag] = true
	}

	for tag := range allTags {
		cfg := out.Configs[tag]
		spec := builtinTags[tag]
		cfg.Description = firstNonEmpty(strings.TrimSpace(spec.Description), cfg.Description, generatedTagDescription(tag))
		if spec.AlwaysActive {
			cfg.AlwaysActive = true
		}
		if spec.Protected {
			cfg.Protected = true
		}

		// Start from native + mcp attribution for this tag.
		sources := make(map[string]toolcatalog.CapabilityToolSource)
		for name, src := range perTagSources[tag] {
			sources[name] = src
		}

		excludeSet := make(map[string]bool)
		if override, ok := overrides[tag]; ok {
			if desc := strings.TrimSpace(override.Description); desc != "" {
				cfg.Description = desc
			}
			if override.AlwaysActive {
				cfg.AlwaysActive = true
			}
			if override.Protected {
				cfg.Protected = true
			}
			for _, name := range override.Include {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				if _, exists := sources[name]; exists {
					// Already attributed by native or mcp; the operator
					// re-listing is harmless and we keep the natural
					// attribution.
					continue
				}
				sources[name] = toolcatalog.CapabilityToolSource{
					Kind:   toolcatalog.ToolSourceOverlay,
					Origin: overlayIncludeOrigin(tag),
				}
			}
			for _, name := range override.Exclude {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				excludeSet[name] = true
			}
		}

		activeEntries, excludedEntries, activeNames := splitActiveExcluded(tag, sources, excludeSet)

		cfg.Tools = activeNames
		if strings.TrimSpace(cfg.Description) == "" {
			cfg.Description = generatedTagDescription(tag)
		}
		out.Configs[tag] = cfg
		if len(activeEntries) > 0 {
			out.ToolEntries[tag] = activeEntries
		}
		if len(excludedEntries) > 0 {
			out.ExcludedTools[tag] = excludedEntries
		}
	}

	return out
}

// buildToolSourceAttribution walks the registry and groups every
// tool's natural tag declarations into a tag → name → source map.
// Native tools (Source == "" or "native") are attributed with no
// origin; MCP-bridged tools carry their server name.
func buildToolSourceAttribution(reg *tools.Registry) map[string]map[string]toolcatalog.CapabilityToolSource {
	perTag := make(map[string]map[string]toolcatalog.CapabilityToolSource)
	for _, name := range reg.AllToolNames() {
		t := reg.Get(name)
		if t == nil {
			continue
		}
		src := toolcatalog.CapabilityToolSource{Kind: toolcatalog.ToolSourceNative}
		if t.Source == "mcp" {
			src.Kind = toolcatalog.ToolSourceMCP
			src.Origin = t.Origin
		}
		for _, tag := range t.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			if perTag[tag] == nil {
				perTag[tag] = make(map[string]toolcatalog.CapabilityToolSource)
			}
			perTag[tag][name] = src
		}
	}
	return perTag
}

// splitActiveExcluded converts the resolved per-tool source map into
// sorted active and excluded entry slices plus a sorted active-name
// list (the latter feeds [config.CapabilityTagConfig.Tools]).
func splitActiveExcluded(tag string, sources map[string]toolcatalog.CapabilityToolSource, excludeSet map[string]bool) (active, excluded []toolcatalog.CapabilityToolEntry, activeNames []string) {
	for name, src := range sources {
		if excludeSet[name] {
			excluded = append(excluded, toolcatalog.CapabilityToolEntry{
				Name:   name,
				Source: src,
				State: &toolcatalog.CapabilityToolState{
					Status: toolcatalog.ToolStateExcluded,
					Reason: overlayExcludeReason(tag),
				},
			})
			continue
		}
		active = append(active, toolcatalog.CapabilityToolEntry{
			Name:   name,
			Source: src,
		})
		activeNames = append(activeNames, name)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Name < active[j].Name })
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].Name < excluded[j].Name })
	sort.Strings(activeNames)
	return
}

// auditExcludedToolReferences logs warnings when an operator-excluded
// tool is referenced by another subsystem that assumed it was
// available. Today we audit the orchestrator allowlist; talent
// content audit is intentionally not implemented since talents
// reference tools in free-form prose rather than a structured field.
//
// The check fires only for tools that are unreachable everywhere — a
// tool excluded from one tag but still active in another is fine.
func auditExcludedToolReferences(logger *slog.Logger, excludedByTag map[string][]toolcatalog.CapabilityToolEntry, orchestratorTools []string, _ []talents.Talent) {
	if logger == nil || len(excludedByTag) == 0 {
		return
	}
	excludedByTool := make(map[string][]string)
	for tag, entries := range excludedByTag {
		for _, e := range entries {
			excludedByTool[e.Name] = append(excludedByTool[e.Name], tag)
		}
	}
	for _, name := range orchestratorTools {
		tags, ok := excludedByTool[name]
		if !ok {
			continue
		}
		logger.Warn("orchestrator allowlist references excluded tool",
			"tool", name,
			"excluded_from_tags", tags,
		)
	}
}

func overlayIncludeOrigin(tag string) string {
	return fmt.Sprintf("capability_tags.%s.include", tag)
}

func overlayExcludeReason(tag string) string {
	return fmt.Sprintf("capability_tags.%s.exclude", tag)
}

func shouldSeedBuiltinTag(tag string, spec toolcatalog.BuiltinTagSpec) bool {
	if spec.Protected || spec.Menu {
		return true
	}
	switch tag {
	case "owu":
		return true
	default:
		return false
	}
}

func generatedTagDescription(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "Tool group."
	}
	readable := strings.ReplaceAll(tag, "_", " ")
	readable = strings.ReplaceAll(readable, "-", " ")
	readable = strings.TrimSpace(readable)
	if readable == "" {
		return "Tool group."
	}
	readable = strings.ToUpper(readable[:1]) + readable[1:]
	return fmt.Sprintf("%s tools.", readable)
}

func firstNonEmpty(parts ...string) string {
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			return part
		}
	}
	return ""
}

// buildCapabilitySurface assembles the full capability surface used by
// the prompt renderer, capability manifest, and dashboard. Per-tool
// attribution from the resolver is threaded onto each surface entry
// so consumers (inspect_capability, /api/capabilities) can render
// where every tool came from.
func buildCapabilitySurface(
	resolved resolvedCapabilityTags,
	kbCounts map[string]int,
	menuHints map[string]agent.KBMenuHint,
	liveTags map[string]bool,
	adHocTags map[string]bool,
) []toolcatalog.CapabilitySurface {
	tagIndex := make(map[string][]string, len(resolved.Configs))
	descriptions := make(map[string]string, len(resolved.Configs))
	alwaysActive := make(map[string]bool, len(resolved.Configs))
	protected := make(map[string]bool, len(resolved.Configs))
	for tag, cfg := range resolved.Configs {
		tagIndex[tag] = append([]string(nil), cfg.Tools...)
		descriptions[tag] = cfg.Description
		alwaysActive[tag] = cfg.AlwaysActive
		protected[tag] = cfg.Protected
	}

	surface := toolcatalog.BuildCapabilitySurface(tagIndex, descriptions, alwaysActive, protected)
	indexByTag := make(map[string]int, len(surface))
	for i := range surface {
		indexByTag[surface[i].Tag] = i
		surface[i].KBArticles = kbCounts[surface[i].Tag]
		if hint, ok := menuHints[surface[i].Tag]; ok {
			surface[i].Teaser = hint.Teaser
			surface[i].NextTags = append([]string(nil), hint.NextTags...)
		}
		surface[i].LiveContext = liveTags[surface[i].Tag]
		surface[i].ToolEntries = append([]toolcatalog.CapabilityToolEntry(nil), resolved.ToolEntries[surface[i].Tag]...)
		surface[i].ExcludedTools = append([]toolcatalog.CapabilityToolEntry(nil), resolved.ExcludedTools[surface[i].Tag]...)
	}

	for tag := range adHocTags {
		if _, ok := indexByTag[tag]; ok {
			continue
		}
		surface = append(surface, toolcatalog.CapabilitySurface{
			Tag:         tag,
			Description: descriptions[tag],
			Teaser:      menuHints[tag].Teaser,
			NextTags:    append([]string(nil), menuHints[tag].NextTags...),
			Protected:   protected[tag],
			KBArticles:  kbCounts[tag],
			LiveContext: liveTags[tag],
			AdHoc:       true,
		})
	}

	return toolcatalog.SortCapabilitySurface(surface)
}

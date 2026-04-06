package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func resolveCapabilityTags(reg *tools.Registry, overrides map[string]config.CapabilityTagConfig) map[string]config.CapabilityTagConfig {
	resolved := make(map[string]config.CapabilityTagConfig)
	builtinTags := toolcatalog.BuiltinTagSpecs()
	for tag, spec := range builtinTags {
		if !shouldSeedBuiltinTag(tag, spec) {
			continue
		}
		resolved[tag] = config.CapabilityTagConfig{
			Description:  firstNonEmpty(strings.TrimSpace(spec.Description), generatedTagDescription(tag)),
			AlwaysActive: spec.AlwaysActive,
			Protected:    spec.Protected,
		}
	}
	for tag, toolNames := range reg.MetadataTagIndex() {
		spec := builtinTags[tag]
		sortedToolNames := append([]string(nil), toolNames...)
		sort.Strings(sortedToolNames)
		merged := resolved[tag]
		merged.Description = firstNonEmpty(strings.TrimSpace(spec.Description), merged.Description, generatedTagDescription(tag))
		merged.Tools = sortedToolNames
		if spec.AlwaysActive {
			merged.AlwaysActive = true
		}
		if spec.Protected {
			merged.Protected = true
		}
		resolved[tag] = merged
	}
	for tag, override := range overrides {
		merged := resolved[tag]
		if desc := strings.TrimSpace(override.Description); desc != "" {
			merged.Description = desc
		}
		if len(override.Tools) > 0 {
			merged.Tools = append([]string(nil), override.Tools...)
			sort.Strings(merged.Tools)
		}
		if override.AlwaysActive {
			merged.AlwaysActive = true
		}
		if override.Protected {
			merged.Protected = true
		}
		if strings.TrimSpace(merged.Description) == "" {
			merged.Description = generatedTagDescription(tag)
		}
		resolved[tag] = merged
	}
	return resolved
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

func buildCapabilitySurface(
	resolved map[string]config.CapabilityTagConfig,
	kbCounts map[string]int,
	menuHints map[string]agent.KBMenuHint,
	liveTags map[string]bool,
	adHocTags map[string]bool,
) []toolcatalog.CapabilitySurface {
	tagIndex := make(map[string][]string, len(resolved))
	descriptions := make(map[string]string, len(resolved))
	alwaysActive := make(map[string]bool, len(resolved))
	protected := make(map[string]bool, len(resolved))
	for tag, cfg := range resolved {
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

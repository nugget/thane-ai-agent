package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func resolveCapabilityTags(reg *tools.Registry, overrides map[string]config.CapabilityTagConfig) map[string]config.CapabilityTagConfig {
	resolved := make(map[string]config.CapabilityTagConfig)
	builtinTags := toolcatalog.BuiltinTagSpecs()
	for tag, toolNames := range reg.MetadataTagIndex() {
		spec := builtinTags[tag]
		resolved[tag] = config.CapabilityTagConfig{
			Description:  firstNonEmpty(strings.TrimSpace(spec.Description), generatedTagDescription(tag)),
			Tools:        append([]string(nil), toolNames...),
			AlwaysActive: spec.AlwaysActive,
		}
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
		if strings.TrimSpace(merged.Description) == "" {
			merged.Description = generatedTagDescription(tag)
		}
		resolved[tag] = merged
	}
	return resolved
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

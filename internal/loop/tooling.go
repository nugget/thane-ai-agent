package loop

import (
	"sort"

	"github.com/nugget/thane-ai-agent/internal/toolcatalog"
)

// ToolingState captures the resolved tool/capability surface for a
// loop or a single historical iteration. It gives the dashboard one
// authoritative payload to render instead of re-deriving state from a
// mix of config, live context, and snapshot arrays.
type ToolingState struct {
	ConfiguredTags     []string                            `json:"configured_tags,omitempty"`
	LoadedTags         []string                            `json:"loaded_tags,omitempty"`
	LoadedCapabilities []toolcatalog.LoadedCapabilityEntry `json:"loaded_capabilities,omitempty"`
	EffectiveTools     []string                            `json:"effective_tools,omitempty"`
	ExcludedTools      []string                            `json:"excluded_tools,omitempty"`
	ToolsUsed          map[string]int                      `json:"tools_used,omitempty"`
}

func BuildToolingState(configuredTags, loadedTags, effectiveTools, excludedTools []string, loadedCapabilities []toolcatalog.LoadedCapabilityEntry, toolsUsed map[string]int) ToolingState {
	state := ToolingState{
		ConfiguredTags:     append([]string(nil), configuredTags...),
		LoadedTags:         append([]string(nil), loadedTags...),
		EffectiveTools:     append([]string(nil), effectiveTools...),
		ExcludedTools:      append([]string(nil), excludedTools...),
		LoadedCapabilities: append([]toolcatalog.LoadedCapabilityEntry(nil), loadedCapabilities...),
	}
	if len(toolsUsed) > 0 {
		state.ToolsUsed = make(map[string]int, len(toolsUsed))
		for name, count := range toolsUsed {
			state.ToolsUsed[name] = count
		}
	}
	sort.Strings(state.ConfiguredTags)
	sort.Strings(state.LoadedTags)
	sort.Strings(state.EffectiveTools)
	sort.Strings(state.ExcludedTools)
	sort.Slice(state.LoadedCapabilities, func(i, j int) bool {
		return state.LoadedCapabilities[i].Tag < state.LoadedCapabilities[j].Tag
	})
	return state
}

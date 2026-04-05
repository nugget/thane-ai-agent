package llm

import "strings"

// ToolCallStyle describes the primary tool-calling contract we expect a
// model family to follow.
type ToolCallStyle string

const (
	ToolCallStyleNative      ToolCallStyle = "native"
	ToolCallStyleRawTextJSON ToolCallStyle = "raw_text_json"
)

// ContextRenderStyle describes how runtime-generated context should be
// shaped for a model family.
type ContextRenderStyle string

const (
	ContextRenderStyleJSONFirst ContextRenderStyle = "json_first"
)

// ModelProfileInput is the normalized metadata used to choose a
// model-family interaction profile.
type ModelProfileInput struct {
	Provider          string
	Model             string
	Family            string
	Families          []string
	TrainedForToolUse bool
}

// ModelInteractionProfile captures model-family defaults for
// model-facing context and tool-call compatibility.
type ModelInteractionProfile struct {
	Name            string
	ContextStyle    ContextRenderStyle
	ToolCallStyle   ToolCallStyle
	TextToolProfile ToolCallTextProfile
}

// DefaultModelInteractionProfile returns the generic Thane default.
func DefaultModelInteractionProfile() ModelInteractionProfile {
	return ModelInteractionProfile{
		Name:            "generic_native",
		ContextStyle:    ContextRenderStyleJSONFirst,
		ToolCallStyle:   ToolCallStyleNative,
		TextToolProfile: DefaultToolCallTextProfile(),
	}
}

// ProfileForModel selects the best current model-interaction profile
// from provider/model-family hints. The current default is conservative:
// stay JSON-first for context, but switch local open-model families to a
// raw-text tool-call contract when they commonly emit text instead of
// native tool-call structures.
func ProfileForModel(input ModelProfileInput) ModelInteractionProfile {
	profile := DefaultModelInteractionProfile()

	parts := []string{
		strings.ToLower(strings.TrimSpace(input.Provider)),
		strings.ToLower(strings.TrimSpace(input.Model)),
		strings.ToLower(strings.TrimSpace(input.Family)),
	}
	for _, family := range input.Families {
		parts = append(parts, strings.ToLower(strings.TrimSpace(family)))
	}
	haystack := strings.Join(parts, " ")

	switch {
	case strings.Contains(haystack, "claude") || strings.Contains(haystack, "anthropic"):
		profile.Name = "anthropic_native"
	case strings.Contains(haystack, "gpt-oss"):
		// OpenAI's gpt-oss guidance distinguishes between direct Harmony
		// deployments and provider-backed serving layers like Ollama or
		// vLLM. In Thane we talk to provider APIs, so keep the prompt-side
		// contract native and rely on provider normalization plus our
		// runtime fallback if raw text still leaks through.
		if input.TrainedForToolUse || strings.Contains(haystack, "ollama") || strings.Contains(haystack, "lmstudio") {
			profile.Name = "gpt_oss_provider_native"
			break
		}
		profile.Name = "gpt_oss_raw_text_tools"
		profile.ToolCallStyle = ToolCallStyleRawTextJSON
	case strings.Contains(haystack, "gemma"),
		strings.Contains(haystack, "qwen"),
		strings.Contains(haystack, "llama"),
		strings.Contains(haystack, "mistral"):
		profile.Name = "local_raw_text_tools"
		profile.ToolCallStyle = ToolCallStyleRawTextJSON
	case (strings.Contains(haystack, "lmstudio") || strings.Contains(haystack, "ollama")) && !input.TrainedForToolUse:
		profile.Name = "local_raw_text_tools"
		profile.ToolCallStyle = ToolCallStyleRawTextJSON
	}

	return profile
}

// ToolCallingContract returns a short model-facing instruction for
// runtimes that need to recover tool calls from raw assistant text.
func (p ModelInteractionProfile) ToolCallingContract() string {
	if p.ToolCallStyle != ToolCallStyleRawTextJSON {
		return ""
	}
	return strings.Join([]string{
		"When you need a tool, emit only one compact JSON object with exactly these fields:",
		`{"name":"exact_tool_name","arguments":{...}}`,
		"Capability and tag requests are tool actions: if the user asks to activate, deactivate, load, unload, or inspect loaded capabilities/tags, use the exact capability tool instead of answering conversationally.",
		"Do not wrap the JSON in markdown fences.",
		"Do not add prose before or after the JSON.",
		"Do not invent tool names.",
		"If no tool is needed, answer normally.",
	}, "\n")
}

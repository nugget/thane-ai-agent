package modeleval

import (
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// ApplyToolCallingProfile replaces only the model-family tool-calling
// contract inside a captured system prompt. It returns changed=false when
// section boundaries are unavailable or invalid, leaving the exact captured
// prompt untouched.
func ApplyToolCallingProfile(messages []llm.Message, sections []PromptSection, profile llm.ModelInteractionProfile) ([]llm.Message, bool) {
	out := cloneMessages(messages)
	if len(out) == 0 || out[0].Role != "system" || len(sections) == 0 {
		return out, false
	}
	prompt := out[0].Content
	if !validSections(prompt, sections) {
		return out, false
	}

	contract := strings.TrimSpace(profile.ToolCallingContract())
	contractContent := ""
	if contract != "" {
		contractContent = "## Tool Calling Contract\n\n" + contract + "\n"
	}

	var contractSection *PromptSection
	var runtimeSection *PromptSection
	for _, section := range sections {
		if section.Name == "TOOL CALLING CONTRACT" {
			copy := section
			contractSection = &copy
		}
		if section.Name == "RUNTIME CONTRACT" {
			copy := section
			runtimeSection = &copy
		}
	}
	if contractSection == nil && contractContent == "" {
		return out, false
	}
	if contractSection != nil && prompt[contractSection.Start:contractSection.End] == contractContent {
		return out, false
	}
	if contractSection == nil && runtimeSection == nil {
		return out, false
	}
	if contractSection != nil {
		before := prompt[:contractSection.Start]
		after := prompt[contractSection.End:]
		if contractContent == "" && strings.HasPrefix(after, "\n\n") {
			after = after[2:]
		}
		out[0].Content = before + contractContent + after
	} else {
		out[0].Content = prompt[:runtimeSection.End] + "\n\n" + contractContent + prompt[runtimeSection.End:]
	}
	out[0].Sections = nil
	return out, true
}

func validSections(prompt string, sections []PromptSection) bool {
	previousEnd := 0
	for i, section := range sections {
		if section.Start < 0 || section.End <= section.Start || section.End > len(prompt) {
			return false
		}
		if i > 0 && section.Start < previousEnd {
			return false
		}
		previousEnd = section.End
	}
	return true
}

func cloneMessages(src []llm.Message) []llm.Message {
	dst := make([]llm.Message, len(src))
	for i := range src {
		dst[i] = src[i]
		dst[i].Images = append([]llm.ImageContent(nil), src[i].Images...)
		dst[i].Sections = append([]llm.PromptSection(nil), src[i].Sections...)
		if src[i].ToolCalls != nil {
			dst[i].ToolCalls = make([]llm.ToolCall, len(src[i].ToolCalls))
			for callIndex := range src[i].ToolCalls {
				dst[i].ToolCalls[callIndex] = src[i].ToolCalls[callIndex]
				dst[i].ToolCalls[callIndex].Function.Arguments = cloneJSONMap(src[i].ToolCalls[callIndex].Function.Arguments)
			}
		}
	}
	return dst
}

package modeleval

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestApplyToolCallingProfile(t *testing.T) {
	t.Parallel()

	runtime := "runtime"
	oldContract := "## Tool Calling Contract\n\nold\n"
	talent := "talent"
	prompt := strings.Join([]string{runtime, oldContract, talent}, "\n\n")
	sections := sectionsFor(prompt, []struct{ name, content string }{
		{"RUNTIME CONTRACT", runtime},
		{"TOOL CALLING CONTRACT", oldContract},
		{"TALENTS ALWAYS ON", talent},
	})
	messages := []llm.Message{{Role: "system", Content: prompt}}

	native, changed := ApplyToolCallingProfile(messages, sections, llm.DefaultModelInteractionProfile())
	if !changed || strings.Contains(native[0].Content, "old") || native[0].Content != runtime+"\n\n"+talent {
		t.Fatalf("native prompt = %q, changed=%v", native[0].Content, changed)
	}

	raw := llm.DefaultModelInteractionProfile()
	raw.ToolCallStyle = llm.ToolCallStyleRawTextJSON
	rewritten, changed := ApplyToolCallingProfile(messages, sections, raw)
	if !changed || !strings.Contains(rewritten[0].Content, "emit only one compact JSON object") || strings.Contains(rewritten[0].Content, "\nold\n") {
		t.Fatalf("raw prompt = %q, changed=%v", rewritten[0].Content, changed)
	}
}

func TestApplyToolCallingProfileInsertsAfterRuntime(t *testing.T) {
	t.Parallel()

	prompt := "runtime\n\ntalent"
	sections := sectionsFor(prompt, []struct{ name, content string }{
		{"RUNTIME CONTRACT", "runtime"},
		{"TALENTS ALWAYS ON", "talent"},
	})
	profile := llm.DefaultModelInteractionProfile()
	profile.ToolCallStyle = llm.ToolCallStyleRawTextJSON
	got, changed := ApplyToolCallingProfile([]llm.Message{{Role: "system", Content: prompt}}, sections, profile)
	if !changed || !strings.HasPrefix(got[0].Content, "runtime\n\n## Tool Calling Contract") || !strings.HasSuffix(got[0].Content, "\n\ntalent") {
		t.Fatalf("prompt = %q, changed=%v", got[0].Content, changed)
	}
}

func TestApplyToolCallingProfileNoOp(t *testing.T) {
	t.Parallel()

	messages := []llm.Message{{Role: "system", Content: "runtime"}}
	got, changed := ApplyToolCallingProfile(messages, []PromptSection{{Name: "RUNTIME CONTRACT", Start: 0, End: 7}}, llm.DefaultModelInteractionProfile())
	if changed || got[0].Content != "runtime" {
		t.Fatalf("prompt = %q, changed=%v", got[0].Content, changed)
	}
}

func sectionsFor(prompt string, inputs []struct{ name, content string }) []PromptSection {
	cursor := 0
	out := make([]PromptSection, 0, len(inputs))
	for _, input := range inputs {
		rel := strings.Index(prompt[cursor:], input.content)
		start := cursor + rel
		end := start + len(input.content)
		out = append(out, PromptSection{Name: input.name, Start: start, End: end})
		cursor = end
	}
	return out
}

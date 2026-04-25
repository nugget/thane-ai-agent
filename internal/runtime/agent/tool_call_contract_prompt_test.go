package agent

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestBuildSystemPromptWithProfile_AddsToolCallingContractForRawTextModels(t *testing.T) {
	l := newTagTestLoop()

	prompt := l.buildSystemPromptWithProfile(
		testCtxForLoop(l),
		"activate forge",
		nil,
		llm.ProfileForModel(llm.ModelProfileInput{
			Provider: "lmstudio",
			Model:    "google/gemma-3-4b",
			Family:   "gemma3",
		}),
	)

	if !strings.Contains(prompt, "## Tool Calling Contract") {
		t.Fatalf("prompt missing Tool Calling Contract section: %s", prompt)
	}
	if !strings.Contains(prompt, `{"name":"exact_tool_name","arguments":{...}}`) {
		t.Fatalf("prompt missing raw JSON tool-call contract: %s", prompt)
	}
	if !strings.Contains(prompt, "Do not wrap the JSON in markdown fences.") {
		t.Fatalf("prompt missing anti-fence guidance: %s", prompt)
	}
	if !strings.Contains(prompt, "Capability and tag requests are tool actions") {
		t.Fatalf("prompt missing capability tool-action guidance: %s", prompt)
	}
}

func TestBuildSystemPromptWithProfile_OmitsToolCallingContractForNativeModels(t *testing.T) {
	l := newTagTestLoop()

	prompt := l.buildSystemPromptWithProfile(
		testCtxForLoop(l),
		"hello",
		nil,
		llm.DefaultModelInteractionProfile(),
	)

	if strings.Contains(prompt, "## Tool Calling Contract") {
		t.Fatalf("prompt unexpectedly included Tool Calling Contract: %s", prompt)
	}
}

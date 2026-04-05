package llm

import "testing"

func TestProfileForModel_ClassifiesGemmaAsRawTextTools(t *testing.T) {
	profile := ProfileForModel(ModelProfileInput{
		Provider: "lmstudio",
		Model:    "google/gemma-3-4b",
		Family:   "gemma3",
	})

	if profile.ToolCallStyle != ToolCallStyleRawTextJSON {
		t.Fatalf("ToolCallStyle = %q, want %q", profile.ToolCallStyle, ToolCallStyleRawTextJSON)
	}
	if profile.ToolCallingContract() == "" {
		t.Fatal("ToolCallingContract() should be non-empty for gemma raw-text profile")
	}
}

func TestProfileForModel_ClassifiesClaudeAsNativeTools(t *testing.T) {
	profile := ProfileForModel(ModelProfileInput{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
	})

	if profile.ToolCallStyle != ToolCallStyleNative {
		t.Fatalf("ToolCallStyle = %q, want %q", profile.ToolCallStyle, ToolCallStyleNative)
	}
	if profile.ToolCallingContract() != "" {
		t.Fatal("ToolCallingContract() should be empty for native-tool profile")
	}
}

package llm

import "testing"

func TestProfileForModel_ClassifiesToolContractsByModelFamily(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        ModelProfileInput
		wantStyle    ToolCallStyle
		wantContract bool
	}{
		{
			name: "gemma via lmstudio uses raw text contract",
			input: ModelProfileInput{
				Provider: "lmstudio",
				Model:    "google/gemma-3-4b",
				Family:   "gemma3",
			},
			wantStyle:    ToolCallStyleRawTextJSON,
			wantContract: true,
		},
		{
			name: "provider backed gpt oss 20b stays native",
			input: ModelProfileInput{
				Provider:          "ollama",
				Model:             "gpt-oss:20b",
				Family:            "gpt-oss",
				TrainedForToolUse: true,
			},
			wantStyle:    ToolCallStyleNative,
			wantContract: false,
		},
		{
			name: "provider backed gpt oss 120b stays native",
			input: ModelProfileInput{
				Provider:          "lmstudio",
				Model:             "openai/gpt-oss-120b",
				Family:            "gpt-oss",
				TrainedForToolUse: true,
			},
			wantStyle:    ToolCallStyleNative,
			wantContract: false,
		},
		{
			name: "claude stays native",
			input: ModelProfileInput{
				Provider: "anthropic",
				Model:    "claude-sonnet-4-20250514",
			},
			wantStyle:    ToolCallStyleNative,
			wantContract: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			profile := ProfileForModel(tt.input)
			if profile.ToolCallStyle != tt.wantStyle {
				t.Fatalf("ToolCallStyle = %q, want %q", profile.ToolCallStyle, tt.wantStyle)
			}
			gotContract := profile.ToolCallingContract() != ""
			if gotContract != tt.wantContract {
				t.Fatalf("ToolCallingContract() present = %v, want %v", gotContract, tt.wantContract)
			}
		})
	}
}

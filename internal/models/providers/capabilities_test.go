package providers

import "testing"

func TestSupportsImagesForModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		model    string
		family   string
		families []string
		caps     Capabilities
		want     bool
	}{
		{
			name:     "anthropic stays vision-capable",
			provider: "anthropic",
			model:    "claude-sonnet-4-5",
			caps:     Capabilities{SupportsImages: true},
			want:     true,
		},
		{
			name:     "ollama gpt oss is text only",
			provider: "ollama",
			model:    "gpt-oss:20b",
			family:   "gpt-oss",
			caps:     Capabilities{SupportsImages: true},
			want:     false,
		},
		{
			name:     "ollama qwen vl is vision capable",
			provider: "ollama",
			model:    "qwen2.5-vl:7b",
			family:   "qwen2.5-vl",
			caps:     Capabilities{SupportsImages: true},
			want:     true,
		},
		{
			name:     "lmstudio gemma 3 is vision capable",
			provider: "lmstudio",
			model:    "google/gemma-3-4b",
			caps:     Capabilities{SupportsImages: true},
			want:     true,
		},
		{
			name:     "provider without image transport is false",
			provider: "lmstudio",
			model:    "google/gemma-3-4b",
			caps:     Capabilities{},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SupportsImagesForModel(tt.provider, tt.model, tt.family, tt.families, tt.caps)
			if got != tt.want {
				t.Fatalf("SupportsImagesForModel(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

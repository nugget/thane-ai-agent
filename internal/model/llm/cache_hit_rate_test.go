package llm

import (
	"math"
	"testing"
)

func TestCacheHitRate(t *testing.T) {
	tests := []struct {
		name      string
		reads     int
		creations int
		want      float64
	}{
		{"cold start: only creations", 0, 1000, 0.0},
		{"fully warm: only reads", 1000, 0, 1.0},
		{"half hot", 500, 500, 0.5},
		{"empty window", 0, 0, 0.0},
		{"negatives clamp to zero", -5, -10, 0.0},
		{"typical warm session", 10000, 200, 10000.0 / 10200.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CacheHitRate(tt.reads, tt.creations)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("CacheHitRate(%d, %d) = %v, want %v", tt.reads, tt.creations, got, tt.want)
			}
		})
	}
}

func TestChatResponse_CacheHitRate(t *testing.T) {
	resp := &ChatResponse{
		CacheReadInputTokens:     800,
		CacheCreationInputTokens: 200,
	}
	want := 0.8
	if got := resp.CacheHitRate(); math.Abs(got-want) > 1e-9 {
		t.Errorf("CacheHitRate() = %v, want %v", got, want)
	}

	empty := &ChatResponse{}
	if got := empty.CacheHitRate(); got != 0 {
		t.Errorf("empty response CacheHitRate() = %v, want 0 (no division by zero)", got)
	}
}

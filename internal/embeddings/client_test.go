package embeddings

import (
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []float32
		expected float32
	}{
		{
			name:     "identical",
			a:        []float32{1, 0, 0},
			b:        []float32{1, 0, 0},
			expected: 1.0,
		},
		{
			name:     "orthogonal",
			a:        []float32{1, 0},
			b:        []float32{0, 1},
			expected: 0.0,
		},
		{
			name:     "opposite",
			a:        []float32{1, 1},
			b:        []float32{-1, -1},
			expected: -1.0,
		},
		{
			name:     "mismatched length",
			a:        []float32{1},
			b:        []float32{1, 2},
			expected: 0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CosineSimilarity(tc.a, tc.b)
			if math.Abs(float64(got-tc.expected)) > 0.0001 {
				t.Errorf("got %f, want %f", got, tc.expected)
			}
		})
	}
}

func TestTopK(t *testing.T) {
	query := []float32{1, 0, 0}
	vectors := [][]float32{
		{0, 1, 0},     // orthogonal, sim = 0
		{1, 0, 0},     // identical, sim = 1
		{-1, 0, 0},    // opposite, sim = -1
		{0.7, 0.7, 0}, // similar, sim ≈ 0.707
	}

	top2 := TopK(query, vectors, 2)
	if len(top2) != 2 {
		t.Fatalf("expected 2 results, got %d", len(top2))
	}
	if top2[0] != 1 {
		t.Errorf("expected index 1 (identical) first, got %d", top2[0])
	}
	if top2[1] != 3 {
		t.Errorf("expected index 3 (similar) second, got %d", top2[1])
	}
}

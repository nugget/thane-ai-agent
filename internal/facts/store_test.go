package facts

import (
	"math"
	"testing"
)

func TestEmbeddingEncodeDecode(t *testing.T) {
	original := []float32{1.5, -2.3, 0.0, 3.14159, -0.001}

	encoded := encodeEmbedding(original)
	decoded := decodeEmbedding(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(original))
	}

	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("value %d: got %f, want %f", i, decoded[i], original[i])
		}
	}
}

func TestEmbeddingEncodeEmpty(t *testing.T) {
	if encoded := encodeEmbedding(nil); encoded != nil {
		t.Errorf("expected nil for nil input, got %v", encoded)
	}
	if encoded := encodeEmbedding([]float32{}); encoded != nil {
		t.Errorf("expected nil for empty input, got %v", encoded)
	}
}

func TestEmbeddingDecodeEmpty(t *testing.T) {
	if decoded := decodeEmbedding(nil); decoded != nil {
		t.Errorf("expected nil for nil input, got %v", decoded)
	}
	if decoded := decodeEmbedding([]byte{}); decoded != nil {
		t.Errorf("expected nil for empty input, got %v", decoded)
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []float32
		expected float32
	}{
		{
			name:     "identical vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{1, 0, 0},
			expected: 1.0,
		},
		{
			name:     "orthogonal vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{0, 1, 0},
			expected: 0.0,
		},
		{
			name:     "opposite vectors",
			a:        []float32{1, 0, 0},
			b:        []float32{-1, 0, 0},
			expected: -1.0,
		},
		{
			name:     "different lengths",
			a:        []float32{1, 2},
			b:        []float32{1, 2, 3},
			expected: 0.0,
		},
		{
			name:     "zero vector",
			a:        []float32{0, 0, 0},
			b:        []float32{1, 2, 3},
			expected: 0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cosineSimilarity(tc.a, tc.b)
			if math.Abs(float64(got-tc.expected)) > 0.0001 {
				t.Errorf("got %f, want %f", got, tc.expected)
			}
		})
	}
}

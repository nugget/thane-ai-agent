package embeddings

import (
	"testing"
)

func TestEncodeDecodeEmbedding(t *testing.T) {
	original := []float32{1.5, -2.3, 0.0, 3.14159, -0.001}

	encoded := EncodeEmbedding(original)
	decoded := DecodeEmbedding(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("value %d: got %f, want %f", i, decoded[i], original[i])
		}
	}
}

func TestEncodeEmbeddingEmpty(t *testing.T) {
	if encoded := EncodeEmbedding(nil); encoded != nil {
		t.Errorf("expected nil for nil input, got %v", encoded)
	}
	if encoded := EncodeEmbedding([]float32{}); encoded != nil {
		t.Errorf("expected nil for empty input, got %v", encoded)
	}
}

func TestDecodeEmbeddingEmpty(t *testing.T) {
	if decoded := DecodeEmbedding(nil); decoded != nil {
		t.Errorf("expected nil for nil input, got %v", decoded)
	}
	if decoded := DecodeEmbedding([]byte{}); decoded != nil {
		t.Errorf("expected nil for empty input, got %v", decoded)
	}
}

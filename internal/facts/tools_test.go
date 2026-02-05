package facts

import (
	"context"
	"os"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// mockEmbeddingClient implements EmbeddingClient for testing.
type mockEmbeddingClient struct {
	embedding []float32
	err       error
	callCount int
}

func (m *mockEmbeddingClient) Generate(ctx context.Context, text string) ([]float32, error) {
	m.callCount++
	return m.embedding, m.err
}

func TestRememberWithEmbedding(t *testing.T) {
	// Create temp database
	tmpFile, err := os.CreateTemp("", "facts-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tools := NewTools(store)

	// Set up mock embedding client
	mockEmb := &mockEmbeddingClient{
		embedding: []float32{0.1, 0.2, 0.3},
	}
	tools.SetEmbeddingClient(mockEmb)

	// Remember a fact
	result, err := tools.Remember(`{"category":"user","key":"test_key","value":"test_value"}`)
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}

	if result == "" {
		t.Error("expected non-empty result")
	}

	// Verify embedding was generated
	if mockEmb.callCount != 1 {
		t.Errorf("expected 1 embedding call, got %d", mockEmb.callCount)
	}

	// Verify embedding was stored
	facts, err := store.GetAllWithEmbeddings()
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Errorf("expected 1 fact with embedding, got %d", len(facts))
	}
	if len(facts[0].Embedding) != 3 {
		t.Errorf("expected embedding length 3, got %d", len(facts[0].Embedding))
	}
}

func TestRememberWithoutEmbedding(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "facts-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tools := NewTools(store)
	// No embedding client set

	// Remember should still work
	result, err := tools.Remember(`{"category":"user","key":"test_key","value":"test_value"}`)
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	// Verify fact stored but no embedding
	facts, err := store.GetFactsWithoutEmbeddings()
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Errorf("expected 1 fact without embedding, got %d", len(facts))
	}
}

func TestGenerateMissingEmbeddings(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "facts-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tools := NewTools(store)

	// Add facts without embeddings
	_, _ = store.Set(CategoryUser, "key1", "value1", "test", 1.0)
	_, _ = store.Set(CategoryUser, "key2", "value2", "test", 1.0)

	// Verify no embeddings initially
	without, _ := store.GetFactsWithoutEmbeddings()
	if len(without) != 2 {
		t.Fatalf("expected 2 facts without embeddings, got %d", len(without))
	}

	// Set up mock
	mockEmb := &mockEmbeddingClient{
		embedding: []float32{0.5, 0.5},
	}
	tools.SetEmbeddingClient(mockEmb)

	// Generate missing embeddings
	count, err := tools.GenerateMissingEmbeddings()
	if err != nil {
		t.Fatalf("GenerateMissingEmbeddings failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 embeddings generated, got %d", count)
	}

	// Verify all facts now have embeddings
	without, _ = store.GetFactsWithoutEmbeddings()
	if len(without) != 0 {
		t.Errorf("expected 0 facts without embeddings, got %d", len(without))
	}

	with, _ := store.GetAllWithEmbeddings()
	if len(with) != 2 {
		t.Errorf("expected 2 facts with embeddings, got %d", len(with))
	}
}

func TestGenerateMissingEmbeddingsNoClient(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "facts-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tools := NewTools(store)
	// No embedding client

	_, err = tools.GenerateMissingEmbeddings()
	if err == nil {
		t.Error("expected error when no embedding client configured")
	}
}

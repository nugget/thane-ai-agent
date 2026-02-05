package facts

import (
	"context"
	"os"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/embeddings"
)

// mockEmbedder returns predictable embeddings for testing.
type mockEmbedder struct {
	embedding []float32
	err       error
}

func (m *mockEmbedder) Generate(_ context.Context, _ string) ([]float32, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.embedding, nil
}

func TestContextProvider_GetContext_Empty(t *testing.T) {
	// Create temp database
	tmpDB, err := os.CreateTemp("", "context-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	store, err := NewStore(tmpDB.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Mock embedder - won't be called since no facts exist
	mock := &mockEmbedder{embedding: []float32{1.0, 0.0, 0.0}}

	// Need a real embeddings.Client, but we can't easily mock it
	// For now, test with nil embedder (should handle gracefully)
	provider := &ContextProvider{
		store:    store,
		embedder: nil,
		maxFacts: 5,
		minScore: 0.3,
	}

	// Empty message should return empty context
	ctx := context.Background()
	result, err := provider.GetContext(ctx, "")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}

	// Mark mock as used to avoid lint error
	_ = mock
}

func TestContextProvider_Config(t *testing.T) {
	tmpDB, err := os.CreateTemp("", "context-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	store, err := NewStore(tmpDB.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	provider := NewContextProvider(store, nil)

	// Check defaults
	if provider.maxFacts != 5 {
		t.Errorf("expected maxFacts=5, got %d", provider.maxFacts)
	}
	if provider.minScore != 0.3 {
		t.Errorf("expected minScore=0.3, got %f", provider.minScore)
	}

	// Test setters
	provider.SetMaxFacts(10)
	if provider.maxFacts != 10 {
		t.Errorf("expected maxFacts=10 after set, got %d", provider.maxFacts)
	}

	provider.SetMinScore(0.5)
	if provider.minScore != 0.5 {
		t.Errorf("expected minScore=0.5 after set, got %f", provider.minScore)
	}
}

func TestNewContextProvider(t *testing.T) {
	tmpDB, err := os.CreateTemp("", "context-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	store, err := NewStore(tmpDB.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Test with nil embedder (valid case)
	provider := NewContextProvider(store, nil)
	if provider == nil {
		t.Error("expected non-nil provider")
	}
	if provider.store != store {
		t.Error("store not set correctly")
	}

	// Test with real embedder config
	cfg := embeddings.Config{
		BaseURL: "http://localhost:11434",
		Model:   "nomic-embed-text",
	}
	embedder := embeddings.New(cfg)
	provider2 := NewContextProvider(store, embedder)
	if provider2.embedder != embedder {
		t.Error("embedder not set correctly")
	}
}

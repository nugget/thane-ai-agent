package contacts

import (
	"context"
	"strings"
	"testing"
)

func TestContextProvider_NilEmbedder(t *testing.T) {
	store := newTestStore(t)
	cp := NewContextProvider(store, nil)

	result, err := cp.GetContext(context.Background(), "hello")
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result with nil embedder, got %q", result)
	}
}

func TestContextProvider_EmptyMessage(t *testing.T) {
	store := newTestStore(t)
	emb := &fakeEmbedder{embedding: []float32{1, 0, 0}}
	cp := NewContextProvider(store, emb)

	result, err := cp.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for empty message, got %q", result)
	}
}

func TestContextProvider_NoContacts(t *testing.T) {
	store := newTestStore(t)
	emb := &fakeEmbedder{embedding: []float32{1, 0, 0}}
	cp := NewContextProvider(store, emb)

	result, err := cp.GetContext(context.Background(), "hello")
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result with no contacts, got %q", result)
	}
}

func TestContextProvider_ReturnsRelevant(t *testing.T) {
	store := newTestStore(t)

	c1 := &Contact{FormattedName: "Alice Relevant", Kind: "individual", AISummary: "Works at TechCo"}
	created1, _ := store.Upsert(c1)
	_ = store.SetEmbedding(created1.ID, []float32{0.9, 0.1, 0.0})
	_ = store.AddProperty(created1.ID, &Property{Property: "EMAIL", Value: "alice@techco.com"})
	_ = store.AddProperty(created1.ID, &Property{Property: "timezone", Value: "America/Chicago"})

	c2 := &Contact{FormattedName: "Bob Irrelevant", Kind: "individual", AISummary: "Completely unrelated"}
	created2, _ := store.Upsert(c2)
	_ = store.SetEmbedding(created2.ID, []float32{0.0, 0.0, 1.0})

	emb := &fakeEmbedder{embedding: []float32{1.0, 0.0, 0.0}}
	cp := NewContextProvider(store, emb)
	cp.SetMinScore(0.5)

	result, err := cp.GetContext(context.Background(), "tell me about Alice")
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}

	if !strings.Contains(result, "Alice Relevant") {
		t.Errorf("result should contain 'Alice Relevant', got %q", result)
	}
	if !strings.Contains(result, "Works at TechCo") {
		t.Errorf("result should contain 'Works at TechCo', got %q", result)
	}
	if !strings.Contains(result, "EMAIL") {
		t.Errorf("result should contain property 'EMAIL', got %q", result)
	}
	if !strings.Contains(result, "timezone") {
		t.Errorf("result should contain property 'timezone', got %q", result)
	}
	if !strings.Contains(result, "[known]") {
		t.Errorf("result should contain trust zone tag '[known]', got %q", result)
	}
	if strings.Contains(result, "Bob Irrelevant") {
		t.Errorf("result should not contain 'Bob Irrelevant'")
	}
}

func TestContextProvider_ScoreFiltering(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Low Score Contact", Kind: "individual"}
	created, _ := store.Upsert(c)
	_ = store.SetEmbedding(created.ID, []float32{0.0, 1.0, 0.0})

	emb := &fakeEmbedder{embedding: []float32{1.0, 0.0, 0.0}}
	cp := NewContextProvider(store, emb)
	cp.SetMinScore(0.5)

	result, err := cp.GetContext(context.Background(), "query")
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result for low-score contact, got %q", result)
	}
}

func TestContextProvider_MaxContacts(t *testing.T) {
	store := newTestStore(t)

	for i := 0; i < 5; i++ {
		c := &Contact{FormattedName: strings.Repeat("X", i+1), Kind: "individual"}
		created, _ := store.Upsert(c)
		_ = store.SetEmbedding(created.ID, []float32{0.8, 0.2, 0.0})
	}

	emb := &fakeEmbedder{embedding: []float32{1.0, 0.0, 0.0}}
	cp := NewContextProvider(store, emb)
	cp.SetMaxContacts(2)
	cp.SetMinScore(0.1)

	result, err := cp.GetContext(context.Background(), "query")
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	boldCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "**") {
			boldCount++
		}
	}
	if boldCount != 2 {
		t.Errorf("expected 2 contacts in output, got %d (result: %q)", boldCount, result)
	}
}

func TestContextProvider_TrustZoneTag(t *testing.T) {
	store := newTestStore(t)

	c := &Contact{FormattedName: "Trusted Alice", Kind: "individual", TrustZone: "trusted", AISummary: "A trusted friend"}
	created, _ := store.Upsert(c)
	_ = store.SetEmbedding(created.ID, []float32{0.9, 0.1, 0.0})

	emb := &fakeEmbedder{embedding: []float32{1.0, 0.0, 0.0}}
	cp := NewContextProvider(store, emb)
	cp.SetMinScore(0.1)

	result, err := cp.GetContext(context.Background(), "Alice")
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	if !strings.Contains(result, "[trusted]") {
		t.Errorf("result should contain '[trusted]' tag, got %q", result)
	}
}

func TestSetMaxContacts_ClampsToMin(t *testing.T) {
	store := newTestStore(t)
	cp := NewContextProvider(store, nil)

	cp.SetMaxContacts(0)
	if cp.maxContacts != 1 {
		t.Errorf("SetMaxContacts(0) = %d, want 1", cp.maxContacts)
	}

	cp.SetMaxContacts(-5)
	if cp.maxContacts != 1 {
		t.Errorf("SetMaxContacts(-5) = %d, want 1", cp.maxContacts)
	}

	cp.SetMaxContacts(10)
	if cp.maxContacts != 10 {
		t.Errorf("SetMaxContacts(10) = %d, want 10", cp.maxContacts)
	}
}

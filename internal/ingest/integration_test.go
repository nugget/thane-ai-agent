package ingest

import (
	"context"
	"os"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/facts"

	_ "github.com/mattn/go-sqlite3"
)

// mockEmbedder implements facts.EmbeddingClient for testing.
type mockEmbedder struct {
	calls int
}

func (m *mockEmbedder) Generate(ctx context.Context, text string) ([]float32, error) {
	m.calls++
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestArchitectureIngesterIntegration(t *testing.T) {
	// Create temp database
	tmpDB, err := os.CreateTemp("", "ingest-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create temp markdown file
	tmpMD, err := os.CreateTemp("", "arch-test-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpMD.Name())

	mdContent := `# Test Architecture

Introduction to the system.

## Components

### Database

SQLite for storage.

### API

REST endpoints.

## Deployment

Docker recommended.
`
	if _, err := tmpMD.WriteString(mdContent); err != nil {
		t.Fatal(err)
	}
	tmpMD.Close()

	// Open fact store
	store, err := facts.NewStore(tmpDB.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Create ingester with mock embedder
	mock := &mockEmbedder{}
	ingester := NewMarkdownIngester(store, mock, "test:integration", facts.CategoryArchitecture)

	// Run ingestion
	ctx := context.Background()
	count, err := ingester.IngestFile(ctx, tmpMD.Name())
	if err != nil {
		t.Fatalf("IngestFile failed: %v", err)
	}

	// Verify counts (4 chunks: intro, database, api, deployment)
	// Note: "Components" header has no content before its subheaders
	if count != 4 {
		t.Errorf("expected 4 facts, got %d", count)
	}

	// Verify embeddings were generated
	if mock.calls != 4 {
		t.Errorf("expected 4 embedding calls, got %d", mock.calls)
	}

	// Verify facts can be retrieved
	allFacts, err := store.GetAllWithEmbeddings()
	if err != nil {
		t.Fatal(err)
	}
	if len(allFacts) != 4 {
		t.Errorf("expected 4 facts with embeddings, got %d", len(allFacts))
	}

	// Verify semantic search works
	query := []float32{0.1, 0.2, 0.3} // Same as mock returns
	results, scores, err := store.SemanticSearch(query, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 search results, got %d", len(results))
	}
	// All should have similarity 1.0 (identical vectors)
	for i, s := range scores {
		if s < 0.99 {
			t.Errorf("result %d: expected similarity ~1.0, got %f", i, s)
		}
	}
}

func TestArchitectureIngesterReimport(t *testing.T) {
	// Create temp database
	tmpDB, err := os.CreateTemp("", "ingest-reimport-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	store, err := facts.NewStore(tmpDB.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ingester := NewMarkdownIngester(store, nil, "test:reimport", facts.CategoryArchitecture)
	ctx := context.Background()

	// First import
	content1 := "# Doc\n\nVersion 1 content.\n"
	count1, _ := ingester.IngestString(ctx, content1)
	if count1 != 1 {
		t.Errorf("first import: expected 1 fact, got %d", count1)
	}

	// Second import (should replace)
	content2 := "# Doc\n\nVersion 2 content.\n\n## New Section\n\nMore stuff.\n"
	count2, _ := ingester.IngestString(ctx, content2)
	if count2 != 2 {
		t.Errorf("second import: expected 2 facts, got %d", count2)
	}

	// Verify only 2 facts exist (not 3)
	stats := store.Stats()
	total, _ := stats["total"].(int)
	if total != 2 {
		t.Errorf("expected 2 total facts after reimport, got %d", total)
	}
}

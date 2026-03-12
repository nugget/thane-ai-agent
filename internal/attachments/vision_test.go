package attachments

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

// mockLLMClient implements llm.Client for testing.
type mockLLMClient struct {
	response string
	err      error
	calls    int
}

func (m *mockLLMClient) Chat(_ context.Context, _ string, _ []llm.Message, _ []map[string]any) (*llm.ChatResponse, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &llm.ChatResponse{
		Message: llm.Message{Content: m.response},
		Done:    true,
	}, nil
}

func (m *mockLLMClient) ChatStream(ctx context.Context, model string, msgs []llm.Message, tools []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	return m.Chat(ctx, model, msgs, tools)
}

func (m *mockLLMClient) Ping(_ context.Context) error { return nil }

func newTestAnalyzer(t *testing.T, client llm.Client) (*Analyzer, *Store) {
	t.Helper()
	store := newTestStore(t)
	analyzer := NewAnalyzer(store, AnalyzerConfig{
		Client:  client,
		Model:   "test-vision-model",
		Timeout: 5 * time.Second,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return analyzer, store
}

func ingestTestImage(t *testing.T, store *Store, content []byte) *Record {
	t.Helper()
	rec, err := store.Ingest(context.Background(), IngestParams{
		Source:       bytes.NewReader(content),
		ContentType:  "image/jpeg",
		OriginalName: "test.jpg",
		Channel:      "signal",
		Sender:       "+15551234567",
		ReceivedAt:   time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestAnalyze_HappyPath(t *testing.T) {
	client := &mockLLMClient{response: "A photo of a sunset over mountains"}
	analyzer, store := newTestAnalyzer(t, client)

	rec := ingestTestImage(t, store, []byte("fake jpeg data"))

	desc, err := analyzer.Analyze(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if desc != "A photo of a sunset over mountains" {
		t.Errorf("description = %q, want %q", desc, "A photo of a sunset over mountains")
	}
	if client.calls != 1 {
		t.Errorf("LLM calls = %d, want 1", client.calls)
	}

	// Verify cached in DB.
	found, err := store.ByID(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Description != "A photo of a sunset over mountains" {
		t.Errorf("cached description = %q", found.Description)
	}
	if found.AnalysisModel != "test-vision-model" {
		t.Errorf("analysis_model = %q", found.AnalysisModel)
	}
	if found.AnalyzedAt.IsZero() {
		t.Error("analyzed_at should be set")
	}
}

func TestAnalyze_CacheHit(t *testing.T) {
	client := &mockLLMClient{response: "cached result"}
	analyzer, store := newTestAnalyzer(t, client)

	rec := ingestTestImage(t, store, []byte("cache test data"))

	// First call — triggers LLM.
	_, err := analyzer.Analyze(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}

	// Re-read from DB to get the cached record.
	rec, err = store.ByID(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Second call — should return cached, no LLM call.
	desc, err := analyzer.Analyze(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if desc != "cached result" {
		t.Errorf("description = %q, want %q", desc, "cached result")
	}
	if client.calls != 1 {
		t.Errorf("LLM calls = %d, want 1 (should use cache)", client.calls)
	}
}

func TestAnalyze_HashReuse(t *testing.T) {
	client := &mockLLMClient{response: "shared description"}
	analyzer, store := newTestAnalyzer(t, client)

	content := []byte("identical image content")

	// Ingest first copy and analyze.
	rec1 := ingestTestImage(t, store, content)
	_, err := analyzer.Analyze(context.Background(), rec1)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", client.calls)
	}

	// Ingest second copy (different sender, same hash).
	rec2, err := store.Ingest(context.Background(), IngestParams{
		Source:      bytes.NewReader(content),
		ContentType: "image/jpeg",
		Channel:     "email",
		Sender:      "other@example.com",
		ReceivedAt:  time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Analyze second copy — should reuse via hash, no LLM call.
	desc, err := analyzer.Analyze(context.Background(), rec2)
	if err != nil {
		t.Fatal(err)
	}
	if desc != "shared description" {
		t.Errorf("description = %q, want %q", desc, "shared description")
	}
	if client.calls != 1 {
		t.Errorf("LLM calls = %d, want 1 (should reuse via hash)", client.calls)
	}
}

func TestAnalyze_NonImage(t *testing.T) {
	client := &mockLLMClient{response: "should not be called"}
	analyzer, store := newTestAnalyzer(t, client)

	// Ingest a non-image file.
	rec, err := store.Ingest(context.Background(), IngestParams{
		Source:      bytes.NewReader([]byte("not an image")),
		ContentType: "text/plain",
		ReceivedAt:  time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	desc, err := analyzer.Analyze(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if desc != "" {
		t.Errorf("expected empty description for non-image, got %q", desc)
	}
	if client.calls != 0 {
		t.Errorf("LLM calls = %d, want 0", client.calls)
	}
}

func TestAnalyze_LLMError(t *testing.T) {
	client := &mockLLMClient{err: fmt.Errorf("model unavailable")}
	analyzer, store := newTestAnalyzer(t, client)

	rec := ingestTestImage(t, store, []byte("error test data"))

	desc, err := analyzer.Analyze(context.Background(), rec)
	if err == nil {
		t.Fatal("expected error from failing LLM")
	}
	if desc != "" {
		t.Errorf("expected empty description on error, got %q", desc)
	}
}

func TestUpdateVision_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rec, err := store.Ingest(ctx, IngestParams{
		Source:      bytes.NewReader([]byte("vision round-trip")),
		ContentType: "image/png",
		ReceivedAt:  time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Initially empty.
	if rec.Description != "" || !rec.AnalyzedAt.IsZero() {
		t.Error("new record should have empty vision fields")
	}

	// Update vision.
	if err := store.UpdateVision(ctx, rec.ID, "test description", "llava:latest"); err != nil {
		t.Fatal(err)
	}

	// Re-read.
	found, err := store.ByID(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Description != "test description" {
		t.Errorf("Description = %q", found.Description)
	}
	if found.AnalysisModel != "llava:latest" {
		t.Errorf("AnalysisModel = %q", found.AnalysisModel)
	}
	if found.AnalyzedAt.IsZero() {
		t.Error("AnalyzedAt should be non-zero")
	}
}

func TestVisionByHash(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	content := []byte("hash lookup content")

	rec, err := store.Ingest(ctx, IngestParams{
		Source:      bytes.NewReader(content),
		ContentType: "image/jpeg",
		ReceivedAt:  time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// No analysis yet.
	_, _, ok := store.VisionByHash(ctx, rec.Hash)
	if ok {
		t.Error("VisionByHash should return false before analysis")
	}

	// Analyze.
	if err := store.UpdateVision(ctx, rec.ID, "found it", "test-model"); err != nil {
		t.Fatal(err)
	}

	// Now should find it.
	desc, model, ok := store.VisionByHash(ctx, rec.Hash)
	if !ok {
		t.Fatal("VisionByHash should return true after analysis")
	}
	if desc != "found it" {
		t.Errorf("description = %q", desc)
	}
	if model != "test-model" {
		t.Errorf("model = %q", model)
	}
}

func TestNewAnalyzer_Defaults(t *testing.T) {
	store := newTestStore(t)
	client := &mockLLMClient{}

	analyzer := NewAnalyzer(store, AnalyzerConfig{
		Client: client,
		Model:  "test-model",
	})

	if analyzer.prompt != defaultVisionPrompt {
		t.Errorf("prompt = %q, want default", analyzer.prompt)
	}
	if analyzer.timeout != defaultVisionTimeout {
		t.Errorf("timeout = %v, want %v", analyzer.timeout, defaultVisionTimeout)
	}
}

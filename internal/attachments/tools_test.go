package attachments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ingestTestRecord is a helper that ingests a record for tool tests.
func ingestTestRecord(t *testing.T, store *Store, name, contentType, channel, sender, convID string, content []byte) *Record {
	t.Helper()
	rec, err := store.Ingest(context.Background(), IngestParams{
		Source:         bytes.NewReader(content),
		OriginalName:   name,
		ContentType:    contentType,
		Channel:        channel,
		Sender:         sender,
		ConversationID: convID,
		ReceivedAt:     time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestSearch_NoFilters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ingestTestRecord(t, store, "a.jpg", "image/jpeg", "signal", "+15551111111", "conv-1", []byte("aaa"))
	ingestTestRecord(t, store, "b.png", "image/png", "email", "bob@example.com", "conv-2", []byte("bbb"))

	results, err := store.Search(ctx, SearchParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestSearch_ByChannel(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ingestTestRecord(t, store, "sig.jpg", "image/jpeg", "signal", "+15551111111", "conv-1", []byte("sig"))
	ingestTestRecord(t, store, "email.pdf", "application/pdf", "email", "bob@example.com", "conv-2", []byte("email"))

	results, err := store.Search(ctx, SearchParams{Channel: "signal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 signal result, got %d", len(results))
	}
	if results[0].OriginalName != "sig.jpg" {
		t.Errorf("expected sig.jpg, got %s", results[0].OriginalName)
	}
}

func TestSearch_ByContentType(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ingestTestRecord(t, store, "photo.jpg", "image/jpeg", "signal", "+1", "c", []byte("img"))
	ingestTestRecord(t, store, "doc.pdf", "application/pdf", "signal", "+1", "c", []byte("doc"))

	results, err := store.Search(ctx, SearchParams{ContentType: "image/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 image result, got %d", len(results))
	}
	if results[0].ContentType != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %s", results[0].ContentType)
	}
}

func TestSearch_TextQuery(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rec := ingestTestRecord(t, store, "sunset.jpg", "image/jpeg", "signal", "+15559999999", "conv-x", []byte("sunset"))
	ingestTestRecord(t, store, "receipt.pdf", "application/pdf", "email", "shop@store.com", "conv-y", []byte("receipt"))

	// Add a description to the image.
	if err := store.UpdateVision(ctx, rec.ID, "Beautiful sunset over mountains", "llava"); err != nil {
		t.Fatal(err)
	}

	// Search by description content.
	results, err := store.Search(ctx, SearchParams{Query: "sunset"})
	if err != nil {
		t.Fatal(err)
	}
	// Should match both (original_name "sunset.jpg" and description "Beautiful sunset").
	if len(results) < 1 {
		t.Fatal("expected at least 1 result for 'sunset'")
	}

	// Search by sender.
	results, err = store.Search(ctx, SearchParams{Query: "shop@store"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for sender query, got %d", len(results))
	}
}

func TestSearch_Limit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		ingestTestRecord(t, store, fmt.Sprintf("file%d.jpg", i), "image/jpeg", "signal", "+1", "c",
			[]byte(fmt.Sprintf("content-%d", i)))
	}

	results, err := store.Search(ctx, SearchParams{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results with limit, got %d", len(results))
	}
}

func TestToolsList_Basic(t *testing.T) {
	store := newTestStore(t)
	tools := NewTools(store, nil)

	ingestTestRecord(t, store, "photo.jpg", "image/jpeg", "signal", "+15551234567", "conv-1", []byte("photo"))

	result, err := tools.List(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	var summaries []attachmentSummary
	if err := json.Unmarshal([]byte(result), &summaries); err != nil {
		t.Fatalf("failed to parse list result: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Name != "photo.jpg" {
		t.Errorf("name = %q, want photo.jpg", summaries[0].Name)
	}
}

func TestToolsList_Empty(t *testing.T) {
	store := newTestStore(t)
	tools := NewTools(store, nil)

	result, err := tools.List(context.Background(), map[string]any{
		"channel": "nonexistent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No attachments found") {
		t.Errorf("expected 'No attachments found' message, got: %q", result)
	}
}

func TestToolsDescribe_Cached(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rec := ingestTestRecord(t, store, "dog.jpg", "image/jpeg", "signal", "+1", "c", []byte("dog"))
	if err := store.UpdateVision(ctx, rec.ID, "A happy golden retriever", "llava"); err != nil {
		t.Fatal(err)
	}

	// No analyzer — should return cached description.
	tools := NewTools(store, nil)
	result, err := tools.Describe(ctx, map[string]any{"id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result != "A happy golden retriever" {
		t.Errorf("description = %q", result)
	}
}

func TestToolsDescribe_NonImage(t *testing.T) {
	store := newTestStore(t)
	rec := ingestTestRecord(t, store, "doc.pdf", "application/pdf", "email", "a@b.com", "c", []byte("pdf"))

	tools := NewTools(store, nil)
	result, err := tools.Describe(context.Background(), map[string]any{"id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "not an image") {
		t.Errorf("expected 'not an image' message, got: %q", result)
	}
}

func TestToolsDescribe_NotFound(t *testing.T) {
	store := newTestStore(t)
	tools := NewTools(store, nil)

	_, err := tools.Describe(context.Background(), map[string]any{"id": "nonexistent-id"})
	if err == nil {
		t.Fatal("expected error for missing attachment")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestToolsDescribe_WithAnalyzer(t *testing.T) {
	client := &mockLLMClient{response: "A cat sitting on a keyboard"}
	store := newTestStore(t)
	analyzer := NewAnalyzer(store, AnalyzerConfig{
		Client: client,
		Model:  "test-vision-model",
	})

	tools := NewTools(store, analyzer)
	rec := ingestTestImage(t, store, []byte("cat image data"))

	result, err := tools.Describe(context.Background(), map[string]any{"id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result != "A cat sitting on a keyboard" {
		t.Errorf("description = %q", result)
	}
	if client.calls != 1 {
		t.Errorf("LLM calls = %d, want 1", client.calls)
	}
}

func TestToolsDescribe_Reanalyze(t *testing.T) {
	client := &mockLLMClient{response: "Updated description from better model"}
	store := newTestStore(t)
	analyzer := NewAnalyzer(store, AnalyzerConfig{
		Client: client,
		Model:  "test-vision-model",
	})

	tools := NewTools(store, analyzer)
	rec := ingestTestImage(t, store, []byte("reanalyze data"))

	// First analysis.
	_, err := tools.Describe(context.Background(), map[string]any{"id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Reanalyze.
	result, err := tools.Describe(context.Background(), map[string]any{
		"id":        rec.ID,
		"reanalyze": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "Updated description from better model" {
		t.Errorf("reanalysis result = %q", result)
	}
	if client.calls != 2 {
		t.Errorf("LLM calls = %d, want 2", client.calls)
	}
}

func TestToolsSearch_Basic(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	tools := NewTools(store, nil)

	rec := ingestTestRecord(t, store, "landscape.jpg", "image/jpeg", "signal", "+15551234567", "conv-1", []byte("landscape"))
	if err := store.UpdateVision(ctx, rec.ID, "Mountain landscape with snow", "llava"); err != nil {
		t.Fatal(err)
	}
	ingestTestRecord(t, store, "receipt.pdf", "application/pdf", "email", "shop@store.com", "conv-2", []byte("receipt"))

	result, err := tools.Search(ctx, map[string]any{"query": "mountain"})
	if err != nil {
		t.Fatal(err)
	}

	var summaries []attachmentSummary
	if err := json.Unmarshal([]byte(result), &summaries); err != nil {
		t.Fatalf("failed to parse search result: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 result for 'mountain', got %d", len(summaries))
	}
	if summaries[0].Name != "landscape.jpg" {
		t.Errorf("name = %q, want landscape.jpg", summaries[0].Name)
	}
}

func TestToolsSearch_NoResults(t *testing.T) {
	store := newTestStore(t)
	tools := NewTools(store, nil)

	result, err := tools.Search(context.Background(), map[string]any{"query": "nonexistent"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No attachments found") {
		t.Errorf("expected 'No attachments found' message, got: %q", result)
	}
}

func TestToolsSearch_MissingQuery(t *testing.T) {
	store := newTestStore(t)
	tools := NewTools(store, nil)

	_, err := tools.Search(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
}

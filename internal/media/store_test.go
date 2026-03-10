package media

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestNewMediaStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_engagement.db")
	store, err := NewMediaStore(dbPath, nil)
	if err != nil {
		t.Fatalf("NewMediaStore() error: %v", err)
	}
	defer store.Close()
}

func TestRecordAnalysis(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_engagement.db")
	store, err := NewMediaStore(dbPath, nil)
	if err != nil {
		t.Fatalf("NewMediaStore() error: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	e := &Engagement{
		EntryURL:      "https://youtube.com/watch?v=abc123",
		FeedID:        "feed123",
		AnalysisPath:  "/vault/Channels/test/2026-03-09-test.md",
		AnalysisDepth: "summary",
		Topics:        []string{"ai", "machine-learning"},
		TrustZone:     "known",
		QualityScore:  0.85,
		AnalyzedAt:    time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC),
		SessionID:     "session-1",
	}

	if err := store.RecordAnalysis(ctx, e); err != nil {
		t.Fatalf("RecordAnalysis() error: %v", err)
	}

	// ID should have been auto-generated.
	if e.ID == "" {
		t.Error("expected auto-generated ID, got empty string")
	}
}

func TestHasBeenAnalyzed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_engagement.db")
	store, err := NewMediaStore(dbPath, nil)
	if err != nil {
		t.Fatalf("NewMediaStore() error: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	url := "https://youtube.com/watch?v=xyz789"

	// Should not be analyzed yet.
	analyzed, err := store.HasBeenAnalyzed(ctx, url)
	if err != nil {
		t.Fatalf("HasBeenAnalyzed() error: %v", err)
	}
	if analyzed {
		t.Error("HasBeenAnalyzed() = true before any analysis recorded")
	}

	// Record analysis.
	e := &Engagement{
		EntryURL:  url,
		Topics:    []string{"test"},
		TrustZone: "unknown",
	}
	if err := store.RecordAnalysis(ctx, e); err != nil {
		t.Fatalf("RecordAnalysis() error: %v", err)
	}

	// Should be analyzed now.
	analyzed, err = store.HasBeenAnalyzed(ctx, url)
	if err != nil {
		t.Fatalf("HasBeenAnalyzed() error: %v", err)
	}
	if !analyzed {
		t.Error("HasBeenAnalyzed() = false after analysis recorded")
	}
}

func TestHasBeenAnalyzed_DifferentURLs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_engagement.db")
	store, err := NewMediaStore(dbPath, nil)
	if err != nil {
		t.Fatalf("NewMediaStore() error: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Record analysis for URL A.
	e := &Engagement{
		EntryURL:  "https://example.com/a",
		Topics:    []string{"a"},
		TrustZone: "unknown",
	}
	if err := store.RecordAnalysis(ctx, e); err != nil {
		t.Fatalf("RecordAnalysis() error: %v", err)
	}

	// URL B should not be marked as analyzed.
	analyzed, err := store.HasBeenAnalyzed(ctx, "https://example.com/b")
	if err != nil {
		t.Fatalf("HasBeenAnalyzed() error: %v", err)
	}
	if analyzed {
		t.Error("HasBeenAnalyzed() = true for unrelated URL")
	}
}

func TestRecordAnalysis_AutoTimestamp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_engagement.db")
	store, err := NewMediaStore(dbPath, nil)
	if err != nil {
		t.Fatalf("NewMediaStore() error: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	before := time.Now().UTC()

	e := &Engagement{
		EntryURL:  "https://example.com/auto-ts",
		Topics:    []string{},
		TrustZone: "unknown",
	}
	if err := store.RecordAnalysis(ctx, e); err != nil {
		t.Fatalf("RecordAnalysis() error: %v", err)
	}

	// AnalyzedAt should have been auto-set.
	if e.AnalyzedAt.Before(before) {
		t.Errorf("AnalyzedAt %v is before test start %v", e.AnalyzedAt, before)
	}
}

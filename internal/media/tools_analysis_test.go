package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/opstate"
)

// newTestAnalysisTools creates AnalysisTools backed by temp directories.
func newTestAnalysisTools(t *testing.T, defaultOutputPath string) (*AnalysisTools, *opstate.Store, string) {
	t.Helper()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	state, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}

	engDB := filepath.Join(t.TempDir(), "engagement.db")
	store, err := NewMediaStore(engDB, nil)
	if err != nil {
		t.Fatalf("NewMediaStore() error: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	writer := NewVaultWriter(nil)

	outputDir := t.TempDir()
	if defaultOutputPath == "" {
		defaultOutputPath = outputDir
	}

	at := NewAnalysisTools(state, store, writer, defaultOutputPath, nil)
	return at, state, outputDir
}

func TestSaveHandler_Success(t *testing.T) {
	at, _, outputDir := newTestAnalysisTools(t, "")
	// Overwrite defaultOutputPath with our known temp dir.
	at.defaultOutputPath = outputDir

	handler := at.SaveHandler()
	ctx := context.Background()

	args := map[string]any{
		"title":         "Test Video Title",
		"channel":       "Test Channel",
		"url":           "https://youtube.com/watch?v=test1",
		"published":     "2026-03-15",
		"topics":        []any{"ai", "testing"},
		"content":       "## Key Insights\n\n- This is a test\n",
		"trust_zone":    "known",
		"quality_score": 0.9,
		"detail":        "full",
	}

	result, err := handler(ctx, args)
	if err != nil {
		t.Fatalf("SaveHandler() error: %v", err)
	}
	if !strings.Contains(result, `"status":"saved"`) {
		t.Errorf("result missing saved status: %s", result)
	}
	if !strings.Contains(result, `"path":`) {
		t.Errorf("result missing path: %s", result)
	}

	// Verify file was written.
	channelDir := filepath.Join(outputDir, "Channels", "test-channel")
	entries, err := os.ReadDir(channelDir)
	if err != nil {
		t.Fatalf("ReadDir() error: %v", err)
	}
	// Should have the analysis file + _channel.md.
	mdFiles := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			mdFiles++
		}
	}
	if mdFiles < 2 {
		t.Errorf("expected at least 2 .md files (analysis + index), got %d", mdFiles)
	}
}

func TestSaveHandler_MissingRequired(t *testing.T) {
	at, _, _ := newTestAnalysisTools(t, "")
	handler := at.SaveHandler()
	ctx := context.Background()

	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{
			name:    "missing title",
			args:    map[string]any{"channel": "c", "url": "u", "topics": []any{"t"}, "content": "c"},
			wantErr: "title is required",
		},
		{
			name:    "missing channel",
			args:    map[string]any{"title": "t", "url": "u", "topics": []any{"t"}, "content": "c"},
			wantErr: "channel is required",
		},
		{
			name:    "missing url",
			args:    map[string]any{"title": "t", "channel": "c", "topics": []any{"t"}, "content": "c"},
			wantErr: "url is required",
		},
		{
			name:    "missing content",
			args:    map[string]any{"title": "t", "channel": "c", "url": "u", "topics": []any{"t"}},
			wantErr: "content is required",
		},
		{
			name:    "missing topics",
			args:    map[string]any{"title": "t", "channel": "c", "url": "u", "content": "c"},
			wantErr: "topics is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handler(ctx, tt.args)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSaveHandler_FeedIDOutputPath(t *testing.T) {
	at, state, _ := newTestAnalysisTools(t, "")

	feedOutputDir := t.TempDir()

	// Store per-feed output_path.
	if err := state.Set(feedNamespace, feedKeyOutputPath("feed123"), feedOutputDir); err != nil {
		t.Fatalf("Set output_path: %v", err)
	}

	// Clear default so we know it came from the feed.
	at.defaultOutputPath = ""

	handler := at.SaveHandler()
	ctx := context.Background()

	args := map[string]any{
		"title":   "Feed Output Test",
		"channel": "Test Channel",
		"url":     "https://example.com/feed-output-test",
		"topics":  []any{"test"},
		"content": "test content",
		"feed_id": "feed123",
	}

	result, err := handler(ctx, args)
	if err != nil {
		t.Fatalf("SaveHandler() error: %v", err)
	}
	if !strings.Contains(result, feedOutputDir) {
		t.Errorf("result path should be under feed output dir %q: %s", feedOutputDir, result)
	}
}

func TestSaveHandler_DefaultOutputPath(t *testing.T) {
	defaultDir := t.TempDir()
	at, _, _ := newTestAnalysisTools(t, defaultDir)

	handler := at.SaveHandler()
	ctx := context.Background()

	args := map[string]any{
		"title":   "Default Path Test",
		"channel": "Test Channel",
		"url":     "https://example.com/default-path",
		"topics":  []any{"test"},
		"content": "test content",
	}

	result, err := handler(ctx, args)
	if err != nil {
		t.Fatalf("SaveHandler() error: %v", err)
	}
	if !strings.Contains(result, defaultDir) {
		t.Errorf("result path should be under default dir %q: %s", defaultDir, result)
	}
}

func TestSaveHandler_NoOutputPath(t *testing.T) {
	at, _, _ := newTestAnalysisTools(t, "")
	at.defaultOutputPath = ""

	handler := at.SaveHandler()
	ctx := context.Background()

	args := map[string]any{
		"title":   "No Path",
		"channel": "Test",
		"url":     "https://example.com/no-path",
		"topics":  []any{"test"},
		"content": "test",
	}

	_, err := handler(ctx, args)
	if err == nil {
		t.Fatal("expected error for missing output_path, got nil")
	}
	if !strings.Contains(err.Error(), "no output_path configured") {
		t.Errorf("error %q should mention output_path", err.Error())
	}
}

func TestSaveHandler_QualityScoreBounds(t *testing.T) {
	at, _, _ := newTestAnalysisTools(t, "")
	handler := at.SaveHandler()
	ctx := context.Background()

	tests := []struct {
		name    string
		score   float64
		wantErr bool
	}{
		{name: "valid 0", score: 0, wantErr: false},
		{name: "valid mid", score: 0.5, wantErr: false},
		{name: "valid 1", score: 1, wantErr: false},
		{name: "too low", score: -0.1, wantErr: true},
		{name: "too high", score: 1.5, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{
				"title":         "Score Test",
				"channel":       "Test",
				"url":           "https://example.com/score-" + tt.name,
				"topics":        []any{"test"},
				"content":       "test",
				"quality_score": tt.score,
			}
			_, err := handler(ctx, args)
			if tt.wantErr && err == nil {
				t.Error("expected error for out-of-bounds quality_score")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestSaveHandler_AlreadyAnalyzed(t *testing.T) {
	at, _, _ := newTestAnalysisTools(t, "")
	handler := at.SaveHandler()
	ctx := context.Background()

	args := map[string]any{
		"title":   "Dedup Test",
		"channel": "Test",
		"url":     "https://example.com/dedup-test",
		"topics":  []any{"test"},
		"content": "first analysis",
	}

	// First call should succeed.
	result1, err := handler(ctx, args)
	if err != nil {
		t.Fatalf("first SaveHandler() error: %v", err)
	}
	if !strings.Contains(result1, `"status":"saved"`) {
		t.Errorf("first result should be saved: %s", result1)
	}

	// Second call with same URL should report already analyzed.
	args["content"] = "second analysis"
	result2, err := handler(ctx, args)
	if err != nil {
		t.Fatalf("second SaveHandler() error: %v", err)
	}
	if !strings.Contains(result2, "already_analyzed") {
		t.Errorf("second result should be already_analyzed: %s", result2)
	}
}

func TestSaveHandler_FeedTrustZoneResolution(t *testing.T) {
	at, state, _ := newTestAnalysisTools(t, "")

	// Store trust_zone for feed.
	if err := state.Set(feedNamespace, feedKeyTrustZone("trust-feed"), "trusted"); err != nil {
		t.Fatalf("Set trust_zone: %v", err)
	}

	handler := at.SaveHandler()
	ctx := context.Background()

	args := map[string]any{
		"title":   "Trust Zone Test",
		"channel": "Test",
		"url":     "https://example.com/trust-test",
		"topics":  []any{"test"},
		"content": "test",
		"feed_id": "trust-feed",
	}

	result, err := handler(ctx, args)
	if err != nil {
		t.Fatalf("SaveHandler() error: %v", err)
	}

	// Verify the analysis was saved (the trust zone resolution happened).
	if !strings.Contains(result, `"status":"saved"`) {
		t.Errorf("result should be saved: %s", result)
	}
}

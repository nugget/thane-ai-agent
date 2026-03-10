package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple", input: "Hello World", want: "hello-world"},
		{name: "special chars", input: "AI & Machine Learning!", want: "ai-machine-learning"},
		{name: "colons", input: "Part 1: The Beginning", want: "part-1-the-beginning"},
		{name: "multiple spaces", input: "a   b   c", want: "a-b-c"},
		{name: "leading trailing", input: "---hello---", want: "hello"},
		{name: "empty string", input: "", want: "untitled"},
		{name: "only special", input: "!!!@@@", want: "untitled"},
		{name: "numbers", input: "Episode 42", want: "episode-42"},
		{name: "hyphens preserved", input: "already-slugified", want: "already-slugified"},
		{name: "mixed case", input: "Lex Fridman Podcast", want: "lex-fridman-podcast"},
		{name: "parentheses", input: "Annie Jacobsen (Interview)", want: "annie-jacobsen-interview"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestShortHash(t *testing.T) {
	h := shortHash("https://youtube.com/watch?v=abc123")
	if len(h) != 4 {
		t.Errorf("shortHash length = %d, want 4", len(h))
	}

	// Same input should produce same hash.
	h2 := shortHash("https://youtube.com/watch?v=abc123")
	if h != h2 {
		t.Errorf("shortHash not deterministic: %q != %q", h, h2)
	}

	// Different input should produce different hash.
	h3 := shortHash("https://youtube.com/watch?v=xyz789")
	if h == h3 {
		t.Errorf("shortHash collision: %q == %q for different inputs", h, h3)
	}
}

func TestWriteAnalysis(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewVaultWriter(nil)

	page := &AnalysisPage{
		Title:        "Annie Jacobsen: Nuclear War & Area 51",
		Channel:      "Lex Fridman Podcast",
		URL:          "https://youtube.com/watch?v=test123",
		Published:    "2026-03-15",
		Topics:       []string{"nuclear-war", "area-51", "existential-risk"},
		TrustZone:    "known",
		QualityScore: 0.85,
		AnalyzedAt:   time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC),
		Content:      "## Key Insights\n\n- Nuclear war is bad\n- Area 51 exists\n",
	}

	path, err := w.WriteAnalysis(tmpDir, page)
	if err != nil {
		t.Fatalf("WriteAnalysis() error: %v", err)
	}

	// Verify file exists.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("analysis file does not exist: %s", path)
	}

	// Verify path structure.
	rel, _ := filepath.Rel(tmpDir, path)
	if !strings.HasPrefix(rel, filepath.Join("Channels", "lex-fridman-podcast")) {
		t.Errorf("path %q not under expected channel directory", rel)
	}
	if !strings.HasPrefix(filepath.Base(path), "2026-03-15-") {
		t.Errorf("filename %q doesn't start with date prefix", filepath.Base(path))
	}

	// Verify content.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	content := string(data)

	// Check frontmatter.
	if !strings.HasPrefix(content, "---\n") {
		t.Error("missing YAML frontmatter opening")
	}
	if !strings.Contains(content, `title: "Annie Jacobsen: Nuclear War & Area 51"`) {
		t.Error("missing title in frontmatter")
	}
	if !strings.Contains(content, `channel: "Lex Fridman Podcast"`) {
		t.Error("missing channel in frontmatter")
	}
	if !strings.Contains(content, `trust_zone: "known"`) {
		t.Error("missing trust_zone in frontmatter")
	}
	if !strings.Contains(content, `quality_score: 0.85`) {
		t.Error("missing quality_score in frontmatter")
	}
	if !strings.Contains(content, `- "nuclear-war"`) {
		t.Error("missing topic in frontmatter")
	}

	// Check body.
	if !strings.Contains(content, "# Annie Jacobsen: Nuclear War & Area 51") {
		t.Error("missing H1 heading")
	}
	if !strings.Contains(content, "## Key Insights") {
		t.Error("missing analysis body content")
	}
}

func TestWriteAnalysis_CreatesDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	deepPath := filepath.Join(tmpDir, "nested", "vault", "path")
	w := NewVaultWriter(nil)

	page := &AnalysisPage{
		Title:      "Test",
		Channel:    "Test Channel",
		URL:        "https://example.com/test",
		Topics:     []string{"test"},
		TrustZone:  "unknown",
		AnalyzedAt: time.Now().UTC(),
		Content:    "test content",
	}

	path, err := w.WriteAnalysis(deepPath, page)
	if err != nil {
		t.Fatalf("WriteAnalysis() error: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("file not created at %s", path)
	}
}

func TestWriteAnalysis_EmptyPublished(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewVaultWriter(nil)

	page := &AnalysisPage{
		Title:      "No Date Video",
		Channel:    "Some Channel",
		URL:        "https://example.com/nodate",
		Topics:     []string{"test"},
		TrustZone:  "unknown",
		AnalyzedAt: time.Now().UTC(),
		Content:    "content",
	}

	path, err := w.WriteAnalysis(tmpDir, page)
	if err != nil {
		t.Fatalf("WriteAnalysis() error: %v", err)
	}

	// Filename should start with today's date.
	today := time.Now().UTC().Format("2006-01-02")
	base := filepath.Base(path)
	if !strings.HasPrefix(base, today+"-") {
		t.Errorf("filename %q doesn't start with today's date %q", base, today)
	}
}

func TestWriteAnalysis_ChannelIndex(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewVaultWriter(nil)

	// Write two analyses for the same channel.
	for i, title := range []string{"First Video", "Second Video"} {
		page := &AnalysisPage{
			Title:      title,
			Channel:    "Index Test",
			URL:        "https://example.com/" + title,
			Published:  "2026-03-15",
			Topics:     []string{"test"},
			TrustZone:  "known",
			AnalyzedAt: time.Date(2026, 3, 15, i, 0, 0, 0, time.UTC),
			Content:    "content for " + title,
		}
		if _, err := w.WriteAnalysis(tmpDir, page); err != nil {
			t.Fatalf("WriteAnalysis(%q) error: %v", title, err)
		}
	}

	// Verify _channel.md exists.
	indexPath := filepath.Join(tmpDir, "Channels", "index-test", "_channel.md")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("ReadFile(_channel.md) error: %v", err)
	}

	index := string(data)

	// Check frontmatter.
	if !strings.Contains(index, `channel: "Index Test"`) {
		t.Error("channel index missing channel name")
	}

	// Check both entries are listed.
	if !strings.Contains(index, "First Video") {
		t.Error("channel index missing First Video")
	}
	if !strings.Contains(index, "Second Video") {
		t.Error("channel index missing Second Video")
	}

	// Check wiki-link format.
	if !strings.Contains(index, "[[") {
		t.Error("channel index missing wiki-links")
	}
}

func TestWriteAnalysis_EmptyOutputPath(t *testing.T) {
	w := NewVaultWriter(nil)
	page := &AnalysisPage{
		Title:   "Test",
		Channel: "Test",
		URL:     "https://example.com",
		Topics:  []string{"test"},
		Content: "content",
	}
	_, err := w.WriteAnalysis("", page)
	if err == nil {
		t.Error("WriteAnalysis() with empty output path should error")
	}
}

func TestWriteAnalysis_TrailingNewline(t *testing.T) {
	tmpDir := t.TempDir()
	w := NewVaultWriter(nil)

	// Content without trailing newline.
	page := &AnalysisPage{
		Title:      "Newline Test",
		Channel:    "Test",
		URL:        "https://example.com/newline",
		Topics:     []string{"test"},
		AnalyzedAt: time.Now().UTC(),
		Content:    "no trailing newline",
	}

	path, err := w.WriteAnalysis(tmpDir, page)
	if err != nil {
		t.Fatalf("WriteAnalysis() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	if !strings.HasSuffix(string(data), "\n") {
		t.Error("file should end with trailing newline")
	}
}

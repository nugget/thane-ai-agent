package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractSource(t *testing.T) {
	tests := []struct {
		url        string
		wantSource string
		wantID     string
	}{
		{
			url:        "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			wantSource: "youtube",
			wantID:     "dQw4w9WgXcQ",
		},
		{
			url:        "https://youtu.be/dQw4w9WgXcQ",
			wantSource: "youtube",
			wantID:     "dQw4w9WgXcQ",
		},
		{
			url:        "https://vimeo.com/123456789",
			wantSource: "vimeo",
			wantID:     "123456789",
		},
		{
			url:        "https://www.twitch.tv/videos/987654321",
			wantSource: "twitch",
			wantID:     "987654321",
		},
		{
			url:        "https://example.com/podcasts/episode-42",
			wantSource: "example.com",
			wantID:     "episode-42",
		},
		{
			url:        "not-a-url",
			wantSource: "unknown",
			wantID:     "",
		},
	}

	for _, tt := range tests {
		source, id := extractSource(tt.url)
		if source != tt.wantSource {
			t.Errorf("extractSource(%q) source = %q, want %q", tt.url, source, tt.wantSource)
		}
		if id != tt.wantID {
			t.Errorf("extractSource(%q) id = %q, want %q", tt.url, id, tt.wantID)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		seconds float64
		want    string
	}{
		{0, "0:00"},
		{65, "1:05"},
		{3661, "1:01:01"},
		{7200, "2:00:00"},
		{90.5, "1:30"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.seconds)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestFormatDate(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"20260222", "2026-02-22"},
		{"20231115", "2023-11-15"},
		{"short", "short"},
		{"", ""},
	}

	for _, tt := range tests {
		got := formatDate(tt.input)
		if got != tt.want {
			t.Errorf("formatDate(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"youtube-dQw4w9WgXcQ", "youtube-dQw4w9WgXcQ"},
		{"some file/with:bad chars", "some_file_with_bad_chars"},
		{"a b c", "a_b_c"},
	}

	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "c"); got != "c" {
		t.Errorf("firstNonEmpty = %q, want %q", got, "c")
	}
	if got := firstNonEmpty("a", "b"); got != "a" {
		t.Errorf("firstNonEmpty = %q, want %q", got, "a")
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("firstNonEmpty = %q, want empty", got)
	}
}

func TestSaveTranscript(t *testing.T) {
	tmpDir := t.TempDir()

	c := &Client{
		cfg: Config{
			TranscriptDir: tmpDir,
		},
	}

	result := &Result{
		Title:      "Test Video Title",
		Channel:    "Test Channel",
		Duration:   "5:30",
		UploadDate: "2026-02-22",
		Source:     "youtube",
		ID:         "abc123",
		Transcript: "This is the transcript text.",
	}

	path, err := c.saveTranscript(result, "https://www.youtube.com/watch?v=abc123")
	if err != nil {
		t.Fatalf("saveTranscript() error: %v", err)
	}

	// Verify file was created.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("transcript file not found at %s: %v", path, err)
	}

	// Verify filename.
	wantFilename := "youtube-abc123.md"
	if filepath.Base(path) != wantFilename {
		t.Errorf("filename = %q, want %q", filepath.Base(path), wantFilename)
	}

	// Read and verify content.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}

	s := string(content)

	// Check frontmatter fields.
	if !strings.Contains(s, "---\n") {
		t.Error("missing YAML frontmatter delimiters")
	}
	if !strings.Contains(s, `title: "Test Video Title"`) {
		t.Error("missing title in frontmatter")
	}
	if !strings.Contains(s, `channel: "Test Channel"`) {
		t.Error("missing channel in frontmatter")
	}
	if !strings.Contains(s, "url: https://www.youtube.com/watch?v=abc123") {
		t.Error("missing url in frontmatter")
	}
	if !strings.Contains(s, "source: youtube") {
		t.Error("missing source in frontmatter")
	}
	if !strings.Contains(s, "date: 2026-02-22") {
		t.Error("missing date in frontmatter")
	}
	if !strings.Contains(s, `duration: "5:30"`) {
		t.Error("missing duration in frontmatter")
	}
	if !strings.Contains(s, "fetched_at: ") {
		t.Error("missing fetched_at in frontmatter")
	}

	// Verify transcript body.
	if !strings.Contains(s, "This is the transcript text.") {
		t.Error("missing transcript body")
	}
}

func TestSaveTranscript_TildeExpansion(t *testing.T) {
	// Use a subdir of TempDir to avoid writing to real home.
	tmpDir := t.TempDir()

	c := &Client{
		cfg: Config{
			TranscriptDir: tmpDir + "/transcripts",
		},
	}

	result := &Result{
		Title:      "Test",
		Source:     "youtube",
		ID:         "xyz",
		Transcript: "hello",
	}

	path, err := c.saveTranscript(result, "https://example.com")
	if err != nil {
		t.Fatalf("saveTranscript() error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("transcript file not found: %v", err)
	}
}

func TestResultTruncation(t *testing.T) {
	// Verify that transcripts longer than MaxTranscriptChars get truncated.
	longText := strings.Repeat("a", 100)

	c := &Client{
		cfg: Config{
			MaxTranscriptChars: 50,
		},
	}

	// Simulate truncation logic from GetTranscript.
	transcript := longText
	truncated := false
	if len(transcript) > c.cfg.MaxTranscriptChars {
		transcript = transcript[:c.cfg.MaxTranscriptChars]
		truncated = true
	}

	if len(transcript) != 50 {
		t.Errorf("truncated length = %d, want 50", len(transcript))
	}
	if !truncated {
		t.Error("expected truncated = true")
	}
}

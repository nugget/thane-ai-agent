package ingest

import (
	"strings"
	"testing"
)

func TestParseMarkdown(t *testing.T) {
	content := `# Main Title

Some intro text.

## Section One

Content for section one.
More content here.

### Subsection A

Details about subsection A.

## Section Two

Content for section two.

### Subsection B

Details about B.

### Subsection C

Details about C.
`

	chunks := parseMarkdown(strings.NewReader(content))

	expected := []struct {
		key     string
		hasText string
	}{
		{"main-title", "intro text"},
		{"main-title/section-one", "section one"},
		{"main-title/section-one/subsection-a", "subsection A"},
		{"main-title/section-two", "section two"},
		{"main-title/section-two/subsection-b", "Details about B"},
		{"main-title/section-two/subsection-c", "Details about C"},
	}

	if len(chunks) != len(expected) {
		t.Fatalf("expected %d chunks, got %d", len(expected), len(chunks))
	}

	for i, exp := range expected {
		if chunks[i].Key != exp.key {
			t.Errorf("chunk %d: expected key %q, got %q", i, exp.key, chunks[i].Key)
		}
		if !strings.Contains(chunks[i].Content, exp.hasText) {
			t.Errorf("chunk %d: expected content to contain %q, got %q", i, exp.hasText, chunks[i].Content)
		}
	}
}

func TestParseMarkdownWithCodeBlocks(t *testing.T) {
	content := "## Config Example\n\nHere's how to configure:\n\n```yaml\nkey: value\nanother: thing\n```\n\nMore text after code.\n"

	chunks := parseMarkdown(strings.NewReader(content))

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	// Code block should be preserved
	if !strings.Contains(chunks[0].Content, "```yaml") {
		t.Error("code block opening not preserved")
	}
	if !strings.Contains(chunks[0].Content, "key: value") {
		t.Error("code block content not preserved")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Simple Title", "simple-title"},
		{"API Server", "api-server"},
		{"Phase 1: Foundation", "phase-1-foundation"},
		{"OpenAI-Compatible API", "openai-compatible-api"},
		{"  Spaces  ", "spaces"},
	}

	for _, tc := range tests {
		got := slugify(tc.input)
		if got != tc.expected {
			t.Errorf("slugify(%q): expected %q, got %q", tc.input, tc.expected, got)
		}
	}
}

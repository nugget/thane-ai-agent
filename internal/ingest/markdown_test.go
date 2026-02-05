package ingest

import (
	"strings"
	"testing"
)

func TestParseMarkdown(t *testing.T) {
	content := `# Houseplant Care Guide

A reference for common indoor plants and their needs.

## Watering

Most houseplants prefer soil that dries slightly between waterings.
Overwatering is the most common cause of plant death.

### Succulents

Water succulents every 2-3 weeks. They store water in their leaves.

## Light Requirements

Different plants have different light needs based on their natural habitat.

### Low Light Plants

Pothos and snake plants thrive in low light conditions.
They can survive in rooms with no windows.

### Bright Indirect Light

Monstera and fiddle leaf figs prefer bright indirect light.
`

	chunks := parseMarkdown(strings.NewReader(content))

	expected := []struct {
		key     string
		hasText string
	}{
		{"houseplant-care-guide", "indoor plants"},
		{"houseplant-care-guide/watering", "dries slightly"},
		{"houseplant-care-guide/watering/succulents", "2-3 weeks"},
		{"houseplant-care-guide/light-requirements", "natural habitat"},
		{"houseplant-care-guide/light-requirements/low-light-plants", "Pothos"},
		{"houseplant-care-guide/light-requirements/bright-indirect-light", "Monstera"},
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
	content := `## Watering Schedule

Here's a simple watering schedule for common plants:

` + "```" + `
Plant           | Frequency
----------------|----------
Snake Plant     | Every 3 weeks
Pothos          | Weekly
Succulent       | Every 2 weeks
` + "```" + `

Adjust based on humidity and season.
`

	chunks := parseMarkdown(strings.NewReader(content))

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	// Code block (table) should be preserved
	if !strings.Contains(chunks[0].Content, "Snake Plant") {
		t.Error("code block content not preserved")
	}
	if !strings.Contains(chunks[0].Content, "Adjust based on") {
		t.Error("text after code block not preserved")
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

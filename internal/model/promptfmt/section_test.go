package promptfmt

import (
	"strings"
	"testing"
)

func TestAppendMarkdownSection(t *testing.T) {
	var sb strings.Builder
	if !AppendMarkdownSection(&sb, 2, " Context ", "\nbody\n") {
		t.Fatal("AppendMarkdownSection returned false for non-empty body")
	}
	if got, want := sb.String(), "## Context\n\nbody"; got != want {
		t.Fatalf("section = %q, want %q", got, want)
	}

	if !AppendMarkdownSection(&sb, 7, "Next", "tail") {
		t.Fatal("AppendMarkdownSection returned false for second section")
	}
	if got, want := sb.String(), "## Context\n\nbody\n\n###### Next\n\ntail"; got != want {
		t.Fatalf("two sections = %q, want %q", got, want)
	}
}

func TestAppendMarkdownSectionBlank(t *testing.T) {
	var sb strings.Builder
	if AppendMarkdownSection(&sb, 2, "Context", " \n\t ") {
		t.Fatal("AppendMarkdownSection returned true for blank body")
	}
	if got := sb.String(); got != "" {
		t.Fatalf("blank body wrote %q", got)
	}
}

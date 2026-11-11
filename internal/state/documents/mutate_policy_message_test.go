package documents

import (
	"strings"
	"testing"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// TestDocumentWriteMessageBorrowsFacets pins the commit-message
// contract: a faceted document's status_line joins the subject and its
// digest becomes the body, so the root's git log reads as a timeline
// of the document's own verdicts; an unfaceted document keeps the
// plain mechanical subject; and a status_line longer than a subject
// holds, whether a valid line of long words or a runaway one from a
// non-publish write, is clamped rather than allowed to swallow the
// subject line.
func TestDocumentWriteMessageBorrowsFacets(t *testing.T) {
	t.Parallel()

	t.Run("unfaceted document keeps the mechanical subject", func(t *testing.T) {
		msg := documentWriteMessage("doc_write", "self", "metacognitive.md", "---\ntitle: X\n---\n# State\n\nprose\n")
		if msg != "doc_write self:metacognitive.md" {
			t.Fatalf("message = %q", msg)
		}
	})

	t.Run("status_line joins the subject, digest becomes the body", func(t *testing.T) {
		raw := "---\ntitle: X\n---\n## Status Line\n\npanel clean, baselines steady\n\n## Digest\n\nNo open concerns; archivist backlog nominal.\n\n## Details\n\nworking memory\n"
		msg := documentWriteMessage("doc_write", "self", "metacognitive.md", raw)
		lines := strings.SplitN(msg, "\n", 3)
		if lines[0] != "doc_write self:metacognitive.md — panel clean, baselines steady" {
			t.Fatalf("subject = %q", lines[0])
		}
		if !strings.Contains(msg, "\n\nNo open concerns; archivist backlog nominal.") {
			t.Fatalf("digest missing from body: %q", msg)
		}
	})

	t.Run("multi-line status_line flattens, oversize clamps", func(t *testing.T) {
		long := strings.Repeat("verdict ", 40) // ~320 runes
		raw := "## Status Line\n\n" + long + "\n\n## Details\n\nbody\n"
		msg := documentWriteMessage("doc_write", "kb", "x.md", raw)
		subject := strings.SplitN(msg, "\n", 2)[0]
		if got := len([]rune(subject)); got > len([]rune("doc_write kb:x.md — "))+facetSubjectMaxRunes {
			t.Fatalf("subject not clamped: %d runes: %q", got, subject)
		}
		if !strings.HasSuffix(subject, "…") {
			t.Fatalf("clamped subject should mark the clip: %q", subject)
		}
	})

	t.Run("a valid status_line of long words clamps too", func(t *testing.T) {
		line := strings.TrimSpace(strings.Repeat("identifier ", 15)) // 15 words, 164 runes
		raw := "## Status Line\n\n" + line + "\n\n## Details\n\nbody\n"
		if err := documentfacets.DefaultContract().Validate(documentfacets.Payload{StatusLine: line, Teaser: "t", Digest: "d", Full: "body"}); err != nil {
			t.Fatalf("status line %q should be valid: %v", line, err)
		}
		subject := strings.SplitN(documentWriteMessage("doc_write", "kb", "x.md", raw), "\n", 2)[0]
		if got := len([]rune(subject)); got > len([]rune("doc_write kb:x.md — "))+facetSubjectMaxRunes || !strings.HasSuffix(subject, "…") {
			t.Fatalf("subject = %q (%d runes), want it clamped with the clip marked", subject, got)
		}
	})

	t.Run("empty status_line stays mechanical, digest still becomes the body", func(t *testing.T) {
		raw := "## Status Line\n\n\n## Digest\n\nsummary\n\n## Details\n\nbody\n"
		msg := documentWriteMessage("doc_write", "kb", "x.md", raw)
		if msg != "doc_write kb:x.md\n\nsummary" {
			t.Fatalf("message = %q, want undecorated subject with digest body", msg)
		}
	})
}

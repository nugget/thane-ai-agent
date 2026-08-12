package documents

import (
	"strings"
	"testing"
)

// TestDocumentWriteMessageBorrowsFacets pins the commit-message
// contract: a faceted document's status_line joins the subject and its
// digest becomes the body, so the root's git log reads as a timeline
// of the document's own verdicts; an unfaceted document keeps the
// plain mechanical subject; and a runaway status_line (possible only
// through non-publish writes) is clamped rather than allowed to
// swallow the subject line.
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

	t.Run("empty status_line stays mechanical", func(t *testing.T) {
		raw := "## Status Line\n\n\n## Digest\n\nsummary\n\n## Details\n\nbody\n"
		msg := documentWriteMessage("doc_write", "kb", "x.md", raw)
		if !strings.HasPrefix(msg, "doc_write kb:x.md\n\n") && msg != "doc_write kb:x.md" {
			if !strings.Contains(msg, "summary") {
				t.Fatalf("message = %q", msg)
			}
		}
		if strings.Contains(strings.SplitN(msg, "\n", 2)[0], "—") {
			t.Fatalf("empty verdict must not decorate the subject: %q", msg)
		}
	})
}

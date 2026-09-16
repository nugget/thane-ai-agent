package documents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestDocReadKeepsRawTemplates pins doc_read as an author surface.
//
// It is the read a whole-document replacement is authored against, and
// the precondition consults it. If it expanded, a model that read a
// document and passed the body back would store "+110d" where the author
// wrote {{delta:2026-12-25}} — the template gone for good, with nothing
// in the result to say it happened. The reader/author split exists for
// exactly this, and until now nothing failed when it was violated.
func TestDocReadKeepsRawTemplates(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	tools := NewTools(store)
	const template = "{{delta:2026-12-25}}"
	body := "# Winter\n\nThe hard freeze lands " + template + ".\n"
	if _, err := store.Write(context.Background(), WriteArgs{Ref: "kb:winter.md", Body: &body}); err != nil {
		t.Fatalf("seed document: %v", err)
	}

	out, err := tools.Read(context.Background(), RefArgs{Ref: "kb:winter.md"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Assert on the decoded body rather than searching the serialized
	// result: the payload carries the prose in more than one field, so a
	// substring search passes on a surviving copy while the field the
	// author actually round-trips has already been expanded.
	var payload struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode read result: %v\n%s", err, out)
	}
	if payload.Body != body {
		t.Fatalf("doc_read did not return the stored body byte-exact:\n got: %q\nwant: %q", payload.Body, body)
	}
	if !strings.Contains(payload.Body, template) {
		t.Fatalf("doc_read expanded a template; the author round-trip is no longer byte-exact: %q", payload.Body)
	}
}

// TestPublishRoundTripsTemplatesByteExact pins the write end: a template
// passed through a faceted publish must come back out of the parse
// unchanged, so a loop republishing every projection cannot launder its
// own templates into rendered text.
func TestPublishRoundTripsTemplatesByteExact(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	tools := NewTools(store)
	const template = "{{delta:2026-12-25}}"

	if _, err := tools.Publish(context.Background(), PublishArgs{
		Ref:        "kb:winter.md",
		Title:      "Winter",
		StatusLine: "Hard freeze " + template,
		Full:       "# Winter\n\nWrap the pipes before " + template + ".\n",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	record, err := store.Read(context.Background(), "kb:winter.md")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Count(record.Body, template) != 2 {
		t.Fatalf("publish did not round-trip both templates byte-exact:\n%s", record.Body)
	}
}

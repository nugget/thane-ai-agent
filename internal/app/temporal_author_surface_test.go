package app

import (
	"context"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// authorTemplate is a temporal template with a date far enough out that
// any expansion produces obviously different text.
const authorTemplate = "{{delta:2026-12-25}}"

// TestLoopOwnOutputInjectionKeepsRawTemplates defends the author-surface
// half of the temporal-template contract, which nothing pinned before.
//
// A loop reads its own maintained document every wake and republishes a
// complete replacement. If this injection ever expanded, the loop would
// see "+110d" where it wrote {{delta:2026-12-25}}, and its next publish
// would store the rendered delta — destroying the template permanently,
// silently, with no error and no way back. That is why reader surfaces
// expand and this one must not.
func TestLoopOwnOutputInjectionKeepsRawTemplates(t *testing.T) {
	store, _ := newLoopOutputDocumentStore(t)
	docTools := documents.NewTools(store)

	body := "# Winter\n\nThe hard freeze lands " + authorTemplate + " and the pipes need wrapping.\n"
	if _, err := store.Write(context.Background(), documents.WriteArgs{
		Ref:  "core:winter.md",
		Body: &body,
	}); err != nil {
		t.Fatalf("seed document: %v", err)
	}

	outputs := []looppkg.OutputSpec{{
		Name: "winter_watch",
		Type: looppkg.OutputTypeMaintainedDocument,
		Ref:  "core:winter.md",
		Mode: looppkg.OutputModeReplace,
	}}
	rendered, err := renderLoopOutputContextWithNow(context.Background(), store, docTools, outputs, time.Now())
	if err != nil {
		t.Fatalf("renderLoopOutputContextWithNow: %v", err)
	}
	if !strings.Contains(rendered, authorTemplate) {
		t.Fatalf("the loop's own document was expanded before it could republish it:\n%s", rendered)
	}
}

// TestStoredDocumentKeepsRawTemplates pins the storage end of the same
// contract: whatever expands, it must never be what lands on disk. A
// rendered delta in storage is indistinguishable from prose an author
// meant, so the damage cannot be detected later, only inherited.
func TestStoredDocumentKeepsRawTemplates(t *testing.T) {
	store, _ := newLoopOutputDocumentStore(t)

	body := "# Winter\n\nThe hard freeze lands " + authorTemplate + ".\n"
	if _, err := store.Write(context.Background(), documents.WriteArgs{
		Ref:  "core:winter.md",
		Body: &body,
	}); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	record, err := store.Read(context.Background(), "core:winter.md")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(record.Body, authorTemplate) {
		t.Fatalf("storage does not round-trip the template byte-exact:\n%s", record.Body)
	}
}

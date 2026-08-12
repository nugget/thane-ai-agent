package documents

import (
	"context"
	"testing"
)

// TestSearchUsesAuthoredFacetsForSnippetAndAdvertisesThem pins the
// #1250 read surface: a faceted document's search summary is its
// authored teaser — or its status_line when no teaser is declared —
// never a derived excerpt of the rendered sections, and the hit lists
// the facets present so the next step is one deliberate doc_read with
// level. An unfaceted document keeps the derived first paragraph and
// advertises nothing.
func TestSearchUsesAuthoredFacetsForSnippetAndAdvertisesThem(t *testing.T) {
	store, _ := newMutationStore(t)
	ctx := context.Background()

	teasered := "## Status Line\n\nverdict line here\n\n## Teaser\n\nThe authored orangutan teaser, written for search snippets.\n\n## Details\n\nlong working memory body\n"
	if _, err := store.Write(ctx, WriteArgs{Ref: "kb:zoo/teasered.md", Title: "Teasered", Body: stringPtr(teasered)}); err != nil {
		t.Fatalf("write teasered: %v", err)
	}
	verdictOnly := "## Status Line\n\norangutan verdict, no teaser declared\n\n## Details\n\nbody\n"
	if _, err := store.Write(ctx, WriteArgs{Ref: "kb:zoo/verdict.md", Title: "VerdictOnly", Body: stringPtr(verdictOnly)}); err != nil {
		t.Fatalf("write verdict: %v", err)
	}
	plain := "# Plain\n\nA plain orangutan document with a first paragraph.\n"
	if _, err := store.Write(ctx, WriteArgs{Ref: "kb:zoo/plain.md", Title: "Plain", Body: stringPtr(plain)}); err != nil {
		t.Fatalf("write plain: %v", err)
	}

	results, err := store.Search(ctx, SearchQuery{Query: "orangutan"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	byRef := make(map[string]DocumentSummary, len(results))
	for _, doc := range results {
		byRef[doc.Ref] = doc
	}

	teaseredHit, ok := byRef["kb:zoo/teasered.md"]
	if !ok {
		t.Fatalf("teasered document missing from results: %v", byRef)
	}
	if teaseredHit.Summary != "The authored orangutan teaser, written for search snippets." {
		t.Errorf("teasered summary = %q, want the authored teaser", teaseredHit.Summary)
	}
	if len(teaseredHit.Facets) != 2 || teaseredHit.Facets[0] != "status_line" || teaseredHit.Facets[1] != "teaser" {
		t.Errorf("teasered facets = %v, want [status_line teaser]", teaseredHit.Facets)
	}

	verdictHit, ok := byRef["kb:zoo/verdict.md"]
	if !ok {
		t.Fatalf("verdict document missing from results: %v", byRef)
	}
	if verdictHit.Summary != "orangutan verdict, no teaser declared" {
		t.Errorf("verdict summary = %q, want the status_line fallback", verdictHit.Summary)
	}

	plainHit, ok := byRef["kb:zoo/plain.md"]
	if !ok {
		t.Fatalf("plain document missing from results: %v", byRef)
	}
	if plainHit.Summary != "A plain orangutan document with a first paragraph." {
		t.Errorf("plain summary = %q, want the derived first paragraph", plainHit.Summary)
	}
	if plainHit.Facets != nil {
		t.Errorf("plain document advertises facets: %v", plainHit.Facets)
	}
}

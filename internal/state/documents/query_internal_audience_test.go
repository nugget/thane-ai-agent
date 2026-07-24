package documents

import (
	"context"
	"testing"
)

// seedAudienceSearchDocs writes one published and one internal-audience
// document that both match the query text "climate".
func seedAudienceSearchDocs(t *testing.T) *Store {
	t.Helper()
	store, _ := newMutationStore(t)
	ctx := context.Background()

	if _, err := store.Write(ctx, WriteArgs{
		Ref:   "kb:climate/status.md",
		Title: "Climate Status",
		Body:  stringPtr("# Climate Status\n\nPublished state."),
	}); err != nil {
		t.Fatalf("Write published: %v", err)
	}
	if _, err := store.Write(ctx, WriteArgs{
		Ref:         "kb:climate/notes.md",
		Title:       "Climate Working Notes",
		Frontmatter: map[string][]string{"audience": {"internal"}},
		Body:        stringPtr("# Climate Working Notes\n\nProcess narration."),
	}); err != nil {
		t.Fatalf("Write internal: %v", err)
	}
	return store
}

func searchRefs(t *testing.T, store *Store, q SearchQuery) map[string]bool {
	t.Helper()
	results, err := store.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	refs := make(map[string]bool, len(results))
	for _, doc := range results {
		refs[doc.Ref] = true
	}
	return refs
}

func TestSearchExcludesInternalAudienceByDefault(t *testing.T) {
	t.Parallel()

	store := seedAudienceSearchDocs(t)
	refs := searchRefs(t, store, SearchQuery{Root: "kb", Query: "climate", Limit: 10})
	if !refs["kb:climate/status.md"] {
		t.Fatalf("published doc missing from default search: %v", refs)
	}
	if refs["kb:climate/notes.md"] {
		t.Fatalf("internal doc leaked into default search: %v", refs)
	}
}

func TestSearchIncludeInternalFlagIncludesInternal(t *testing.T) {
	t.Parallel()

	store := seedAudienceSearchDocs(t)
	refs := searchRefs(t, store, SearchQuery{Root: "kb", Query: "climate", Limit: 10, IncludeInternal: true})
	if !refs["kb:climate/status.md"] || !refs["kb:climate/notes.md"] {
		t.Fatalf("include_internal search missing docs: %v", refs)
	}
}

func TestSearchExplicitAudienceFilterBypassesExclusion(t *testing.T) {
	t.Parallel()

	store := seedAudienceSearchDocs(t)

	// Filtering on the audience value is a deliberate selection: the
	// default exclusion must not silently empty the result.
	refs := searchRefs(t, store, SearchQuery{
		Root:        "kb",
		Frontmatter: map[string][]string{"audience": {"internal"}},
		Limit:       10,
	})
	if !refs["kb:climate/notes.md"] {
		t.Fatalf("explicit audience-value filter did not surface internal doc: %v", refs)
	}
	if refs["kb:climate/status.md"] {
		t.Fatalf("audience filter matched a published doc without the key: %v", refs)
	}

	// Requiring the audience key to be present behaves the same way.
	refs = searchRefs(t, store, SearchQuery{
		Root:            "kb",
		FrontmatterKeys: []string{"audience"},
		Limit:           10,
	})
	if !refs["kb:climate/notes.md"] {
		t.Fatalf("audience frontmatter_keys filter did not surface internal doc: %v", refs)
	}
}

func TestSearchInternalReadByRefUnaffected(t *testing.T) {
	t.Parallel()

	store := seedAudienceSearchDocs(t)
	doc, err := store.Read(context.Background(), "kb:climate/notes.md")
	if err != nil {
		t.Fatalf("Read internal by ref: %v", err)
	}
	if doc.Title != "Climate Working Notes" {
		t.Fatalf("Read internal doc = %#v, want working notes", doc)
	}
}

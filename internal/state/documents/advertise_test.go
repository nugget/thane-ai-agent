package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// newAdvertiseStore indexes two roots holding a faceted curator-style
// document with provenance frontmatter, two plain documents, and one
// internal-audience document that must never be offered.
func newAdvertiseStore(t *testing.T) (*Store, map[string]string) {
	t.Helper()

	rootDir := t.TempDir()
	alphaDir := filepath.Join(rootDir, "alpha")
	betaDir := filepath.Join(rootDir, "beta")

	writeFile(t, filepath.Join(alphaDir, "b-doc.md"), "---\ntags: [ops]\n---\n\n# Bravo\n\nSecond alphabetical document.\n")
	writeFile(t, filepath.Join(alphaDir, "a-doc.md"), "# Alpha\n\nFirst alphabetical document.\n")
	writeFile(t, filepath.Join(betaDir, "dossier.md"), `---
title: Utah Trip Dossier
tags: [travel, utah]
audience: published
managed_by: loop_output_trip
loop_definition_name: trip-curator
loop_intent: "Keep the Utah trip dossier current."
---

## Status Line

trip is 20 days out

## Teaser

Why the dossier matters right now.

## Details

The full dossier body.
`)
	writeFile(t, filepath.Join(betaDir, "notes.md"), "---\naudience: internal\n---\n\n# Working Notes\n\nCurator process narration.\n")

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	roots := map[string]string{"alpha": alphaDir, "beta": betaDir}
	store, err := NewStore(db, roots, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return store, roots
}

func advertisedRefs(docs []AdvertisableDocument) []string {
	refs := make([]string, 0, len(docs))
	for _, doc := range docs {
		refs = append(refs, doc.Ref)
	}
	return refs
}

// TestAdvertisableDocumentsProjectsIndexFields pins the enumeration
// contract the #1431 advertiser consumes: internal-audience documents
// are excluded, order is deterministic (root, rel_path), and every
// per-row field an offer needs — identity, summary, facet levels with
// byte costs, tags, freshness, provenance — arrives from the index.
func TestAdvertisableDocumentsProjectsIndexFields(t *testing.T) {
	t.Parallel()

	store, roots := newAdvertiseStore(t)
	ctx := context.Background()

	docs, err := store.AdvertisableDocuments(ctx)
	if err != nil {
		t.Fatalf("AdvertisableDocuments: %v", err)
	}
	wantRefs := []string{"alpha:a-doc.md", "alpha:b-doc.md", "beta:dossier.md"}
	if got := advertisedRefs(docs); len(got) != len(wantRefs) || got[0] != wantRefs[0] || got[1] != wantRefs[1] || got[2] != wantRefs[2] {
		t.Fatalf("refs = %v, want %v (internal excluded, (root, rel_path) order)", got, wantRefs)
	}

	dossier := docs[2]
	if dossier.Root != "beta" || dossier.Path != "dossier.md" {
		t.Errorf("dossier identity = %q %q, want beta dossier.md", dossier.Root, dossier.Path)
	}
	if dossier.Title != "Utah Trip Dossier" {
		t.Errorf("Title = %q, want the authored title", dossier.Title)
	}
	if dossier.Summary != "Why the dossier matters right now." {
		t.Errorf("Summary = %q, want the authored teaser", dossier.Summary)
	}
	if len(dossier.Facets) != 2 || dossier.Facets[0] != "status_line" || dossier.Facets[1] != "teaser" {
		t.Errorf("Facets = %v, want [status_line teaser]", dossier.Facets)
	}
	wantBytes := map[string]int{
		"status_line": len("trip is 20 days out"),
		"teaser":      len("Why the dossier matters right now."),
		"full":        len("The full dossier body."),
	}
	if len(dossier.FacetBytes) != len(wantBytes) {
		t.Fatalf("FacetBytes = %v, want %v", dossier.FacetBytes, wantBytes)
	}
	for key, want := range wantBytes {
		if got := dossier.FacetBytes[key]; got != want {
			t.Errorf("FacetBytes[%q] = %d, want %d", key, got, want)
		}
	}
	if len(dossier.Tags) != 2 || dossier.Tags[0] != "travel" || dossier.Tags[1] != "utah" {
		t.Errorf("Tags = %v, want [travel utah]", dossier.Tags)
	}
	info, err := os.Stat(filepath.Join(roots["beta"], "dossier.md"))
	if err != nil {
		t.Fatalf("Stat dossier: %v", err)
	}
	if !dossier.ModifiedAt.Equal(info.ModTime().UTC()) {
		t.Errorf("ModifiedAt = %v, want file mtime %v", dossier.ModifiedAt, info.ModTime().UTC())
	}
	if dossier.LoopDefinitionName != "trip-curator" {
		t.Errorf("LoopDefinitionName = %q, want trip-curator", dossier.LoopDefinitionName)
	}
	if dossier.ManagedBy != "loop_output_trip" {
		t.Errorf("ManagedBy = %q, want loop_output_trip", dossier.ManagedBy)
	}
	if dossier.LoopIntent != "Keep the Utah trip dossier current." {
		t.Errorf("LoopIntent = %q, want the authored intent", dossier.LoopIntent)
	}
	for _, doc := range docs[:2] {
		if len(doc.FacetBytes) != 1 || doc.FacetBytes["full"] == 0 {
			t.Errorf("%s FacetBytes = %v, want a lone non-zero full entry", doc.Ref, doc.FacetBytes)
		}
	}
}

// TestAdvertisableDocumentsExcludesInternalInSQL pins that the privacy
// gate lives in the SELECT itself, not only in the post-scan frontmatter
// check: a row whose audience COLUMN reads internal — with frontmatter
// that would pass the in-Go check — must never come back. Case and
// whitespace variants are the LOWER(TRIM(...)) part of the clause.
func TestAdvertisableDocumentsExcludesInternalInSQL(t *testing.T) {
	t.Parallel()

	store, _ := newAdvertiseStore(t)
	ctx := context.Background()

	if _, err := store.db.ExecContext(ctx,
		`UPDATE indexed_documents SET audience = '  Internal ' WHERE root = 'alpha' AND rel_path = 'a-doc.md'`,
	); err != nil {
		t.Fatalf("mark row internal in column only: %v", err)
	}
	docs, err := store.AdvertisableDocuments(ctx)
	if err != nil {
		t.Fatalf("AdvertisableDocuments: %v", err)
	}
	for _, doc := range docs {
		if doc.Ref == "alpha:a-doc.md" {
			t.Fatalf("row with internal audience column leaked through the SQL gate: %v", advertisedRefs(docs))
		}
	}
}

// TestAdvertisableDocumentsDoesNotRefresh proves the no-Refresh design
// behaviorally rather than structurally: the root directory is deleted
// and the refresh throttle disabled, so ANY code path that calls
// Refresh fails loudly ("document root does not exist"). Enumeration
// must still serve the warm index rows — an assembly-time caller reads
// what the background refresher last wrote and never joins the
// refreshMu queue — while a Refresh-first query on the same store
// state errors, which is the positive control that this construction
// really does detect a Refresh call.
func TestAdvertisableDocumentsDoesNotRefresh(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	writeFile(t, filepath.Join(kbDir, "doc.md"), "# Doc\n\nIndexed once.\n")

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}

	store.refreshInterval = 0
	if err := os.RemoveAll(kbDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	docs, err := store.AdvertisableDocuments(ctx)
	if err != nil {
		t.Fatalf("AdvertisableDocuments refreshed (or failed) instead of reading the index: %v", err)
	}
	if len(docs) != 1 || docs[0].Ref != "kb:doc.md" {
		t.Fatalf("docs = %v, want the one warm index row", advertisedRefs(docs))
	}

	if _, err := store.Search(ctx, SearchQuery{Root: "kb"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Search err = %v; the positive control expects the Refresh-first path to fail on the deleted root", err)
	}
}

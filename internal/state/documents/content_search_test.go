package documents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func newContentSearchStore(t *testing.T, policies map[string]RootPolicy) (*Store, map[string]string) {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	roots := make(map[string]string, len(policies))
	for root := range policies {
		roots[root] = t.TempDir()
	}
	store, err := NewStoreWithOptions(db, roots, nil, StoreOptions{RootPolicies: policies})
	if err != nil {
		t.Fatal(err)
	}
	store.refreshInterval = 0
	return store, roots
}

func TestSearchContentBodyOptInAndRootCoverage(t *testing.T) {
	store, roots := newContentSearchStore(t, map[string]RootPolicy{
		"body":     {Indexing: true, Context: RootContextPolicy{SearchBody: true}},
		"metadata": {Indexing: true},
		"named":    {Indexing: true, Context: RootContextPolicy{SearchBody: true, Search: RootSearchOnRequest}},
		"never":    {Indexing: true, Context: RootContextPolicy{SearchBody: true, Search: RootSearchNever}},
		"disabled": {Indexing: false, Context: RootContextPolicy{SearchBody: true}},
	})
	for _, dir := range roots {
		writeFile(t, filepath.Join(dir, "notes.md"), "# Inventory\n\nAuthored opening summary.\n\nThe buried cobalt retrieval token is here.\n")
	}
	result, err := NewTools(store).SearchContent(context.Background(), SearchQuery{Query: "cobalt retrieval"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Document.Root != "body" || result.Hits[0].Document.Summary != "Authored opening summary." || !strings.Contains(result.Hits[0].Excerpt, "cobalt retrieval") {
		t.Fatalf("body opt-in or excerpt/summary separation failed: %+v", result)
	}
	coverage := make(map[string]ContentSearchRoot)
	for _, root := range result.Roots {
		coverage[root.Root] = root
	}
	if len(coverage) != 5 || !coverage["body"].Body || coverage["metadata"].Body || coverage["named"].Status != "on_request" || coverage["never"].Status != "disabled" || coverage["disabled"].Status != "disabled" {
		t.Fatalf("incorrect root coverage: %+v", coverage)
	}
	for _, tc := range []struct {
		root string
		want int
	}{{"named", 1}, {"never", 0}, {"metadata", 0}, {"disabled", 0}} {
		result, err := store.SearchContent(context.Background(), SearchQuery{Root: tc.root, Query: "cobalt retrieval"})
		if err != nil || len(result.Hits) != tc.want {
			t.Errorf("root %s: hits=%+v error=%v, want %d", tc.root, result, err, tc.want)
		}
	}
	metadata, err := store.Search(context.Background(), SearchQuery{Query: "cobalt retrieval"})
	if err != nil || len(metadata) != 0 {
		t.Fatalf("metadata search widened into bodies: %+v, %v", metadata, err)
	}
}

func TestSearchContentAudienceAndFiltersBeforeLimit(t *testing.T) {
	store, roots := newContentSearchStore(t, map[string]RootPolicy{"kb": {Indexing: true, Context: RootContextPolicy{SearchBody: true}}})
	for i := range 6 {
		writeFile(t, filepath.Join(roots["kb"], fmt.Sprintf("private-%d.md", i)), "---\naudience: [agent, private]\n---\n# Restricted\n\nOpening.\n\nquartz bodymatch\n")
	}
	writeFile(t, filepath.Join(roots["kb"], "selected", "one.md"), "---\ntags: [operations]\narea: rack\n---\n# Selected\n\nOpening.\n\nquartz bodymatch\n")
	writeFile(t, filepath.Join(roots["kb"], "other.md"), "---\ntags: [other]\narea: office\n---\n# Other\n\nOpening.\n\nquartz bodymatch\n")
	q := SearchQuery{Query: "quartz bodymatch", Limit: 1, Tags: []string{"OPERATIONS"}, Frontmatter: map[string][]string{"AREA": {"RACK"}}, FrontmatterKeys: []string{"area"}, PathPrefix: "selected"}
	result, err := store.SearchContent(context.Background(), q)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Document.Path != "selected/one.md" || result.Truncated {
		t.Fatalf("filters were applied after limit: %+v, %v", result, err)
	}
	result, err = store.SearchContent(context.Background(), SearchQuery{Query: "quartz bodymatch", Limit: 1})
	if err != nil || len(result.Hits) != 1 || !result.Truncated || isRestrictedAudienceDocument(result.Hits[0].Document.Frontmatter) {
		t.Fatalf("restricted documents affected public result limit: %+v, %v", result, err)
	}
	result, err = store.SearchContent(context.Background(), SearchQuery{Query: "quartz bodymatch", Frontmatter: map[string][]string{"audience": {"private"}}, Limit: 10})
	if err != nil || len(result.Hits) != 6 {
		t.Fatalf("explicit restricted selection lost: %+v, %v", result, err)
	}
}

func TestSearchContentFacetProjectionAndWriter(t *testing.T) {
	store, roots := newContentSearchStore(t, map[string]RootPolicy{"kb": {Indexing: true, Authoring: AuthoringManaged, Context: RootContextPolicy{SearchBody: true}}})
	teaser := "Authored reason to open the source."
	digest := "Digestonlymarker belongs to the condensed view."
	if _, err := NewTools(store).Publish(context.Background(), PublishArgs{Ref: "kb:plan.md", Title: "Plan", StatusLine: "Ready.", Teaser: &teaser, Digest: &digest, Full: "The verified xenon finding is in the full source."}); err != nil {
		t.Fatal(err)
	}
	result, err := store.SearchContent(context.Background(), SearchQuery{Query: "xenon finding"})
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("full projection missing: %+v %v", result, err)
	}
	hit := result.Hits[0]
	if hit.Document.Summary != teaser || len(hit.Document.Facets) != 3 || hit.WriteTool != DocumentWriteToolName || strings.Contains(hit.Excerpt, "## Details") {
		t.Fatalf("lost authored facet/writer contract: %+v", hit)
	}
	writeFile(t, filepath.Join(roots["kb"], "invalid.md"), "---\nfacets: [digest]\nmanaged_by: doc_write\nthane_document: faceted/v1\n---\n## Details\n\nMalformedprivatepayload\n")
	for _, query := range []string{"Digestonlymarker", "Malformedprivatepayload"} {
		result, err := store.SearchContent(context.Background(), SearchQuery{Query: query})
		if err != nil || len(result.Hits) != 0 {
			t.Errorf("private codec leaked for %q: %+v %v", query, result, err)
		}
	}
}

func TestSearchContentRefreshRebuildAndDeletion(t *testing.T) {
	policy := RootPolicy{Indexing: true, Context: RootContextPolicy{SearchBody: true}}
	store, roots := newContentSearchStore(t, map[string]RootPolicy{"kb": policy})
	path := filepath.Join(roots["kb"], "record.md")
	writeFile(t, path, "# Record\n\nOpening.\n\nLegacybodyneedle\n")
	assertHits := func(query string, count int) {
		t.Helper()
		result, err := store.SearchContent(context.Background(), SearchQuery{Query: query})
		if err != nil || len(result.Hits) != count {
			t.Fatalf("query %q: result=%+v error=%v, want %d", query, result, err, count)
		}
	}
	assertHits("Legacybodyneedle", 1)
	// Reconstruct the pre-upgrade metadata shape without changing source
	// bytes or timestamps. NewStore must migrate and backfill it on refresh.
	if _, err := store.db.Exec(`DELETE FROM indexed_document_content_fts; ALTER TABLE indexed_documents DROP COLUMN content_body_indexed`); err != nil {
		t.Fatal(err)
	}
	var err error
	store, err = NewStoreWithOptions(store.db, roots, nil, StoreOptions{RootPolicies: map[string]RootPolicy{"kb": policy}})
	if err != nil {
		t.Fatal(err)
	}
	store.refreshInterval = 0
	assertHits("Legacybodyneedle", 1)
	if _, err := store.db.Exec(`DELETE FROM indexed_document_content_fts`); err != nil {
		t.Fatal(err)
	}
	assertHits("Legacybodyneedle", 1)
	policy.Context.SearchBody = false
	store.rootPolicies["kb"] = policy
	assertHits("Legacybodyneedle", 0)
	var retained string
	if err := store.db.QueryRow(`SELECT body FROM indexed_document_content_fts`).Scan(&retained); err != nil || retained != "" {
		t.Fatalf("body retained after opting out: %q %v", retained, err)
	}
	policy.Context.SearchBody = true
	store.rootPolicies["kb"] = policy
	assertHits("Legacybodyneedle", 1)
	writeFile(t, path, "# Record\n\nOpening.\n\nReplacementbodyneedle with changed bytes.\n")
	assertHits("Legacybodyneedle", 0)
	assertHits("Replacementbodyneedle", 1)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	assertHits("Replacementbodyneedle", 0)
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM indexed_document_content_fts`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("deleted source retained content rows: %d %v", rows, err)
	}
}

func TestSearchContentRechecksSignatures(t *testing.T) {
	policy := RootPolicy{Indexing: true, Context: RootContextPolicy{SearchBody: true}, Git: RootGitPolicy{Enabled: true, VerifySignatures: VerificationRequired}}
	store, roots := newContentSearchStore(t, map[string]RootPolicy{"kb": policy})
	trusted := map[string]SignatureVerification{"signed.md": {Status: SignatureTrusted}}
	store.rootVerifiers = map[string]RootVerifier{"kb": fakeRootVerifier{files: trusted}}
	writeFile(t, filepath.Join(roots["kb"], "signed.md"), "# Signed\n\nOpening.\n\nTrustedbodyneedle\n")
	if result, err := store.SearchContent(context.Background(), SearchQuery{Query: "Trustedbodyneedle"}); err != nil || len(result.Hits) != 1 {
		t.Fatalf("trusted source missing: %+v %v", result, err)
	}
	// Keep the refresh cache warm to ensure search itself verifies the hit.
	store.refreshInterval = time.Hour
	trusted["signed.md"] = SignatureVerification{Status: SignatureFailed}
	if _, err := store.SearchContent(context.Background(), SearchQuery{Query: "Trustedbodyneedle"}); err == nil {
		t.Fatal("revoked signature leaked a cached content excerpt")
	}
}

func TestSearchContentLiteralQueriesAndBoundedExcerpt(t *testing.T) {
	store, roots := newContentSearchStore(t, map[string]RootPolicy{"kb": {Indexing: true, Context: RootContextPolicy{SearchBody: true}}})
	writeFile(t, filepath.Join(roots["kb"], "terms.md"), "# Match\n\nOpening.\n\nalpha intervening beta\n\n"+strings.Repeat("界", 4000)+"\n")
	for _, tc := range []struct {
		query string
		want  int
		match string
	}{{"alpha beta", 1, "terms"}, {"alpha intervening", 1, "phrase"}, {`alpha OR nonexistent`, 0, ""}, {`\" OR *`, 0, ""}, {strings.Repeat("界", 4000), 0, ""}} {
		result, err := store.SearchContent(context.Background(), SearchQuery{Query: tc.query})
		if len([]rune(tc.query)) > 1024 {
			if err == nil {
				t.Error("oversized query accepted")
			}
			continue
		}
		if err != nil || len(result.Hits) != tc.want {
			t.Errorf("query %q: %+v %v", tc.query, result, err)
			continue
		}
		if tc.want > 0 && (result.Hits[0].MatchType != tc.match || !utf8.ValidString(result.Hits[0].Excerpt) || len(result.Hits[0].Excerpt) > 2048) {
			t.Errorf("invalid match metadata/excerpt: %+v", result.Hits[0])
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.SearchContent(ctx, SearchQuery{Query: "alpha"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query error = %v", err)
	}
}

func TestSearchContentOptOutPurgesBodyBeforeUnavailableRootRead(t *testing.T) {
	policy := RootPolicy{Indexing: true, Context: RootContextPolicy{SearchBody: true}}
	store, roots := newContentSearchStore(t, map[string]RootPolicy{"kb": policy})
	writeFile(t, filepath.Join(roots["kb"], "record.md"), "# Source\n\nOpening.\n\nRetainedbodyneedle\n")
	if _, err := store.SearchContent(context.Background(), SearchQuery{Query: "Retainedbodyneedle"}); err != nil {
		t.Fatal(err)
	}
	policy.Context.SearchBody = false
	store.rootPolicies["kb"] = policy
	if err := os.Rename(roots["kb"], roots["kb"]+"-moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SearchContent(context.Background(), SearchQuery{Query: "Retainedbodyneedle"}); err == nil {
		t.Fatal("missing root was reported as a successful empty search")
	}
	var body string
	if err := store.db.QueryRow(`SELECT body FROM indexed_document_content_fts`).Scan(&body); err != nil || body != "" {
		t.Fatalf("opted-out body retained when source unavailable: %q %v", body, err)
	}
}

func TestSearchContentIndexFailureIsNotEmptySuccess(t *testing.T) {
	store, roots := newContentSearchStore(t, map[string]RootPolicy{"kb": {Indexing: true, Context: RootContextPolicy{SearchBody: true}}})
	writeFile(t, filepath.Join(roots["kb"], "record.md"), "# Source\n\nOpening.\n\nNeedle\n")
	if _, err := store.db.Exec(`CREATE TRIGGER reject_content_metadata BEFORE INSERT ON indexed_documents BEGIN SELECT RAISE(ABORT, 'forced index failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SearchContent(context.Background(), SearchQuery{Query: "Needle"}); err == nil || !strings.Contains(err.Error(), "forced index failure") {
		t.Fatalf("index failure hidden as search absence: %v", err)
	}
}

func TestSearchContentExcerptRetainsMatchAfterLongToken(t *testing.T) {
	store, roots := newContentSearchStore(t, map[string]RootPolicy{"kb": {Indexing: true, Context: RootContextPolicy{SearchBody: true}}})
	writeFile(t, filepath.Join(roots["kb"], "record.md"), "# Source\n\nOpening.\n\n"+strings.Repeat("界", 4000)+" actualneedle follows the long token.\n")
	result, err := store.SearchContent(context.Background(), SearchQuery{Query: "actualneedle"})
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("search: %+v %v", result, err)
	}
	if excerpt := result.Hits[0].Excerpt; !strings.Contains(excerpt, "actualneedle") || !utf8.ValidString(excerpt) || len(excerpt) > 2048 {
		t.Fatalf("excerpt lost match or exceeded bounds: %q", excerpt)
	}
}

package documents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// newContextPolicyStore builds a two-root store where the second root
// carries a declared search policy.
func newContextPolicyStore(t *testing.T, search string) *Store {
	t.Helper()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	vaultDir := filepath.Join(rootDir, "vault")
	for _, dir := range []string{kbDir, vaultDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	write(kbDir, "climate.md", "---\ntitle: Climate\n---\n\nClimate notes.")
	write(vaultDir, "climate.md", "---\ntitle: Climate Vault\n---\n\nClimate vault notes.")

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStoreWithOptions(db, map[string]string{"kb": kbDir, "vault": vaultDir}, nil, StoreOptions{
		RootPolicies: map[string]RootPolicy{
			"vault": {Indexing: true, Authoring: AuthoringManaged, Context: RootContextPolicy{Search: search}},
		},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return store
}

func searchRootSet(t *testing.T, store *Store, q SearchQuery) map[string]bool {
	t.Helper()
	results, err := store.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	roots := map[string]bool{}
	for _, doc := range results {
		roots[doc.Root] = true
	}
	return roots
}

func TestSearchOnRequestRootExcludedFromUnscopedSearch(t *testing.T) {
	t.Parallel()

	store := newContextPolicyStore(t, RootSearchOnRequest)
	roots := searchRootSet(t, store, SearchQuery{Query: "climate", Limit: 10})
	if !roots["kb"] {
		t.Fatalf("default-visibility root missing from unscoped search: %v", roots)
	}
	if roots["vault"] {
		t.Fatalf("on_request root leaked into unscoped search: %v", roots)
	}
}

func TestSearchOnRequestRootReachableWhenNamed(t *testing.T) {
	t.Parallel()

	store := newContextPolicyStore(t, RootSearchOnRequest)
	roots := searchRootSet(t, store, SearchQuery{Root: "vault", Query: "climate", Limit: 10})
	if !roots["vault"] {
		t.Fatalf("naming an on_request root should search it: %v", roots)
	}
}

func TestSearchNeverRootExcludedEvenWhenNamed(t *testing.T) {
	t.Parallel()

	store := newContextPolicyStore(t, RootSearchNever)
	if roots := searchRootSet(t, store, SearchQuery{Root: "vault", Query: "climate", Limit: 10}); len(roots) != 0 {
		t.Fatalf("never-searchable root returned results even when named: %v", roots)
	}
}

func TestSearchUndeclaredPolicyKeepsFullVisibility(t *testing.T) {
	t.Parallel()

	store := newContextPolicyStore(t, "")
	roots := searchRootSet(t, store, SearchQuery{Query: "climate", Limit: 10})
	if !roots["kb"] || !roots["vault"] {
		t.Fatalf("an undeclared policy must not change search visibility: %v", roots)
	}
}

func TestRootPolicySummaryReportsContext(t *testing.T) {
	t.Parallel()

	store := newContextPolicyStore(t, RootSearchOnRequest)
	summary := store.rootPolicySummary("vault")
	if summary.Context.Search != RootSearchOnRequest {
		t.Fatalf("summary search = %q, want on_request", summary.Context.Search)
	}
	// Defaults are emitted rather than omitted so the model can tell
	// "will not inject" from "policy unknown".
	if summary.Context.Inject != RootInjectNone {
		t.Fatalf("summary inject = %q, want none", summary.Context.Inject)
	}
}

func TestSearchExcludedRootsDrivesSQLNotPostFilter(t *testing.T) {
	t.Parallel()

	store := newContextPolicyStore(t, RootSearchOnRequest)
	// An unscoped search excludes the on_request root before the query
	// runs, so its rows are never fetched, decoded, or scored.
	if got := store.searchExcludedRoots(""); len(got) != 1 || got[0] != "vault" {
		t.Fatalf("searchExcludedRoots(\"\") = %v, want [vault]", got)
	}
	// Naming it lifts the exclusion entirely.
	if got := store.searchExcludedRoots("vault"); len(got) != 0 {
		t.Fatalf("searchExcludedRoots(\"vault\") = %v, want empty", got)
	}
	// A never-searchable root stays excluded even when named.
	never := newContextPolicyStore(t, RootSearchNever)
	if got := never.searchExcludedRoots("vault"); len(got) != 1 || got[0] != "vault" {
		t.Fatalf("never-searchable root should stay excluded when named, got %v", got)
	}
}

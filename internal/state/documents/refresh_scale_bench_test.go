package documents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// buildCorpus writes a document corpus shaped like the production one:
// thirteen roots holding 359 documents between them.
func buildCorpus(tb testing.TB, roots, docsTotal int) map[string]string {
	tb.Helper()

	base := tb.TempDir()
	out := make(map[string]string, roots)
	perRoot := docsTotal / roots
	body := "---\ntitle: Doc\ntags: [a, b]\n---\n\n# Heading\n\n" +
		"Body text that is long enough to be worth parsing and counting words over.\n\n" +
		"## Section\n\nMore prose, several sentences of it, so the word count is not trivial.\n"
	for r := range roots {
		name := fmt.Sprintf("root%02d", r)
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatalf("mkdir: %v", err)
		}
		for d := range perRoot {
			path := filepath.Join(dir, fmt.Sprintf("doc-%03d.md", d))
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				tb.Fatalf("write: %v", err)
			}
		}
		out[name] = dir
	}
	return out
}

// BenchmarkRefreshAtProductionScale measures the cost the operations
// panel pays on every metacognitive iteration.
//
// The panel calls Activity once per revision-backed root, and every
// Activity call opens with Refresh. Refresh walks and re-indexes *every*
// root, not the one being asked about, and its 5-second throttle is
// always expired by the time a loop wakes — so a loop that assembles
// context pays this in full, per iteration.
//
// Production shape at the time of writing: 359 indexed documents across
// thirteen roots, of which about sixteen fall inside the panel's
// twenty-four hour window.
func BenchmarkRefreshAtProductionScale(b *testing.B) {
	roots := buildCorpus(b, 13, 359)

	db, err := database.OpenMemory()
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })

	store, err := NewStore(db, roots, nil)
	if err != nil {
		b.Fatalf("NewStore: %v", err)
	}
	// Defeat the throttle: a wake is always further apart than it.
	store.refreshInterval = 0

	ctx := context.Background()
	if err := store.Refresh(ctx); err != nil {
		b.Fatalf("warm Refresh: %v", err)
	}

	b.ResetTimer()
	for b.Loop() {
		if err := store.Refresh(ctx); err != nil {
			b.Fatalf("Refresh: %v", err)
		}
	}
}

// TestRefreshAtProductionScaleFitsItsSlice pins the panel's opening cost
// against the budget it actually runs in.
//
// A context provider gets perProviderContextBudget — 500ms at the time of
// writing — and Refresh is what it spends before doing any of its own
// work. If the walk alone does not fit, the panel cannot complete however
// cheap the rest of it is, and the loop that depends on it wakes with no
// operations panel and no signal saying why.
func TestRefreshAtProductionScaleFitsItsSlice(t *testing.T) {
	roots := buildCorpus(t, 13, 359)

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewStore(db, roots, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.refreshInterval = 0
	ctx := context.Background()

	cold := time.Now()
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("cold Refresh: %v", err)
	}
	coldElapsed := time.Since(cold)

	warm := time.Now()
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("warm Refresh: %v", err)
	}
	warmElapsed := time.Since(warm)

	t.Logf("refresh at 359 docs / 13 roots: cold %s, warm %s",
		coldElapsed.Round(time.Millisecond), warmElapsed.Round(time.Millisecond))

	// The warm case is the one a loop actually pays: nothing has changed
	// since the last wake for the overwhelming majority of documents.
	const providerSlice = 500 * time.Millisecond
	if warmElapsed > providerSlice/2 {
		t.Errorf("a warm refresh takes %s of a %s provider slice, leaving too little for the panel's own work",
			warmElapsed.Round(time.Millisecond), providerSlice)
	}
}

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

// TestRefreshScalesLinearlyWithCorpusSize pins the property that
// actually matters as the corpus grows.
//
// An earlier version of this test asserted a wall-clock ceiling — a warm
// refresh inside half a provider slice — and failed in CI at 358ms where
// the same code took 13ms locally. That number measured the machine and
// the race detector, not the code, which is the mistake the assertion
// existed to catch elsewhere.
//
// The portable invariant is the shape of the curve. Quadruple the corpus
// and a linear refresh takes about four times as long; anything
// quadratic takes sixteen. The bound sits between them, so hardware and
// instrumentation overhead cancel in the ratio and only a real change in
// complexity trips it.
func TestRefreshScalesLinearlyWithCorpusSize(t *testing.T) {
	warmRefresh := func(t *testing.T, roots, docs int) time.Duration {
		t.Helper()

		db, err := database.OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		store, err := NewStore(db, buildCorpus(t, roots, docs), nil)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		// A wake is always further apart than the throttle, so a loop
		// pays this in full on every iteration.
		store.refreshInterval = 0

		ctx := context.Background()
		if err := store.Refresh(ctx); err != nil {
			t.Fatalf("cold Refresh: %v", err)
		}
		start := time.Now()
		if err := store.Refresh(ctx); err != nil {
			t.Fatalf("warm Refresh: %v", err)
		}
		return time.Since(start)
	}

	const (
		baseRoots = 13
		baseDocs  = 359 // production shape at the time of writing
		factor    = 4
	)

	base := warmRefresh(t, baseRoots, baseDocs)
	quad := warmRefresh(t, baseRoots, baseDocs*factor)

	t.Logf("warm refresh: %s at %d docs, %s at %d docs",
		base.Round(time.Millisecond), baseDocs,
		quad.Round(time.Millisecond), baseDocs*factor)

	if base <= 0 {
		t.Skip("base refresh too fast to time reliably on this machine")
	}
	// Linear is 4x, quadratic is 16x. Eight leaves room for noise and
	// still fails long before the curve bends.
	const maxRatio = 8.0
	if ratio := float64(quad) / float64(base); ratio > maxRatio {
		t.Errorf("refresh took %.1fx longer for %dx the corpus (%s to %s); linear would be %dx, so the cost is bending upward with corpus size",
			ratio, factor, base.Round(time.Millisecond), quad.Round(time.Millisecond), factor)
	}
}

package introspection

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/phasetrace"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// stubReviser makes a root revision-backed without standing up a git
// repository. The flag sweep reaches history through it and finds none,
// which is all this test needs: the question is where time is charged,
// not what the sweep reports.
type stubReviser struct{}

func (stubReviser) Snapshot(context.Context, string) (documents.RevisionContent, error) {
	return documents.RevisionContent{}, nil
}

func (stubReviser) Resolve(context.Context, string, string) (documents.RevisionRef, error) {
	return documents.RevisionRef{}, nil
}

func (stubReviser) History(context.Context, string, documents.RevisionQuery) (documents.RevisionListing, error) {
	return documents.RevisionListing{}, nil
}

func (stubReviser) Diff(context.Context, string, string, string, string) (documents.RevisionDiff, error) {
	return documents.RevisionDiff{}, nil
}

func (stubReviser) Content(context.Context, string, string) (documents.RevisionContent, error) {
	return documents.RevisionContent{}, nil
}

// TestDocFlagsChargesRefreshToItsOwnPhase pins the property the per-root
// phases depend on to mean anything.
//
// Activity opens with Refresh, and Refresh walks every root. While that
// call was implicit, the first root's phase carried the whole corpus's
// indexing cost and the trace named it as the expensive root — a
// diagnostic that points at the wrong place is worse than none, which is
// the failure this whole instrumentation exists to end.
func TestDocFlagsChargesRefreshToItsOwnPhase(t *testing.T) {
	dir := t.TempDir()
	roots := make(map[string]string, 2)
	revisers := make(map[string]documents.RootReviser, 2)
	for _, name := range []string{"alpha", "beta"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		doc := "---\ntitle: Doc\ntags: [a]\n---\n\n# Heading\n\nBody text.\n"
		if err := os.WriteFile(filepath.Join(path, "doc.md"), []byte(doc), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		roots[name] = path
		revisers[name] = stubReviser{}
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := documents.NewStoreWithOptions(db, roots, nil, documents.StoreOptions{RootRevisers: revisers})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}

	probe := NewDocFlags(store)
	if probe == nil {
		t.Fatal("NewDocFlags returned nil for a store with revision-backed roots")
	}

	ctx, trace := phasetrace.New(context.Background())
	if _, err := probe(ctx); err != nil {
		t.Fatalf("doc flags: %v", err)
	}

	summary := trace.Summary()
	for _, want := range []string{"doc_flags=", "doc_flags:refresh=", "doc_flags:alpha=", "doc_flags:beta="} {
		if !strings.Contains(summary, want) {
			t.Errorf("phase %q missing from trace %q", want, summary)
		}
	}
}

// TestDocFlagsSkipsRefreshWithoutRevisionBackedRoots keeps the probe from
// paying for a whole-corpus walk it has no use for: with nothing to
// sweep there is nothing to refresh for.
func TestDocFlagsSkipsRefreshWithoutRevisionBackedRoots(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte("---\ntitle: Doc\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := documents.NewStore(db, map[string]string{"plain": dir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ctx, trace := phasetrace.New(context.Background())
	flagged, err := NewDocFlags(store)(ctx)
	if err != nil {
		t.Fatalf("doc flags: %v", err)
	}
	if len(flagged) != 0 {
		t.Errorf("flagged %d documents across zero revision-backed roots", len(flagged))
	}
	if summary := trace.Summary(); strings.Contains(summary, "doc_flags:refresh=") {
		t.Errorf("refreshed the whole corpus with no root to sweep: %q", summary)
	}
}

// TestHealthTracesTheBootJournal keeps the one health source that leaves
// the process from hiding inside the aggregate.
//
// Every other collector reads memory or the local database; the boot
// journal shells out to the host's journal, twice, and is therefore the
// standing suspect whenever the panel is slow. A phase that stops at
// "health" cannot rule it in or out.
func TestHealthTracesTheBootJournal(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	insp := NewInspector(HealthSources{
		BuildVersion: "v0.10.3",
		BuildCommit:  "abcdef1234567",
		BootHistory: func(context.Context) ([]BootRecord, error) {
			return []BootRecord{{At: now.Add(-time.Hour), Version: "v0.10.3", Commit: "abcdef1234567"}}, nil
		},
		BootCountSince: func(context.Context, time.Time) (int, error) { return 1, nil },
	})
	insp.now = func() time.Time { return now }

	ctx, trace := phasetrace.New(context.Background())
	insp.Health(ctx)

	summary := trace.Summary()
	for _, want := range []string{"health:boot_history=", "health:boot_count="} {
		if !strings.Contains(summary, want) {
			t.Errorf("phase %q missing from trace %q", want, summary)
		}
	}
}

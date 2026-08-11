package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// recordingReviser serves canned per-file history/diffs and records the
// diff base so tests can pin the spanning-diff selection.
type recordingReviser struct {
	listings  map[string]RevisionListing
	diffs     map[string]RevisionDiff
	diffBases map[string]string
}

func (r *recordingReviser) Resolve(context.Context, string, string) (RevisionRef, error) {
	return RevisionRef{}, nil
}

func (r *recordingReviser) History(_ context.Context, filename string, _ RevisionQuery) (RevisionListing, error) {
	return r.listings[filename], nil
}

func (r *recordingReviser) Diff(_ context.Context, filename, from, _, _ string) (RevisionDiff, error) {
	if r.diffBases == nil {
		r.diffBases = make(map[string]string)
	}
	r.diffBases[filename] = from
	return r.diffs[filename], nil
}

func (r *recordingReviser) Content(context.Context, string, string) (RevisionContent, error) {
	return RevisionContent{}, nil
}

func newActivityStore(t *testing.T, files map[string]string, reviser RootReviser) *Store {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	revisers := map[string]RootReviser{}
	if reviser != nil {
		revisers["self"] = reviser
	}
	store, err := NewStoreWithOptions(db, map[string]string{"self": dir}, nil, StoreOptions{RootRevisers: revisers})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return store
}

func TestActivityErrorsTeachTheNextMove(t *testing.T) {
	store := newActivityStore(t, map[string]string{"ego.md": "# ego\n"}, &recordingReviser{})

	if _, err := store.Activity(t.Context(), ActivityQuery{Root: "nope", Since: time.Now().Add(-time.Hour)}); err == nil || !strings.Contains(err.Error(), "known roots") {
		t.Errorf("unknown-root error must name the known roots, got %v", err)
	}

	bare := newActivityStore(t, map[string]string{"a.md": "# a\n"}, nil)
	if _, err := bare.Activity(t.Context(), ActivityQuery{Root: "self", Since: time.Now().Add(-time.Hour)}); err == nil || !strings.Contains(err.Error(), "keeps no revision history") {
		t.Errorf("revision-less root error must teach which roots qualify, got %v", err)
	}
}

func TestActivityReportsChurnFlagsAndAuthors(t *testing.T) {
	now := time.Now().UTC()
	loopRev := func(age time.Duration, commit, loopID, model string) RevisionRef {
		return RevisionRef{
			Commit: commit, Short: commit[:4], Timestamp: now.Add(-age),
			Trailers: map[string]string{TrailerLoopID: loopID, TrailerModel: model},
		}
	}
	manualRev := func(age time.Duration, commit string) RevisionRef {
		return RevisionRef{Commit: commit, Short: commit[:4], Timestamp: now.Add(-age)}
	}

	reviser := &recordingReviser{
		listings: map[string]RevisionListing{
			// ego.md: three in-window loop revisions plus one older —
			// the older one is the spanning-diff base.
			"ego.md": {Revisions: []RevisionRef{
				loopRev(1*time.Hour, "aaaa1111", "lp_ego", "haiku"),
				loopRev(5*time.Hour, "bbbb2222", "lp_ego", "haiku"),
				manualRev(9*time.Hour, "cccc3333"),
				loopRev(72*time.Hour, "dddd4444", "lp_ego", "haiku"), // outside the window
			}},
			// notes.md: quiet — nothing in the window despite a fresh mtime.
			"notes.md": {Revisions: []RevisionRef{manualRev(72*time.Hour, "eeee5555")}},
		},
		diffs: map[string]RevisionDiff{
			"ego.md": {Added: 120, Removed: 40},
		},
	}
	store := newActivityStore(t, map[string]string{
		"ego.md":   "# ego\ncontent\n",
		"notes.md": "# notes\n",
	}, reviser)

	report, err := store.Activity(t.Context(), ActivityQuery{
		Root:              "self",
		Since:             now.Add(-24 * time.Hour),
		RevisionThreshold: 3,
	})
	if err != nil {
		t.Fatalf("activity: %v", err)
	}
	if report.Total != 1 || len(report.Documents) != 1 {
		t.Fatalf("report = %+v, want exactly ego.md (notes.md has no in-window revisions)", report)
	}

	doc := report.Documents[0]
	if doc.Ref != "self:ego.md" || doc.Revisions != 3 {
		t.Errorf("doc = %s with %d revisions, want self:ego.md with 3", doc.Ref, doc.Revisions)
	}
	if doc.LinesAdded != 120 || doc.LinesRemoved != 40 || doc.NetLineDelta != 80 {
		t.Errorf("line math = +%d/-%d net %d, want +120/-40 net 80", doc.LinesAdded, doc.LinesRemoved, doc.NetLineDelta)
	}
	if doc.SizeBytes <= 0 {
		t.Errorf("size_bytes not joined from the index: %+v", doc)
	}
	if !doc.Flagged || !strings.Contains(doc.FlagReason, "3 revisions") {
		t.Errorf("threshold 3 with 3 revisions must flag, got %+v", doc)
	}
	// The spanning diff runs from the newest revision OLDER than the
	// window — the state in force at window start.
	if base := reviser.diffBases["ego.md"]; base != "dddd4444" {
		t.Errorf("diff base = %q, want the pre-window revision dddd4444", base)
	}
	// Authors: the loop twice, the hand-written revision as "manual".
	if len(doc.Authors) != 2 || doc.Authors[0].Author != "lp_ego" || doc.Authors[0].Revisions != 2 || doc.Authors[0].Model != "haiku" {
		t.Errorf("authors = %+v, want lp_ego(haiku)x2 leading", doc.Authors)
	}
	if doc.Authors[1].Author != "manual" || doc.Authors[1].Revisions != 1 {
		t.Errorf("authors[1] = %+v, want manual x1", doc.Authors[1])
	}
}

func TestActivityDocBornInWindowFallsBackToOldestRevision(t *testing.T) {
	now := time.Now().UTC()
	reviser := &recordingReviser{
		listings: map[string]RevisionListing{
			"fresh.md": {Revisions: []RevisionRef{
				{Commit: "aaaa1111", Short: "aaaa", Timestamp: now.Add(-time.Hour)},
				{Commit: "bbbb2222", Short: "bbbb", Timestamp: now.Add(-2 * time.Hour)},
			}},
		},
		diffs: map[string]RevisionDiff{"fresh.md": {Added: 10}},
	}
	store := newActivityStore(t, map[string]string{"fresh.md": "# fresh\n"}, reviser)

	report, err := store.Activity(t.Context(), ActivityQuery{Root: "self", Since: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatalf("activity: %v", err)
	}
	if len(report.Documents) != 1 || report.Documents[0].Revisions != 2 {
		t.Fatalf("report = %+v, want fresh.md with 2 revisions", report)
	}
	if base := reviser.diffBases["fresh.md"]; base != "bbbb2222" {
		t.Errorf("diff base = %q, want the oldest in-window revision", base)
	}
}

func TestActivityTruncatesFlaggedFirst(t *testing.T) {
	now := time.Now().UTC()
	listings := map[string]RevisionListing{}
	files := map[string]string{}
	// quiet-1..3 have one in-window revision; busy.md has nine.
	for _, name := range []string{"quiet-1.md", "quiet-2.md", "quiet-3.md"} {
		files[name] = "# q\n"
		listings[name] = RevisionListing{Revisions: []RevisionRef{
			{Commit: "aaaa0000", Short: "aaaa", Timestamp: now.Add(-time.Hour)},
		}}
	}
	files["busy.md"] = "# b\n"
	var busy []RevisionRef
	for i := range 9 {
		busy = append(busy, RevisionRef{
			Commit: "bbbb000" + string(rune('0'+i)), Short: "bbbb",
			Timestamp: now.Add(-time.Duration(i+1) * time.Hour),
		})
	}
	listings["busy.md"] = RevisionListing{Revisions: busy}

	store := newActivityStore(t, files, &recordingReviser{listings: listings})
	report, err := store.Activity(t.Context(), ActivityQuery{
		Root: "self", Since: now.Add(-24 * time.Hour), Limit: 2,
	})
	if err != nil {
		t.Fatalf("activity: %v", err)
	}
	if report.Total != 4 || !report.Truncated || len(report.Documents) != 2 {
		t.Fatalf("report total=%d truncated=%v len=%d, want 4/true/2", report.Total, report.Truncated, len(report.Documents))
	}
	if report.Documents[0].Ref != "self:busy.md" || !report.Documents[0].Flagged {
		t.Errorf("flagged runaway must survive the cap, got %+v", report.Documents[0])
	}
}

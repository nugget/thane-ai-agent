package provenance

import (
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// buildReaderRepo creates a store with three signed revisions of kb/doc.md:
// "a\n" -> "a\nb\n" -> "a\nb\nc\n" (messages first/second/third).
func buildReaderRepo(t *testing.T) *Store {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	s, err := New(dir, testSigner(t), slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, step := range []struct{ content, msg string }{
		{"a\n", "first"},
		{"a\nb\n", "second"},
		{"a\nb\nc\n", "third"},
	} {
		if err := s.Write(t.Context(), "kb/doc.md", step.content, step.msg); err != nil {
			t.Fatalf("Write %q: %v", step.msg, err)
		}
	}
	return s
}

func TestReaderRevisions(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()

	page, err := s.Revisions(ctx, "kb/doc.md", RevisionOptions{})
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("Total = %d, want 3", page.Total)
	}
	if len(page.Revisions) != 3 {
		t.Fatalf("len(Revisions) = %d, want 3", len(page.Revisions))
	}
	wantMsgs := []string{"third", "second", "first"}
	for i, rev := range page.Revisions {
		if rev.Message != wantMsgs[i] {
			t.Fatalf("Revisions[%d].Message = %q, want %q", i, rev.Message, wantMsgs[i])
		}
		if rev.Index != i {
			t.Fatalf("Revisions[%d].Index = %d, want %d", i, rev.Index, i)
		}
		if rev.Short == "" || len(rev.Short) > len(rev.Commit) {
			t.Fatalf("Revisions[%d].Short = %q (commit %q)", i, rev.Short, rev.Commit)
		}
	}
	if page.NextBefore != "" {
		t.Fatalf("NextBefore = %q, want empty at end of history", page.NextBefore)
	}
}

func TestReaderRevisionsPagination(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()

	first, err := s.Revisions(ctx, "kb/doc.md", RevisionOptions{Limit: 2})
	if err != nil {
		t.Fatalf("Revisions page 1: %v", err)
	}
	if len(first.Revisions) != 2 || first.NextBefore == "" {
		t.Fatalf("page 1 = %d revs, NextBefore=%q; want 2 and a cursor", len(first.Revisions), first.NextBefore)
	}
	if first.Revisions[0].Message != "third" || first.Revisions[1].Message != "second" {
		t.Fatalf("page 1 messages = %q/%q, want third/second", first.Revisions[0].Message, first.Revisions[1].Message)
	}

	second, err := s.Revisions(ctx, "kb/doc.md", RevisionOptions{Limit: 2, Before: first.NextBefore})
	if err != nil {
		t.Fatalf("Revisions page 2: %v", err)
	}
	if len(second.Revisions) != 1 || second.NextBefore != "" {
		t.Fatalf("page 2 = %d revs, NextBefore=%q; want 1 and no cursor", len(second.Revisions), second.NextBefore)
	}
	if second.Revisions[0].Message != "first" {
		t.Fatalf("page 2 message = %q, want first", second.Revisions[0].Message)
	}
	if second.Revisions[0].Index != 2 {
		t.Fatalf("page 2 index = %d, want 2", second.Revisions[0].Index)
	}
}

func TestReaderResolveRevision(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()

	head, err := s.ResolveRevision(ctx, "kb/doc.md", "HEAD")
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	if head.Message != "third" || head.Index != 0 {
		t.Fatalf("HEAD = %q idx %d, want third idx 0", head.Message, head.Index)
	}
	if latest, err := s.ResolveRevision(ctx, "kb/doc.md", "latest"); err != nil || latest.Commit != head.Commit {
		t.Fatalf("latest = %+v, %v; want same as HEAD", latest, err)
	}

	// Resolve the middle commit by hash and confirm its index.
	page, _ := s.Revisions(ctx, "kb/doc.md", RevisionOptions{})
	mid := page.Revisions[1] // "second"
	byHash, err := s.ResolveRevision(ctx, "kb/doc.md", mid.Commit)
	if err != nil {
		t.Fatalf("resolve by hash: %v", err)
	}
	if byHash.Commit != mid.Commit || byHash.Index != 1 {
		t.Fatalf("byHash = %q idx %d, want %q idx 1", byHash.Commit, byHash.Index, mid.Commit)
	}

	// A timestamp at/after HEAD resolves to the newest commit; one before the
	// first commit resolves to nothing. Use the commits' real times so this
	// doesn't depend on git's handling of far-future dates.
	afterHead := head.Timestamp.Add(time.Second).UTC().Format(time.RFC3339)
	if at, err := s.ResolveRevision(ctx, "kb/doc.md", afterHead); err != nil || at.Commit != head.Commit {
		t.Fatalf("timestamp after HEAD = %+v, %v; want newest", at, err)
	}
	beforeFirst := page.Revisions[2].Timestamp.Add(-time.Second).UTC().Format(time.RFC3339)
	if _, err := s.ResolveRevision(ctx, "kb/doc.md", beforeFirst); err == nil {
		t.Fatal("timestamp before the first commit resolved a revision, want error")
	}

	// Unknown selector and a missing file both error.
	if _, err := s.ResolveRevision(ctx, "kb/doc.md", "deadbeefcafe"); err == nil {
		t.Fatal("unknown selector resolved, want error")
	}
	if _, err := s.ResolveRevision(ctx, "kb/missing.md", "HEAD"); err == nil {
		t.Fatal("missing file resolved, want error")
	}
}

func TestReaderResolveHashRejectsFileNotAtCommit(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()
	// A commit exists (HEAD), but a different file did not exist at it.
	head, _ := s.ResolveRevision(ctx, "kb/doc.md", "HEAD")
	if _, err := s.ResolveRevision(ctx, "kb/other.md", head.Commit); err == nil {
		t.Fatal("resolved a hash for a file that never existed there, want error")
	}
}

func TestReaderBlob(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()

	if got, err := s.Blob(ctx, "HEAD", "kb/doc.md"); err != nil || got != "a\nb\nc\n" {
		t.Fatalf("Blob HEAD = %q, %v; want \"a\\nb\\nc\\n\"", got, err)
	}
	page, _ := s.Revisions(ctx, "kb/doc.md", RevisionOptions{})
	firstCommit := page.Revisions[2].Commit
	if got, err := s.Blob(ctx, firstCommit, "kb/doc.md"); err != nil || got != "a\n" {
		t.Fatalf("Blob first = %q, %v; want \"a\\n\"", got, err)
	}
}

func TestReaderDiff(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()
	page, _ := s.Revisions(ctx, "kb/doc.md", RevisionOptions{})
	first := page.Revisions[2].Commit
	head := page.Revisions[0].Commit

	diff, err := s.Diff(ctx, first, head, "kb/doc.md", DiffPatch)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff.Added != 2 || diff.Removed != 0 {
		t.Fatalf("Diff counts = +%d/-%d, want +2/-0", diff.Added, diff.Removed)
	}
	if !strings.Contains(diff.Body, "+b") || !strings.Contains(diff.Body, "+c") {
		t.Fatalf("Diff body missing added lines:\n%s", diff.Body)
	}
	// Diff body must speak clean patch — never leak stderr noise.
	if strings.Contains(diff.Body, "fatal:") {
		t.Fatalf("Diff body contains stderr noise:\n%s", diff.Body)
	}
}

// TestReaderVerifierSatisfiesReader confirms a Verifier (no Store) reads the
// same history — the reason Reader is an interface both implement.
func TestReaderVerifierSatisfiesReader(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()

	verifier, err := NewVerifier(s.path, slog.Default(), Options{})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	var r Reader = verifier
	page, err := r.Revisions(ctx, "kb/doc.md", RevisionOptions{})
	if err != nil {
		t.Fatalf("Verifier.Revisions: %v", err)
	}
	if page.Total != 3 || len(page.Revisions) != 3 {
		t.Fatalf("Verifier history = %d/%d, want 3/3", page.Total, len(page.Revisions))
	}
	if got, err := r.Blob(ctx, "HEAD", "kb/doc.md"); err != nil || got != "a\nb\nc\n" {
		t.Fatalf("Verifier.Blob = %q, %v", got, err)
	}
}

// TestReaderRejectsArgumentInjection locks in the hardening: option-like
// revisions, traversal/absolute paths, and bad cursors are refused before
// reaching git.
func TestReaderRejectsArgumentInjection(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()

	if _, err := s.ResolveRevision(ctx, "kb/doc.md", "--output=/tmp/x"); err == nil {
		t.Fatal("ResolveRevision accepted an option-like selector")
	}
	if _, err := s.Blob(ctx, "--upload-pack=x", "kb/doc.md"); err == nil {
		t.Fatal("Blob accepted an option-like revision")
	}
	if _, err := s.Diff(ctx, "-O/tmp/x", "HEAD", "kb/doc.md", DiffPatch); err == nil {
		t.Fatal("Diff accepted an option-like from")
	}
	if _, err := s.SignerFor(ctx, "--foo"); err == nil {
		t.Fatal("SignerFor accepted an option-like commit")
	}

	// Path traversal and absolute paths are refused.
	if _, err := s.ResolveRevision(ctx, "../escape.md", "HEAD"); err == nil {
		t.Fatal("ResolveRevision accepted a traversal path")
	}
	if _, err := s.Blob(ctx, "HEAD", "/etc/passwd"); err == nil {
		t.Fatal("Blob accepted an absolute path")
	}
	if _, err := s.Blob(ctx, "HEAD", ":(exclude)kb/doc.md"); err == nil {
		t.Fatal("Blob accepted a git pathspec-magic filename")
	}

	// A syntactically valid but nonexistent cursor surfaces an error rather
	// than masking as an empty page.
	if _, err := s.Revisions(ctx, "kb/doc.md", RevisionOptions{Before: "deadbeefcafe"}); err == nil {
		t.Fatal("Revisions accepted an invalid pagination cursor")
	}
}

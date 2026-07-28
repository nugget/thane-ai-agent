package provenance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The trailer contract spans two systems: git renders the trailer block, and
// this package parses it back. Unit-testing the parser against strings we wrote
// ourselves proves only that we agree with ourselves, so this drives a real
// commit through real git and reads it back the way the document tools do.
func TestTrailersSurviveARealCommit(t *testing.T) {
	dir := initRepo(t)
	writeDoc(t, dir, "body\n")
	runGit(t, dir, "add", "doc.md")
	runGit(t, dir, "commit", "-q", "-m",
		"doc_write self:metacognitive.md\n\n"+
			"Thane-Model: gpt-oss:120b\n"+
			"Thane-Iteration: 3\n"+
			"Thane-Core-Head: 9dedd7e3b03c3a392bd25d09dd7f597862382c17\n"+
			"Reconstructed-From: thane.db tool_calls\n")

	page, err := readRevisions(context.Background(), dir, "", "doc.md", RevisionOptions{})
	if err != nil {
		t.Fatalf("readRevisions: %v", err)
	}
	if len(page.Revisions) != 1 {
		t.Fatalf("got %d revisions, want 1", len(page.Revisions))
	}
	rev := page.Revisions[0]

	// The subject must survive untouched — trailers ride along with the message,
	// they do not consume it.
	if rev.Message != "doc_write self:metacognitive.md" {
		t.Errorf("subject = %q, want the message the writer passed", rev.Message)
	}
	// Reconstructed-From is here on purpose: a trailer this build does not know
	// must still reach the reader, or the tool becomes a worse witness than git.
	want := map[string]string{
		"Thane-Model":        "gpt-oss:120b",
		"Thane-Iteration":    "3",
		"Thane-Core-Head":    "9dedd7e3b03c3a392bd25d09dd7f597862382c17",
		"Reconstructed-From": "thane.db tool_calls",
	}
	for key, value := range want {
		if rev.Trailers[key] != value {
			t.Errorf("trailer %s = %q, want %q (parsed: %v)", key, rev.Trailers[key], value, rev.Trailers)
		}
	}
}

// A commit carrying no trailers must report none rather than an empty map, so a
// caller can tell "not written by a loop" from "written with nothing to say".
func TestARevisionWithoutTrailersReportsNone(t *testing.T) {
	dir := initRepo(t)
	writeDoc(t, dir, "body\n")
	runGit(t, dir, "add", "doc.md")
	runGit(t, dir, "commit", "-q", "-m", "hand-authored change")

	page, err := readRevisions(context.Background(), dir, "", "doc.md", RevisionOptions{})
	if err != nil {
		t.Fatalf("readRevisions: %v", err)
	}
	if got := page.Revisions[0].Trailers; got != nil {
		t.Errorf("Trailers = %v, want nil", got)
	}
}

func TestHeadCommitReportsAResolvableHash(t *testing.T) {
	head, err := HeadCommit(context.Background(), initRepo(t))
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if len(head) != 40 {
		t.Errorf("HeadCommit = %q, want a full 40-character hash", head)
	}
}

// A root that exists but has no history yet must error rather than report an
// empty hash, so a caller recording HEAD can tell the two apart.
func TestHeadCommitErrorsWithoutHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "-c", "init.defaultBranch=main", "init")
	if _, err := HeadCommit(context.Background(), dir); err == nil {
		t.Error("expected an error for a repository with no commits")
	}
}

func writeDoc(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

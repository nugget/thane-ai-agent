package provenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A document root is a directory an agent writes files into, so a file whose
// name collides with a revision is a question of when, not whether. Git refuses
// an ambiguous argument with usage advice rather than an answer, and admission
// reported that as though the history itself were unreadable — a root that
// verifies perfectly well was refused at boot by a stray file beside it.
func TestAdmissionSurvivesAFileNamedLikeARevision(t *testing.T) {
	for _, name := range []string{"HEAD", "main"} {
		t.Run(name, func(t *testing.T) {
			dir := initRepo(t)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("not a ref\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			commits, err := rootCommits(context.Background(), dir)
			if err != nil {
				t.Fatalf("rootCommits with a file named %q: %v", name, err)
			}
			if len(commits) != 1 {
				t.Errorf("got %d root commits, want 1", len(commits))
			}
		})
	}
}

// The same collision must not reach the trust-file history either, which is
// walked by the third admission rule.
func TestTrustFileHistorySurvivesTheSameCollision(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("not a ref\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := trustFileCommits(context.Background(), dir); err != nil {
		t.Fatalf("trustFileCommits with a file named HEAD: %v", err)
	}
}

// An empty repository still has to report "no history" rather than an error, so
// the added separator must not turn the unborn-HEAD case into a failure.
func TestRootCommitsStillReportsNoHistoryForAnEmptyRepo(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "-c", "init.defaultBranch=main", "init")

	commits, err := rootCommits(context.Background(), dir)
	if err != nil {
		t.Fatalf("rootCommits on an empty repo: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("got %d root commits, want none", len(commits))
	}
}

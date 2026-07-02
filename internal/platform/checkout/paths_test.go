package checkout

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRoot(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	repo := filepath.Join(rootDir, "repo")
	child := filepath.Join(repo, "knowledge", "kb")

	root, err := ResolveRoot(repo, child)
	if err != nil {
		t.Fatalf("ResolveRoot child: %v", err)
	}
	if root.Prefix != "knowledge/kb" {
		t.Fatalf("Prefix = %q, want knowledge/kb", root.Prefix)
	}
	if !filepath.IsAbs(root.RepoPath) || !filepath.IsAbs(root.WorktreePath) {
		t.Fatalf("paths should be absolute: %+v", root)
	}

	root, err = ResolveRoot(repo, repo)
	if err != nil {
		t.Fatalf("ResolveRoot same: %v", err)
	}
	if root.Prefix != "" {
		t.Fatalf("same-root Prefix = %q, want empty", root.Prefix)
	}

	_, err = ResolveRoot(filepath.Join(rootDir, "other"), child)
	if err == nil {
		t.Fatal("ResolveRoot outside repo returned nil, want error")
	}
	if !strings.Contains(err.Error(), "one of its parents") {
		t.Fatalf("error = %v, want parent relationship message", err)
	}
}

func TestRepoFilenameDoesNotCleanEscapesIntoPrefix(t *testing.T) {
	t.Parallel()

	if got := RepoFilename("knowledge/kb", "notes/doc.md"); got != "knowledge/kb/notes/doc.md" {
		t.Fatalf("RepoFilename(valid) = %q, want prefixed path", got)
	}
	if got := RepoFilename("knowledge/kb", "../outside.md"); got != "../outside.md" {
		t.Fatalf("RepoFilename(escape) = %q, want provenance validator to see escape", got)
	}
	if got := RepoFilename("knowledge/kb", "."); got != "." {
		t.Fatalf("RepoFilename(dot) = %q, want unchanged dot", got)
	}
	if got := RepoFilename("knowledge/kb", "notes/../doc.md"); got != "knowledge/kb/doc.md" {
		t.Fatalf("RepoFilename(clean) = %q, want cleaned path under prefix", got)
	}
	if got := RepoFilename("", "doc.md"); got != "doc.md" {
		t.Fatalf("RepoFilename(no prefix) = %q, want unchanged", got)
	}
}

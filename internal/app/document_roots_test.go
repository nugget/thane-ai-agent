package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func TestBuildDocumentRootsOnlyIncludesExistingDirectories(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	resolver := paths.New(map[string]string{
		"kb":      kbDir,
		"missing": filepath.Join(rootDir, "missing"),
	})

	roots := buildDocumentRoots(resolver)
	if len(roots) != 1 {
		t.Fatalf("len(roots) = %d, want 1: %#v", len(roots), roots)
	}
	if roots["kb"] == "" || !filepath.IsAbs(roots["kb"]) {
		t.Fatalf("roots[kb] = %q, want absolute path", roots["kb"])
	}
	if _, ok := roots["missing"]; ok {
		t.Fatalf("missing root included: %#v", roots)
	}
}

func TestBuildDocumentStoreOptionsMapsConfigPolicy(t *testing.T) {
	t.Parallel()

	indexing := false
	app := &App{cfg: &config.Config{
		DocRoots: map[string]config.DocumentRootConfig{
			"kb:": {
				Indexing:  &indexing,
				Authoring: "read_only",
				Git: config.DocumentRootGitConfig{
					Enabled:          true,
					VerifySignatures: "required",
					RepoPath:         "~/repo",
					AllowedSigners:   "~/allowed_signers",
				},
			},
		},
	}}
	opts, err := app.buildDocumentStoreOptions(map[string]string{"kb": t.TempDir()}, nil)
	if err != nil {
		t.Fatalf("buildDocumentStoreOptions: %v", err)
	}
	policy := opts.RootPolicies["kb"]
	if policy.Indexing || policy.Authoring != documents.AuthoringReadOnly {
		t.Fatalf("policy = %#v, want non-indexed read_only", policy)
	}
	if !policy.Git.Enabled || policy.Git.VerifySignatures != documents.VerificationRequired {
		t.Fatalf("policy.Git = %#v, want enabled required verification", policy.Git)
	}
	if len(opts.RootWriters) != 0 {
		t.Fatalf("RootWriters = %#v, want none without sign_commits", opts.RootWriters)
	}
}

func TestBuildDocumentStoreOptionsRejectsUnknownPolicyRoot(t *testing.T) {
	t.Parallel()

	app := &App{cfg: &config.Config{
		DocRoots: map[string]config.DocumentRootConfig{
			"ghost": {Authoring: "managed"},
		},
	}}
	_, err := app.buildDocumentStoreOptions(map[string]string{"kb": t.TempDir()}, nil)
	if err == nil {
		t.Fatal("buildDocumentStoreOptions returned nil, want unknown root error")
	}
	if !strings.Contains(err.Error(), "doc_roots.ghost references a document root") {
		t.Fatalf("error = %v, want unknown root message", err)
	}
}

func TestRootPrefixWithinRepo(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	repo := filepath.Join(rootDir, "repo")
	child := filepath.Join(repo, "knowledge", "kb")
	prefix, err := rootPrefixWithinRepo(repo, child)
	if err != nil {
		t.Fatalf("rootPrefixWithinRepo child: %v", err)
	}
	if prefix != "knowledge/kb" {
		t.Fatalf("prefix = %q, want knowledge/kb", prefix)
	}
	prefix, err = rootPrefixWithinRepo(repo, repo)
	if err != nil {
		t.Fatalf("rootPrefixWithinRepo same: %v", err)
	}
	if prefix != "" {
		t.Fatalf("same-root prefix = %q, want empty", prefix)
	}
	_, err = rootPrefixWithinRepo(filepath.Join(rootDir, "other"), child)
	if err == nil {
		t.Fatal("rootPrefixWithinRepo outside repo returned nil, want error")
	}
}

func TestDocumentRootProvenanceWriterDoesNotCleanEscapesIntoPrefix(t *testing.T) {
	t.Parallel()

	writer := &documentRootProvenanceWriter{prefix: "knowledge/kb"}
	if got := writer.storeFilename("notes/doc.md"); got != "knowledge/kb/notes/doc.md" {
		t.Fatalf("storeFilename(valid) = %q, want prefixed path", got)
	}
	if got := writer.storeFilename("../outside.md"); got != "../outside.md" {
		t.Fatalf("storeFilename(escape) = %q, want provenance validator to see escape", got)
	}
}

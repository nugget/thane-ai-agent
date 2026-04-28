package app

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/identity"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// writeTestSigningKey generates a fresh ed25519 SSH signing key,
// writes it to a temp path, and returns the path. The matching public
// key is also written for tests that need .allowed_signers.
func writeTestSigningKey(t *testing.T) (privPath, pub string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	pair, err := identity.GenerateSigningKeyPair("doc-roots-test")
	if err != nil {
		t.Fatalf("GenerateSigningKeyPair: %v", err)
	}
	dir := t.TempDir()
	privPath = filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(privPath, pair.PrivatePEM, 0o600); err != nil {
		t.Fatalf("write signing key: %v", err)
	}
	return privPath, strings.TrimSpace(pair.Public)
}

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

	// Use warn rather than required so a bogus repo path (which is
	// what the original config-policy mapping test uses to keep the
	// test hermetic) doesn't trigger the new "required-but-
	// unavailable verifier is fatal" guard. This test cares about
	// policy mapping, not verifier wiring.
	indexing := false
	app := &App{cfg: &config.Config{
		DocRoots: map[string]config.DocumentRootConfig{
			"kb:": {
				Indexing:  &indexing,
				Authoring: "read_only",
				Git: config.DocumentRootGitConfig{
					Enabled:          true,
					VerifySignatures: "warn",
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
	if !policy.Git.Enabled || policy.Git.VerifySignatures != documents.VerificationWarn {
		t.Fatalf("policy.Git = %#v, want enabled warn verification", policy.Git)
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

// TestBuildDocumentStoreOptionsBootstrapsMissingDirectory covers issue
// #789: a doc_roots entry that signs commits should bootstrap a
// missing directory (mkdir + git init + signed birth commit) so
// verification has a baseline. Without this, the operator would have
// to mkdir+git init+commit by hand before pointing config at it.
func TestBuildDocumentStoreOptionsBootstrapsMissingDirectory(t *testing.T) {
	rootDir := t.TempDir()
	// Note: targetPath does NOT exist before the bootstrap call —
	// we want the doc-root layer to create it.
	targetPath := filepath.Join(rootDir, "kb")

	signingKey, _ := writeTestSigningKey(t)

	resolver := paths.New(map[string]string{"kb": targetPath})
	app := &App{
		logger: slog.Default(),
		cfg: &config.Config{
			DocRoots: map[string]config.DocumentRootConfig{
				"kb": {
					Authoring: "managed",
					Git: config.DocumentRootGitConfig{
						Enabled:          true,
						SignCommits:      true,
						VerifySignatures: "required",
						SigningKey:       signingKey,
					},
				},
			},
		},
	}

	opts, err := app.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver)
	if err != nil {
		t.Fatalf("buildDocumentStoreOptions: %v", err)
	}

	// Directory should now exist.
	if info, err := os.Stat(targetPath); err != nil || !info.IsDir() {
		t.Fatalf("bootstrap should have created %s as a directory: err=%v", targetPath, err)
	}
	// Git repo should exist.
	if _, err := os.Stat(filepath.Join(targetPath, ".git")); err != nil {
		t.Fatalf("bootstrap should have run git init: %v", err)
	}
	// HEAD should exist (birth commit).
	cmd := exec.CommandContext(context.Background(), "git", "-C", targetPath, "rev-parse", "--verify", "HEAD^{commit}")
	if err := cmd.Run(); err != nil {
		t.Fatalf("birth commit not found: %v", err)
	}

	// Both writer and verifier should be wired.
	if _, ok := opts.RootWriters["kb"]; !ok {
		t.Errorf("RootWriters[kb] missing")
	}
	if _, ok := opts.RootVerifiers["kb"]; !ok {
		t.Errorf("RootVerifiers[kb] missing — would mean required verification was silently disabled")
	}
}

// TestBuildDocumentStoreOptionsBootstrapsEmptyDirectory covers the
// case where the directory already exists but is empty. This is the
// most common "I just made this directory and added it to config"
// shape. Bootstrap should run identically to the missing-dir case.
func TestBuildDocumentStoreOptionsBootstrapsEmptyDirectory(t *testing.T) {
	targetPath := t.TempDir() // exists but empty

	signingKey, _ := writeTestSigningKey(t)

	resolver := paths.New(map[string]string{"kb": targetPath})
	app := &App{
		logger: slog.Default(),
		cfg: &config.Config{
			DocRoots: map[string]config.DocumentRootConfig{
				"kb": {
					Git: config.DocumentRootGitConfig{
						Enabled:          true,
						SignCommits:      true,
						VerifySignatures: "required",
						SigningKey:       signingKey,
					},
				},
			},
		},
	}

	opts, err := app.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver)
	if err != nil {
		t.Fatalf("buildDocumentStoreOptions: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetPath, ".git")); err != nil {
		t.Fatalf("git init did not run on empty directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetPath, ".gitignore")); err != nil {
		t.Fatalf("birth commit should have written .gitignore: %v", err)
	}
	if opts.RootVerifiers["kb"] == nil {
		t.Fatal("verifier missing on freshly bootstrapped root")
	}
}

// TestBuildDocumentStoreOptionsRequiredVerifierUnavailableIsFatal
// covers the "no silent disable" guarantee. If verify_signatures is
// required but the verifier can't be constructed, startup must fail
// rather than silently downgrade to no-verification.
func TestBuildDocumentStoreOptionsRequiredVerifierUnavailableIsFatal(t *testing.T) {
	t.Parallel()

	targetPath := t.TempDir()
	resolver := paths.New(map[string]string{"kb": targetPath})
	missingAllowedSigners := filepath.Join(t.TempDir(), "does-not-exist")

	app := &App{
		logger: slog.Default(),
		cfg: &config.Config{
			DocRoots: map[string]config.DocumentRootConfig{
				"kb": {
					Git: config.DocumentRootGitConfig{
						Enabled:          true,
						SignCommits:      false, // verifier-only path
						VerifySignatures: "required",
						AllowedSigners:   missingAllowedSigners,
					},
				},
			},
		},
	}

	_, err := app.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver)
	if err == nil {
		t.Fatal("expected fatal error for required-but-unavailable verifier")
	}
	if !strings.Contains(err.Error(), "verify_signatures=required but verifier unavailable") {
		t.Fatalf("error = %v, want required-but-unavailable message", err)
	}
}

// TestBuildDocumentStoreOptionsNonBootstrapMissingDirStillErrors
// preserves the original behavior for non-bootstrap configs: a
// missing directory without git+sign_commits stays an error, since
// we have no way to know whether the operator wants it created.
func TestBuildDocumentStoreOptionsNonBootstrapMissingDirStillErrors(t *testing.T) {
	t.Parallel()

	resolver := paths.New(map[string]string{"kb": filepath.Join(t.TempDir(), "missing")})
	app := &App{
		logger: slog.Default(),
		cfg: &config.Config{
			DocRoots: map[string]config.DocumentRootConfig{
				"kb": {Authoring: "read_only"},
			},
		},
	}
	_, err := app.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver)
	if err == nil {
		t.Fatal("expected error for missing dir without bootstrap policy")
	}
	if !strings.Contains(err.Error(), "does not exist on disk") {
		t.Fatalf("error = %v, want does-not-exist message", err)
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

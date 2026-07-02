package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/identity"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// writeTestSigningKey generates a fresh ed25519 SSH signing key,
// writes the private key to a temp path, and returns the path along
// with the matching public key as a string.
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
	if _, err := os.Stat(filepath.Join(targetPath, ".allowed_signers")); err != nil {
		t.Fatalf("bootstrap should have written repo-local .allowed_signers: %v", err)
	}
	// HEAD should exist (birth commit).
	cmd := exec.CommandContext(t.Context(), "git", "-C", targetPath, "rev-parse", "--verify", "HEAD^{commit}")
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
	if _, err := os.Stat(filepath.Join(targetPath, ".allowed_signers")); err != nil {
		t.Fatalf("provenance init should have written .allowed_signers: %v", err)
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

	app := &App{
		logger: slog.Default(),
		cfg: &config.Config{
			DocRoots: map[string]config.DocumentRootConfig{
				"kb": {
					Git: config.DocumentRootGitConfig{
						Enabled:          true,
						SignCommits:      false, // verifier-only path
						VerifySignatures: "required",
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

func TestBuildDocumentStoreOptionsVerifierUsesRepoLocalAllowedSigners(t *testing.T) {
	targetPath := t.TempDir()
	signingKey, publicKey := writeTestSigningKey(t)

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
	resolver := paths.New(map[string]string{"kb": targetPath})
	opts, err := app.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver)
	if err != nil {
		t.Fatalf("initial buildDocumentStoreOptions: %v", err)
	}
	if opts.RootVerifiers["kb"] == nil {
		t.Fatal("initial verifier missing")
	}

	allowedPath := filepath.Join(targetPath, ".allowed_signers")
	data, err := os.ReadFile(allowedPath)
	if err != nil {
		t.Fatalf("ReadFile .allowed_signers: %v", err)
	}
	if !strings.Contains(string(data), publicKey) {
		t.Fatalf(".allowed_signers = %q, want generated public key", data)
	}

	cfgOut, err := exec.CommandContext(t.Context(), "git", "-C", targetPath, "config", "gpg.ssh.allowedSignersFile").Output()
	if err != nil {
		t.Fatalf("git config allowedSignersFile: %v", err)
	}
	if got := strings.TrimSpace(string(cfgOut)); got != allowedPath {
		t.Fatalf("allowedSignersFile = %q, want %q", got, allowedPath)
	}

	app.cfg.DocRoots["kb"] = config.DocumentRootConfig{
		Git: config.DocumentRootGitConfig{
			Enabled:          true,
			SignCommits:      false,
			VerifySignatures: "required",
		},
	}
	opts, err = app.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver)
	if err != nil {
		t.Fatalf("verifier-only buildDocumentStoreOptions: %v", err)
	}
	if opts.RootVerifiers["kb"] == nil {
		t.Fatal("verifier-only root should use repo-local .allowed_signers")
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

// TestApplyBootVerification locks in the boot round-trip's policy mapping: a
// required root fails to construct on a verification failure, a warn root logs
// and continues, verification-off is a no-op, and a passing check never errors.
func TestApplyBootVerification(t *testing.T) {
	sentinel := errors.New("verify HEAD failed")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	for _, tc := range []struct {
		name    string
		mode    documents.VerificationMode
		verErr  error
		wantErr bool
	}{
		{"required + failure fails construction", documents.VerificationRequired, sentinel, true},
		{"warn + failure continues", documents.VerificationWarn, sentinel, false},
		{"none + failure is a no-op", documents.VerificationNone, sentinel, false},
		{"required + success is a no-op", documents.VerificationRequired, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := applyBootVerification(tc.mode, "kb", tc.verErr, logger)
			if tc.wantErr {
				if err == nil {
					t.Fatal("applyBootVerification = nil, want error")
				}
				if !strings.Contains(err.Error(), "kb") {
					t.Fatalf("error %q should name the root", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyBootVerification = %v, want nil", err)
			}
		})
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

// TestDocumentRootProvenanceReviser exercises the app adapter that bridges a
// provenance.Reader to documents.RootReviser against a real signed repo.
func TestDocumentRootProvenanceReviser(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := provenance.NewSSHSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	store, err := provenance.New(dir, signer, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, step := range []struct{ content, msg string }{
		{"a\n", "first"}, {"a\nb\n", "second"}, {"a\nb\nc\n", "third"},
	} {
		if err := store.Write(t.Context(), "doc.md", step.content, step.msg); err != nil {
			t.Fatalf("write %q: %v", step.msg, err)
		}
	}

	reviser := &documentRootProvenanceReviser{reader: store, prefix: ""}
	ctx := t.Context()

	listing, err := reviser.History(ctx, "doc.md", documents.RevisionQuery{WithSigners: true})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if listing.Total != 3 || len(listing.Revisions) != 3 {
		t.Fatalf("history = %d/%d, want 3/3", listing.Total, len(listing.Revisions))
	}
	if listing.Revisions[0].Message != "third" || listing.Revisions[0].Index != 0 {
		t.Fatalf("newest = %q idx %d, want third idx 0", listing.Revisions[0].Message, listing.Revisions[0].Index)
	}
	if s := listing.Revisions[0].Signer; s == nil || !s.Verified || s.Kind != provenance.SignerKindAgent {
		t.Fatalf("newest signer = %+v, want verified agent", s)
	}

	head, err := reviser.Resolve(ctx, "doc.md", "HEAD")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	content, err := reviser.Content(ctx, "doc.md", head.Commit)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if content.Content != "a\nb\nc\n" {
		t.Fatalf("Content = %q, want full doc", content.Content)
	}
	if content.Revision.Signer == nil || !content.Revision.Signer.Verified {
		t.Fatalf("Content signer = %+v, want verified", content.Revision.Signer)
	}

	// Endpoints passed reversed to confirm the reviser time-orders base<target.
	first := listing.Revisions[2] // "first"
	diff, err := reviser.Diff(ctx, "doc.md", "HEAD", first.Short, "patch")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff.Base.Message != "first" || diff.Target.Message != "third" {
		t.Fatalf("diff base/target = %q/%q, want first/third", diff.Base.Message, diff.Target.Message)
	}
	if diff.Added != 2 || diff.Removed != 0 {
		t.Fatalf("diff counts = +%d/-%d, want +2/-0", diff.Added, diff.Removed)
	}
}

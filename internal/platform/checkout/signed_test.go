package checkout

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/identity"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

func writeSigningKey(t *testing.T, name string) (privPath, pub string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	pair, err := identity.GenerateSigningKeyPair(name)
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

func TestOpenSignedBootstrapsAndReconciles(t *testing.T) {
	worktree := t.TempDir()
	signingKey, _ := writeSigningKey(t, "checkout-test")
	_, operatorPublic := writeSigningKey(t, "operator-test")

	signed, err := OpenSigned(t.Context(), SignedSpec{
		Name:           "kb",
		WorktreePath:   worktree,
		SigningKeyPath: signingKey,
		TrustedSigners: []provenance.TrustedSigner{{
			Principal: "operator@example.com",
			PublicKey: operatorPublic,
			Comment:   "operator laptop",
		}},
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("OpenSigned: %v", err)
	}
	if signed.Store == nil {
		t.Fatal("Store missing")
	}
	if signed.Prefix != "" || signed.RepoPath != worktree {
		t.Fatalf("root = %+v, want repo root checkout", signed.Root)
	}
	if err := signed.VerifyHead(t.Context()); err != nil {
		t.Fatalf("VerifyHead: %v", err)
	}

	allowed, err := os.ReadFile(filepath.Join(worktree, ".allowed_signers"))
	if err != nil {
		t.Fatalf("read .allowed_signers: %v", err)
	}
	if !strings.Contains(string(allowed), operatorPublic) {
		t.Fatalf(".allowed_signers = %q, want operator key", allowed)
	}

	verified, err := OpenVerified(t.Context(), VerifySpec{
		Name:         "kb",
		WorktreePath: worktree,
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("OpenVerified: %v", err)
	}
	if verified.Verifier == nil {
		t.Fatal("Verifier missing")
	}
	if _, err := verified.Verifier.VerifyTree(t.Context(), ""); err != nil {
		t.Fatalf("VerifyTree: %v", err)
	}
}

func TestOpenSignedResolveRootErrorNamesCheckout(t *testing.T) {
	rootDir := t.TempDir()
	errRepo := filepath.Join(rootDir, "other")
	worktree := filepath.Join(rootDir, "repo", "kb")

	_, err := OpenSigned(t.Context(), SignedSpec{
		Name:           "kb",
		WorktreePath:   worktree,
		RepoPath:       errRepo,
		SigningKeyPath: filepath.Join(rootDir, "missing_key"),
	})
	if err == nil {
		t.Fatal("OpenSigned() error = nil, want root relationship error")
	}
	if !strings.Contains(err.Error(), "kb: resolve root") {
		t.Fatalf("error = %v, want checkout name context", err)
	}
}

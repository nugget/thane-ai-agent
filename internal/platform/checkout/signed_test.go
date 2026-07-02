package checkout

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"golang.org/x/crypto/ssh"
)

func writeSigningKey(t *testing.T, name string) (privPath, pub string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("encode signing public key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, name)
	if err != nil {
		t.Fatalf("marshal signing private key: %v", err)
	}
	dir := t.TempDir()
	privPath = filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(privPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write signing key: %v", err)
	}
	return privPath, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
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

func TestOpenSignedCanLeaveBirthCommitToCaller(t *testing.T) {
	worktree := t.TempDir()
	signingKey, _ := writeSigningKey(t, "checkout-test")

	signed, err := OpenSigned(t.Context(), SignedSpec{
		Name:            "core.identity",
		WorktreePath:    worktree,
		SigningKeyPath:  signingKey,
		SkipBirthCommit: true,
		Logger:          slog.Default(),
	})
	if err != nil {
		t.Fatalf("OpenSigned: %v", err)
	}
	if signed.Store == nil {
		t.Fatal("Store missing")
	}
	if _, err := os.Stat(filepath.Join(worktree, ".allowed_signers")); err != nil {
		t.Fatalf("expected .allowed_signers: %v", err)
	}

	cmd := exec.Command("git", "-C", worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err := cmd.Run(); err == nil {
		t.Fatal("HEAD exists after SkipBirthCommit, want caller-owned first commit")
	}
}

func TestOpenSignedRejectsDeferredBirthWithTrustedSigners(t *testing.T) {
	worktree := t.TempDir()
	signingKey, _ := writeSigningKey(t, "checkout-test")
	_, operatorPublic := writeSigningKey(t, "operator-test")

	_, err := OpenSigned(t.Context(), SignedSpec{
		Name:            "core.identity",
		WorktreePath:    worktree,
		SigningKeyPath:  signingKey,
		SkipBirthCommit: true,
		TrustedSigners: []provenance.TrustedSigner{{
			Principal: "operator@example.com",
			PublicKey: operatorPublic,
		}},
	})
	if err == nil {
		t.Fatal("OpenSigned() error = nil, want trusted signer guard")
	}
	if !strings.Contains(err.Error(), "trusted signers require a birth commit") {
		t.Fatalf("error = %v, want trusted signer guard", err)
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

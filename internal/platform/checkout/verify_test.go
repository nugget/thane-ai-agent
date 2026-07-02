package checkout

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureRepoLocalAllowedSignersBestEffortGitConfig(t *testing.T) {
	targetPath := t.TempDir()
	allowedPath := filepath.Join(targetPath, ".allowed_signers")
	if err := os.WriteFile(allowedPath, []byte("thane@example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForConfigOnly\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .allowed_signers: %v", err)
	}

	if err := ConfigureRepoLocalAllowedSigners(t.Context(), "kb", targetPath, slog.Default()); err != nil {
		t.Fatalf("ConfigureRepoLocalAllowedSigners() = %v, want nil despite non-git dir", err)
	}
}

func TestConfigureRepoLocalAllowedSignersRejectsNonRegularFile(t *testing.T) {
	targetPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(targetPath, ".allowed_signers"), 0o755); err != nil {
		t.Fatalf("Mkdir .allowed_signers: %v", err)
	}

	err := ConfigureRepoLocalAllowedSigners(t.Context(), "kb", targetPath, slog.Default())
	if err == nil {
		t.Fatal("ConfigureRepoLocalAllowedSigners() returned nil, want non-regular file error")
	}
	if !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("error = %v, want regular-file message", err)
	}
}

func TestOpenVerifiedResolveRootErrorNamesCheckout(t *testing.T) {
	rootDir := t.TempDir()
	errRepo := filepath.Join(rootDir, "other")
	worktree := filepath.Join(rootDir, "repo", "kb")

	_, err := OpenVerified(t.Context(), VerifySpec{
		Name:         "kb",
		WorktreePath: worktree,
		RepoPath:     errRepo,
	})
	if err == nil {
		t.Fatal("OpenVerified() error = nil, want root relationship error")
	}
	if !strings.Contains(err.Error(), "kb: resolve root") {
		t.Fatalf("error = %v, want checkout name context", err)
	}
}

package checkout

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

// VerifySpec describes an existing checkout opened only for verification and
// revision reads.
type VerifySpec struct {
	// Name is a caller-facing identifier used in logs and errors.
	Name string
	// WorktreePath is the local path exposed to the domain caller.
	WorktreePath string
	// RepoPath optionally points at the backing git repository. Empty means the
	// worktree path itself is the repository.
	RepoPath string
	// Logger receives setup logs. Nil uses slog.Default.
	Logger *slog.Logger
}

// Verified is a local checkout opened for verification and revision reads.
type Verified struct {
	Root

	// Name is the caller-facing checkout identifier used in logs and
	// verification reporting.
	Name string
	// Verifier is the read-side verification engine for this checkout —
	// signature checks and revision reads without write access.
	Verifier *provenance.Verifier
}

// OpenVerified opens an existing checkout for verification and revision reads.
// It never initializes the repository or writes commits. It may best-effort
// update repo-local git config so command-line verification uses the checkout's
// repo-local .allowed_signers file by default.
func OpenVerified(ctx context.Context, spec VerifySpec) (*Verified, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = "checkout"
	}
	if strings.TrimSpace(spec.WorktreePath) == "" {
		return nil, fmt.Errorf("%s: worktree path is required", name)
	}
	repoPath := strings.TrimSpace(spec.RepoPath)
	if repoPath == "" {
		repoPath = spec.WorktreePath
	}
	root, err := ResolveRoot(repoPath, spec.WorktreePath)
	if err != nil {
		return nil, fmt.Errorf("%s: resolve root: %w", name, err)
	}
	logger := spec.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if err := ConfigureRepoLocalAllowedSigners(ctx, name, root.RepoPath, logger); err != nil {
		return nil, err
	}
	verifier, err := provenance.NewVerifier(root.RepoPath, logger, provenance.Options{})
	if err != nil {
		return nil, fmt.Errorf("%s: initialize verifier: %w", name, err)
	}
	return &Verified{Name: name, Root: root, Verifier: verifier}, nil
}

// Reader returns the checkout's revision reader.
func (c *Verified) Reader() provenance.Reader {
	if c == nil {
		return nil
	}
	return c.Verifier
}

// ConfigureRepoLocalAllowedSigners validates the repo-local .allowed_signers
// file and best-effort configures git to use it for SSH signature verification.
func ConfigureRepoLocalAllowedSigners(ctx context.Context, name, repoPath string, logger *slog.Logger) error {
	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()

	if logger == nil {
		logger = slog.Default()
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "checkout"
	}
	allowedSignersPath := filepath.Join(repoPath, ".allowed_signers")
	if info, err := os.Lstat(allowedSignersPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: repo-local .allowed_signers is required at %s", name, allowedSignersPath)
		}
		return fmt.Errorf("%s: stat .allowed_signers: %w", name, err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s: .allowed_signers must be a regular file, not a symlink: %s", name, allowedSignersPath)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: .allowed_signers must be a regular file: %s", name, allowedSignersPath)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "config", "gpg.ssh.allowedSignersFile", allowedSignersPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		logger.Warn("checkout git config allowedSignersFile failed; verification will still use per-command configuration",
			"name", name,
			"repo", repoPath,
			"allowed_signers", allowedSignersPath,
			"error", strings.TrimSpace(fmt.Sprintf("%v: %s", err, out)),
		)
	}
	return nil
}

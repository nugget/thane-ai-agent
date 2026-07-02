package checkout

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

// DefaultBootstrapTimeout bounds checkout birth-commit and trust reconciliation
// work. It matches the provenance package's own git startup timeout.
const DefaultBootstrapTimeout = 30 * time.Second

// SignedSpec describes a checkout that can author signed commits.
type SignedSpec struct {
	// Name is a caller-facing identifier used in logs and errors.
	Name string
	// WorktreePath is the local path exposed to the domain caller.
	WorktreePath string
	// RepoPath optionally points at the backing git repository. Empty means the
	// worktree path itself is the repository.
	RepoPath string
	// SigningKeyPath is the SSH private key used for signed commits.
	SigningKeyPath string
	// TrustedSigners are operator signing keys added to the repo-local trust set.
	TrustedSigners []provenance.TrustedSigner
	// SkipBirthCommit leaves the first commit to the caller. Use this when the
	// domain needs its own birth commit contents; TrustedSigners must be empty
	// because trust reconciliation requires an existing signed HEAD.
	SkipBirthCommit bool
	// Logger receives setup logs. Nil uses slog.Default.
	Logger *slog.Logger
}

// Signed is a local checkout backed by a provenance store.
type Signed struct {
	Root

	Name  string
	Store *provenance.Store
}

// OpenSigned opens or initializes a signed checkout, creates a birth commit
// when needed, and reconciles its repo-local allowed_signers file.
func OpenSigned(ctx context.Context, spec SignedSpec) (*Signed, error) {
	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()

	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = "checkout"
	}
	if strings.TrimSpace(spec.WorktreePath) == "" {
		return nil, fmt.Errorf("%s: worktree path is required", name)
	}
	if spec.SkipBirthCommit && len(spec.TrustedSigners) > 0 {
		return nil, fmt.Errorf("%s: trusted signers require a birth commit before reconciliation", name)
	}
	signingKey := strings.TrimSpace(spec.SigningKeyPath)
	if signingKey == "" {
		return nil, fmt.Errorf("%s: signing key path is required", name)
	}
	repoPath := strings.TrimSpace(spec.RepoPath)
	if repoPath == "" {
		repoPath = spec.WorktreePath
	}
	root, err := ResolveRoot(repoPath, spec.WorktreePath)
	if err != nil {
		return nil, fmt.Errorf("%s: resolve root: %w", name, err)
	}

	signer, err := provenance.NewSSHFileSigner(signingKey)
	if err != nil {
		return nil, fmt.Errorf("%s: signing key: %w", name, err)
	}
	logger := spec.Logger
	if logger == nil {
		logger = slog.Default()
	}
	store, err := provenance.New(root.RepoPath, signer, logger)
	if err != nil {
		return nil, fmt.Errorf("%s: initialize provenance store: %w", name, err)
	}

	if !spec.SkipBirthCommit {
		if err := store.BootstrapBirthCommit(ctx); err != nil {
			return nil, fmt.Errorf("%s: bootstrap birth commit: %w", name, err)
		}
		if _, err := store.ReconcileAllowedSigners(ctx, spec.TrustedSigners); err != nil {
			return nil, fmt.Errorf("%s: reconcile allowed_signers: %w", name, err)
		}
	}

	logger.Info("signed checkout enabled",
		"name", name,
		"repo", store.Path(),
		"worktree", root.WorktreePath,
		"prefix", root.Prefix,
	)
	return &Signed{Name: name, Root: root, Store: store}, nil
}

func withDefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, DefaultBootstrapTimeout)
}

// VerifyHead confirms that the checkout HEAD verifies against its trust set.
func (c *Signed) VerifyHead(ctx context.Context) error {
	if c == nil || c.Store == nil {
		return fmt.Errorf("signed checkout is not configured")
	}
	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()
	return c.Store.VerifyHead(ctx)
}

// Reader returns the checkout's revision reader.
func (c *Signed) Reader() provenance.Reader {
	if c == nil {
		return nil
	}
	return c.Store
}

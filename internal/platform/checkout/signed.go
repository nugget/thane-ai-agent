package checkout

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

// DefaultBootstrapTimeout bounds checkout birth-commit and trust seeding
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
	// SeedSigners establish this root's trust set at birth. They are not
	// re-applied afterwards: the root's own .allowed_signers is its record
	// of whom it trusts from then on.
	SeedSigners []provenance.TrustedSigner
	// SkipBirthCommit leaves the first commit to the caller. Use this when
	// the domain needs its own birth commit contents; SeedSigners must be
	// empty, since those contents include the trust file and would
	// overwrite the seed.
	SkipBirthCommit bool
	// Logger receives setup logs. Nil uses slog.Default.
	Logger *slog.Logger
}

// Signed is a local checkout backed by a provenance store.
type Signed struct {
	Root

	// Name is the caller-facing checkout identifier used in logs and
	// sync-state reporting.
	Name string
	// Store is the provenance engine for this checkout — the write,
	// sync, and history surface callers use once the checkout is open.
	Store *provenance.Store

	// seedSigners is the declared set retained for admission. It is kept
	// rather than re-derived because admission must ask config, not the
	// repository, whose signatures may establish this root.
	seedSigners []provenance.TrustedSigner
}

// OpenSigned opens or initializes a signed checkout, creates a birth commit
// when needed, and seeds its repo-local allowed_signers file on first
// establishment.
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
	if spec.SkipBirthCommit && len(spec.SeedSigners) > 0 {
		// A caller that owns the birth contents writes its own
		// .allowed_signers, which would overwrite the seeded one without
		// saying so. Refuse the combination rather than discard a trust
		// set the caller believed they had declared.
		return nil, fmt.Errorf("%s: seed signers cannot be combined with SkipBirthCommit, because the caller's own birth contents overwrite the seeded trust set", name)
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
	store, err := provenance.NewWithOptions(root.RepoPath, signer, logger, provenance.Options{
		SeedSigners: spec.SeedSigners,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: initialize provenance store: %w", name, err)
	}

	if !spec.SkipBirthCommit {
		if err := store.BootstrapBirthCommit(ctx); err != nil {
			return nil, fmt.Errorf("%s: bootstrap birth commit: %w", name, err)
		}
	}

	logger.Info("signed checkout enabled",
		"name", name,
		"repo", store.Path(),
		"worktree", root.WorktreePath,
		"prefix", root.Prefix,
	)
	return &Signed{Name: name, Root: root, Store: store, seedSigners: spec.SeedSigners}, nil
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

// VerifyAdmission confirms the checkout's history is admitted by its declared
// seed signers: founded by one, and with its trust file only ever changed by
// one. It answers a different question from [Signed.VerifyHead], which asks
// whether the current tip is signed by someone the root already trusts —
// a check the root's own history gets to define the terms of.
//
// A checkout with no declared seed signers is not admitted and not refused:
// admission has nothing to check against, so the caller's signature policy is
// left to decide what an unseeded signed root means.
func (c *Signed) VerifyAdmission(ctx context.Context) error {
	if c == nil || c.Store == nil {
		return fmt.Errorf("signed checkout is not configured")
	}
	if len(c.seedSigners) == 0 {
		return nil
	}
	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()
	_, err := provenance.VerifyAdmission(ctx, c.Store.Path(), c.seedSigners)
	return err
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

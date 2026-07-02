package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

type documentRootProvenanceWriter struct {
	store  *provenance.Store
	prefix string
}

type documentRootProvenanceVerifier struct {
	verifier *provenance.Verifier
	prefix   string
}

func (w *documentRootProvenanceWriter) Write(ctx context.Context, filename, content, message string) error {
	return w.store.Write(ctx, w.storeFilename(filename), content, message)
}

func (w *documentRootProvenanceWriter) Delete(ctx context.Context, filename, message string) error {
	return w.store.Delete(ctx, w.storeFilename(filename), message)
}

func (w *documentRootProvenanceWriter) storeFilename(filename string) string {
	return checkout.RepoFilename(w.prefix, filename)
}

func (v *documentRootProvenanceVerifier) Verify(ctx context.Context, filename string) (documents.SignatureVerification, error) {
	result, err := v.verifier.VerifyFile(ctx, v.storeFilename(filename))
	return documentSignatureVerificationFromProvenance(result), err
}

func (v *documentRootProvenanceVerifier) VerifyRoot(ctx context.Context) (documents.SignatureVerification, error) {
	result, err := v.verifier.VerifyTree(ctx, v.prefix)
	return documentSignatureVerificationFromProvenance(result), err
}

func (v *documentRootProvenanceVerifier) storeFilename(filename string) string {
	return checkout.RepoFilename(v.prefix, filename)
}

func documentSignatureVerificationFromProvenance(result provenance.VerificationResult) documents.SignatureVerification {
	status := documents.SignatureFailed
	if result.Status == provenance.VerificationTrusted {
		status = documents.SignatureTrusted
	}
	return documents.SignatureVerification{
		Status:  status,
		Commit:  result.Commit,
		Message: result.Message,
	}
}

func buildDocumentRoots(resolver *paths.Resolver) map[string]string {
	if resolver == nil {
		return nil
	}
	documentRoots := make(map[string]string)
	for _, root := range resolver.Prefixes() {
		rootPath, err := resolver.Resolve(root + ":")
		if err != nil {
			continue
		}
		info, err := os.Stat(rootPath)
		if err != nil || !info.IsDir() {
			continue
		}
		absPath, err := filepath.Abs(rootPath)
		if err != nil {
			continue
		}
		documentRoots[root] = absPath
	}
	if len(documentRoots) == 0 {
		return nil
	}
	return documentRoots
}

func (a *App) buildDocumentStoreOptions(documentRoots map[string]string, resolver *paths.Resolver) (documents.StoreOptions, error) {
	if a == nil || a.cfg == nil {
		return documents.StoreOptions{}, nil
	}
	opts := documents.StoreOptions{
		RootPolicies: make(map[string]documents.RootPolicy, len(a.cfg.DocRoots)),
	}
	if len(a.cfg.DocRoots) == 0 {
		return opts, nil
	}
	if documentRoots == nil {
		// The loop below mutates this map when bootstrapping a
		// missing directory; ensure it is non-nil so we don't
		// panic on assignment.
		documentRoots = make(map[string]string)
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	for root, rootCfg := range a.cfg.DocRoots {
		root = strings.TrimSuffix(strings.TrimSpace(root), ":")
		if root == "" {
			continue
		}
		policy := documentRootPolicyFromConfig(rootCfg)

		rootPath, ok := documentRoots[root]
		if !ok {
			// A git-managed signing root is allowed to bootstrap
			// from a missing directory — we will mkdir, git init,
			// and birth-commit below. Non-bootstrap configs still
			// require the directory to already exist.
			if !policy.Git.Enabled || !policy.Git.SignCommits {
				return documents.StoreOptions{}, fmt.Errorf("doc_roots.%s references a document root that is not configured in paths or does not exist on disk", root)
			}
			created, err := bootstrapMissingRootDirectory(root, resolver, logger)
			if err != nil {
				return documents.StoreOptions{}, err
			}
			documentRoots[root] = created
			rootPath = created
		}

		opts.RootPolicies[root] = policy

		// Construct the writer first when this root signs commits.
		// provenance.NewWithOptions runs ensureRepo (mkdir + git
		// init + .allowed_signers); BootstrapBirthCommit then makes
		// HEAD exist if the repo was empty. Doing this before the
		// verifier means the verifier always sees a fully prepared
		// repo and never silently no-ops because the repo wasn't
		// ready yet.
		var writer *documentRootProvenanceWriter
		if policy.Git.Enabled && policy.Git.SignCommits {
			w, err := a.newDocumentRootProvenanceWriter(root, rootPath, rootCfg.Git, resolver)
			if err != nil {
				return documents.StoreOptions{}, err
			}
			writer = w
		}

		var verifier *documentRootProvenanceVerifier
		if policy.Git.Enabled && policy.Git.VerifySignatures != documents.VerificationNone {
			v, err := a.newDocumentRootProvenanceVerifier(root, rootPath, rootCfg.Git, resolver)
			if err != nil {
				if policy.Git.VerifySignatures == documents.VerificationRequired {
					return documents.StoreOptions{}, fmt.Errorf("doc_roots.%s verify_signatures=required but verifier unavailable: %w", root, err)
				}
				logger.Warn("document root signature verifier unavailable",
					"root", root,
					"mode", policy.Git.VerifySignatures,
					"error", err,
				)
			} else {
				verifier = v
				if opts.RootVerifiers == nil {
					opts.RootVerifiers = make(map[string]documents.RootVerifier)
				}
				opts.RootVerifiers[root] = verifier
			}
		}

		if writer != nil {
			if opts.RootWriters == nil {
				opts.RootWriters = make(map[string]documents.RootWriter)
			}
			opts.RootWriters[root] = writer
		}

		// Expose revision history for any git-backed root: prefer the signing
		// store, otherwise the verify-only verifier. Both satisfy
		// provenance.Reader, so a required verify-only root is inspectable too.
		var reviser *documentRootProvenanceReviser
		switch {
		case writer != nil:
			reviser = &documentRootProvenanceReviser{reader: writer.store, prefix: writer.prefix}
		case verifier != nil:
			reviser = &documentRootProvenanceReviser{reader: verifier.verifier, prefix: verifier.prefix}
		}
		if reviser != nil {
			if opts.RootRevisers == nil {
				opts.RootRevisers = make(map[string]documents.RootReviser)
			}
			opts.RootRevisers[root] = reviser
		}

		// A remote-backed root gets a syncer driving the fast-forward-only
		// engine on the writer's store — the same store the writer and reviser
		// use, so its lock serializes sync against local writes. Sync needs the
		// signing store, so a remote requires sign_commits.
		if rootCfg.Git.Remote != nil {
			if writer == nil {
				return documents.StoreOptions{}, fmt.Errorf("doc_roots.%s.git.remote requires sign_commits (the sync engine needs the signing store)", root)
			}
			if a.syncRegistry == nil {
				a.syncRegistry = newSyncStateRegistry()
			}
			resolve := func(p string) string { return resolvePath(p, resolver) }
			syncer, err := buildDocRootSyncer(root, rootCfg.Git, writer.store, a.syncRegistry, resolve, logger)
			if err != nil {
				return documents.StoreOptions{}, fmt.Errorf("doc_roots.%s.git.remote: %w", root, err)
			}
			syncer.notifyTransition = a.docRootSyncAttentionNotifier()
			a.docRootSyncers = append(a.docRootSyncers, syncer)
		}
	}
	return opts, nil
}

// bootstrapMissingRootDirectory creates the directory for a git-managed
// document root that was declared in doc_roots: but has no entry in
// paths or does not exist on disk. Returns the absolute path. Only
// callers that are about to construct a signing writer should use this
// — for non-bootstrap roots the existing "does not exist on disk" error
// is preserved.
func bootstrapMissingRootDirectory(root string, resolver *paths.Resolver, logger *slog.Logger) (string, error) {
	if resolver == nil {
		return "", fmt.Errorf("doc_roots.%s has no path configured (paths: missing entry for %q)", root, root)
	}
	resolved, err := resolver.Resolve(root + ":")
	if err != nil {
		return "", fmt.Errorf("doc_roots.%s: %w", root, err)
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return "", fmt.Errorf("doc_roots.%s create directory: %w", root, err)
	}
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("doc_roots.%s resolve absolute path: %w", root, err)
	}
	logger.Info("bootstrapping new document root", "root", root, "path", absPath)
	return absPath, nil
}

func documentRootPolicyFromConfig(rootCfg config.DocumentRootConfig) documents.RootPolicy {
	policy := documents.RootPolicy{
		Indexing:  true,
		Authoring: documents.AuthoringManaged,
		Git: documents.RootGitPolicy{
			VerifySignatures: documents.VerificationNone,
		},
	}
	if rootCfg.Indexing != nil {
		policy.Indexing = *rootCfg.Indexing
	}
	if authoring := strings.TrimSpace(rootCfg.Authoring); authoring != "" {
		policy.Authoring = documents.AuthoringMode(authoring)
	}
	gitCfg := rootCfg.Git
	policy.Git.Enabled = gitCfg.Enabled
	policy.Git.SignCommits = gitCfg.SignCommits
	if verify := strings.TrimSpace(gitCfg.VerifySignatures); verify != "" {
		policy.Git.VerifySignatures = documents.VerificationMode(verify)
	}
	policy.Git.RepoPath = strings.TrimSpace(gitCfg.RepoPath)
	return policy
}

func (a *App) newDocumentRootProvenanceWriter(root, rootPath string, gitCfg config.DocumentRootGitConfig, resolver *paths.Resolver) (*documentRootProvenanceWriter, error) {
	signingKey := strings.TrimSpace(gitCfg.SigningKey)
	if signingKey == "" {
		return nil, fmt.Errorf("doc_roots.%s.git.signing_key is required for signed document root commits", root)
	}
	signingKey = resolvePath(signingKey, resolver)

	repoPath := strings.TrimSpace(gitCfg.RepoPath)
	if repoPath == "" {
		repoPath = rootPath
	} else {
		repoPath = resolvePath(repoPath, resolver)
	}
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("resolve doc_roots.%s.git.repo_path: %w", root, err)
	}
	absRootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve document root %s path: %w", root, err)
	}
	if _, err := checkout.ResolveRoot(absRepoPath, absRootPath); err != nil {
		return nil, fmt.Errorf("doc_roots.%s.git.repo_path: %w", root, err)
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	signed, err := checkout.OpenSigned(context.Background(), checkout.SignedSpec{
		Name:           "doc_roots." + root + ".git",
		WorktreePath:   absRootPath,
		RepoPath:       absRepoPath,
		SigningKeyPath: signingKey,
		TrustedSigners: buildTrustedSigners(a.cfg.Signing.AllowedSigners, gitCfg.AllowedSigners),
		Logger:         logger.With("component", "document_root_provenance", "root", root),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize git provenance for document root %s: %w", root, err)
	}

	// Boot-time round-trip: confirm HEAD actually verifies against the trust
	// file we just rendered, so a malformed signer line or an OpenSSH version
	// that can't parse a rendered option fails loudly now instead of silently
	// blocking reads later. Only worth running where verification is actually
	// consumed; the policy mapping (fail vs. warn) lives in applyBootVerification.
	mode := documents.VerificationMode(strings.TrimSpace(gitCfg.VerifySignatures))
	switch mode {
	case documents.VerificationRequired, documents.VerificationWarn:
		if err := applyBootVerification(mode, root, signed.VerifyHead(context.Background()), logger); err != nil {
			return nil, err
		}
	}

	logger.Info("document root provenance enabled",
		"root", root,
		"repo", signed.Store.Path(),
		"prefix", signed.Prefix,
	)
	return &documentRootProvenanceWriter{store: signed.Store, prefix: signed.Prefix}, nil
}

func (a *App) newDocumentRootProvenanceVerifier(root, rootPath string, gitCfg config.DocumentRootGitConfig, resolver *paths.Resolver) (*documentRootProvenanceVerifier, error) {
	repoPath := strings.TrimSpace(gitCfg.RepoPath)
	if repoPath == "" {
		repoPath = rootPath
	} else {
		repoPath = resolvePath(repoPath, resolver)
	}
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("resolve doc_roots.%s.git.repo_path: %w", root, err)
	}
	absRootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve document root %s path: %w", root, err)
	}
	if _, err := checkout.ResolveRoot(absRepoPath, absRootPath); err != nil {
		return nil, fmt.Errorf("doc_roots.%s.git.repo_path: %w", root, err)
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	verified, err := checkout.OpenVerified(context.Background(), checkout.VerifySpec{
		Name:         "doc_roots." + root + ".git",
		WorktreePath: absRootPath,
		RepoPath:     absRepoPath,
		Logger:       logger.With("component", "document_root_verifier", "root", root),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize git verifier for document root %s: %w", root, err)
	}
	return &documentRootProvenanceVerifier{verifier: verified.Verifier, prefix: verified.Prefix}, nil
}

// applyBootVerification maps a boot-time VerifyHead result onto the root's
// verification policy: a required root fails to construct, a warn root logs and
// continues, and any other mode is a no-op. A nil verifyErr is always a no-op,
// so callers can pass the VerifyHead result directly.
func applyBootVerification(mode documents.VerificationMode, root string, verifyErr error, logger *slog.Logger) error {
	if verifyErr == nil {
		return nil
	}
	switch mode {
	case documents.VerificationRequired:
		return fmt.Errorf("doc_roots.%s allowed_signers boot verification: %w", root, verifyErr)
	case documents.VerificationWarn:
		logger.Warn("document root allowed_signers boot verification failed",
			"root", root, "error", verifyErr)
		return nil
	default:
		return nil
	}
}

// buildTrustedSigners flattens the shared and per-root operator allowed-signer
// config into provenance.TrustedSigner values for rendering. Order does not
// matter — the renderer canonicalizes, deduplicates, and sorts — so the two
// lists are simply concatenated (shared first).
func buildTrustedSigners(shared, perRoot []config.AllowedSigner) []provenance.TrustedSigner {
	out := make([]provenance.TrustedSigner, 0, len(shared)+len(perRoot))
	for _, list := range [][]config.AllowedSigner{shared, perRoot} {
		for _, s := range list {
			out = append(out, provenance.TrustedSigner{
				Principal:   s.Principal,
				PublicKey:   s.Key,
				Comment:     s.Label,
				ValidAfter:  s.ValidAfter,
				ValidBefore: s.ValidBefore,
			})
		}
	}
	return out
}

func sortedDocumentRootNames(documentRoots map[string]string) []string {
	roots := make([]string, 0, len(documentRoots))
	for root := range documentRoots {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func documentRootPolicyAttrs(opts documents.StoreOptions, roots []string) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(roots))
	for _, root := range roots {
		policy, ok := opts.RootPolicies[root]
		if !ok {
			continue
		}
		attrs = append(attrs, slog.Group(root,
			slog.Bool("indexing", policy.Indexing),
			slog.String("authoring", string(policy.Authoring)),
			slog.Bool("git", policy.Git.Enabled),
			slog.Bool("sign_commits", policy.Git.SignCommits),
			slog.String("verify_signatures", string(policy.Git.VerifySignatures)),
		))
	}
	return attrs
}

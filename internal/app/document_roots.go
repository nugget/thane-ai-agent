package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// docRootBootstrapTimeout bounds the per-root birth-commit work
// (stat/write .gitignore, stage, sign commit). Matches the
// 30s ceiling provenance uses for its own startup git operations
// so the budget stays consistent across the boot path.
const docRootBootstrapTimeout = 30 * time.Second

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
	clean := filepath.ToSlash(filepath.Clean(filename))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return clean
	}
	if w.prefix == "" || w.prefix == "." {
		return clean
	}
	return path.Join(w.prefix, clean)
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
	clean := filepath.ToSlash(filepath.Clean(filename))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return clean
	}
	if v.prefix == "" || v.prefix == "." {
		return clean
	}
	return path.Join(v.prefix, clean)
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

		if policy.Git.Enabled && policy.Git.VerifySignatures != documents.VerificationNone {
			verifier, err := a.newDocumentRootProvenanceVerifier(root, rootPath, rootCfg.Git, resolver)
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
	policy.Git.AllowedSigners = strings.TrimSpace(gitCfg.AllowedSigners)
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
	prefix, err := rootPrefixWithinRepo(absRepoPath, absRootPath)
	if err != nil {
		return nil, fmt.Errorf("doc_roots.%s.git.repo_path: %w", root, err)
	}

	allowedSigners := strings.TrimSpace(gitCfg.AllowedSigners)
	if allowedSigners != "" {
		allowedSigners = resolvePath(allowedSigners, resolver)
	}
	signer, err := provenance.NewSSHFileSigner(signingKey)
	if err != nil {
		return nil, fmt.Errorf("doc_roots.%s.git.signing_key: %w", root, err)
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	store, err := provenance.NewWithOptions(absRepoPath, signer, logger.With("component", "document_root_provenance", "root", root), provenance.Options{
		AllowedSignersPath: allowedSigners,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize git provenance for document root %s: %w", root, err)
	}

	// Make sure HEAD exists before any verifier construction. No-op
	// when the repo already has commits; for a fresh repo this
	// commits a templated .gitignore plus the repo-local
	// .allowed_signers (when present) so verification has signed
	// history to verify against.
	bootstrapCtx, cancel := context.WithTimeout(context.Background(), docRootBootstrapTimeout)
	defer cancel()
	if err := store.BootstrapBirthCommit(bootstrapCtx); err != nil {
		return nil, fmt.Errorf("doc_roots.%s bootstrap birth commit: %w", root, err)
	}

	logger.Info("document root provenance enabled",
		"root", root,
		"repo", store.Path(),
		"prefix", prefix,
		"allowed_signers", allowedSigners != "",
	)
	return &documentRootProvenanceWriter{store: store, prefix: prefix}, nil
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
	prefix, err := rootPrefixWithinRepo(absRepoPath, absRootPath)
	if err != nil {
		return nil, fmt.Errorf("doc_roots.%s.git.repo_path: %w", root, err)
	}

	allowedSigners := strings.TrimSpace(gitCfg.AllowedSigners)
	if allowedSigners != "" {
		allowedSigners = resolvePath(allowedSigners, resolver)
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	verifier, err := provenance.NewVerifier(absRepoPath, logger.With("component", "document_root_verifier", "root", root), provenance.Options{
		AllowedSignersPath: allowedSigners,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize git verifier for document root %s: %w", root, err)
	}
	return &documentRootProvenanceVerifier{verifier: verifier, prefix: prefix}, nil
}

func rootPrefixWithinRepo(repoPath, rootPath string) (string, error) {
	rel, err := filepath.Rel(repoPath, rootPath)
	if err != nil {
		return "", fmt.Errorf("compare repository and root paths: %w", err)
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return "", nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repository %s must be the root path %s or one of its parents", repoPath, rootPath)
	}
	return filepath.ToSlash(rel), nil
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

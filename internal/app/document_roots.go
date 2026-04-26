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

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

type documentRootProvenanceWriter struct {
	store  *provenance.Store
	prefix string
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
	if len(documentRoots) == 0 || a == nil || a.cfg == nil {
		return documents.StoreOptions{}, nil
	}
	opts := documents.StoreOptions{
		RootPolicies: make(map[string]documents.RootPolicy, len(a.cfg.DocRoots)),
	}
	if len(a.cfg.DocRoots) == 0 {
		return opts, nil
	}
	for root, rootCfg := range a.cfg.DocRoots {
		root = strings.TrimSuffix(strings.TrimSpace(root), ":")
		if root == "" {
			continue
		}
		rootPath, ok := documentRoots[root]
		if !ok {
			return documents.StoreOptions{}, fmt.Errorf("doc_roots.%s references a document root that is not configured in paths or does not exist on disk", root)
		}
		policy := documentRootPolicyFromConfig(rootCfg)
		opts.RootPolicies[root] = policy
		if !policy.Git.Enabled || !policy.Git.SignCommits {
			continue
		}
		writer, err := a.newDocumentRootProvenanceWriter(root, rootPath, rootCfg.Git, resolver)
		if err != nil {
			return documents.StoreOptions{}, err
		}
		if opts.RootWriters == nil {
			opts.RootWriters = make(map[string]documents.RootWriter)
		}
		opts.RootWriters[root] = writer
	}
	return opts, nil
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
	logger.Info("document root provenance enabled",
		"root", root,
		"repo", store.Path(),
		"prefix", prefix,
		"allowed_signers", allowedSigners != "",
	)
	return &documentRootProvenanceWriter{store: store, prefix: prefix}, nil
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

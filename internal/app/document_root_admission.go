package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// resolveRootPaths resolves a document root's worktree and its backing
// repository to absolute paths, and confirms the repository actually contains
// the worktree.
//
// It lives beside admission because admission made it the third caller. The
// writer, the verifier, and admission must all name the same repository, and
// three copies of this were three chances for one of them to judge a different
// one than the others.
func resolveRootPaths(root, rootPath string, gitCfg config.DocumentRootGitConfig, resolver *paths.Resolver) (repo, worktree string, err error) {
	repoPath := strings.TrimSpace(gitCfg.RepoPath)
	if repoPath == "" {
		repoPath = rootPath
	} else {
		repoPath = resolvePath(repoPath, resolver)
	}
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve doc_roots.%s.git.repo_path: %w", root, err)
	}
	absRootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve document root %s path: %w", root, err)
	}
	if _, err := checkout.ResolveRoot(absRepoPath, absRootPath); err != nil {
		return "", "", fmt.Errorf("doc_roots.%s.git.repo_path: %w", root, err)
	}
	return absRepoPath, absRootPath, nil
}

// verifyRootAdmission checks that a git-backed root's history is admitted by
// the seed signers config declares for it, then maps the outcome onto the
// root's own verification policy — the same mapping the other boot checks use,
// so a root has one answer to "how strict is this".
//
// A root declaring no seed signers is neither admitted nor refused: there is
// nothing to check against. Config already refuses the combination where that
// silence would matter, a root that signs commits while naming no one entitled
// to establish it.
func (a *App) verifyRootAdmission(root, rootPath string, rootCfg config.DocumentRootConfig, mode documents.VerificationMode, resolver *paths.Resolver, logger *slog.Logger) error {
	if !rootCfg.Git.Enabled {
		return nil
	}
	switch mode {
	case documents.VerificationRequired, documents.VerificationWarn:
	default:
		return nil
	}
	seeds := buildTrustedSigners(rootCfg.SeedSigners, rootCfg.Git.AllowedSigners)
	if len(seeds) == 0 {
		return nil
	}
	repoPath, _, err := resolveRootPaths(root, rootPath, rootCfg.Git, resolver)
	if err != nil {
		return err
	}
	_, admitErr := provenance.VerifyAdmission(context.Background(), repoPath, seeds)
	return applyBootVerification(mode, root, "admission", admitErr, logger)
}

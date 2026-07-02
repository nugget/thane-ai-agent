package checkout

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Root describes how a managed subtree maps onto its backing git repository.
type Root struct {
	// RepoPath is the absolute path to the git repository.
	RepoPath string
	// WorktreePath is the absolute path to the managed working-tree subtree.
	WorktreePath string
	// Prefix is the slash-separated path from RepoPath to WorktreePath. It is
	// empty when the managed root is the repository root.
	Prefix string
}

// ResolveRoot resolves repoPath and worktreePath to absolute paths and verifies
// that the worktree is the repository root or a subtree inside it.
func ResolveRoot(repoPath, worktreePath string) (Root, error) {
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return Root{}, fmt.Errorf("resolve repository path: %w", err)
	}
	absWorktreePath, err := filepath.Abs(worktreePath)
	if err != nil {
		return Root{}, fmt.Errorf("resolve worktree path: %w", err)
	}
	prefix, err := prefixWithinRepo(absRepoPath, absWorktreePath)
	if err != nil {
		return Root{}, err
	}
	return Root{
		RepoPath:     absRepoPath,
		WorktreePath: absWorktreePath,
		Prefix:       prefix,
	}, nil
}

// RepoFilename maps a worktree-relative filename to the repository-relative
// path for this checkout. Escaping names are left for the git/provenance layer
// to reject so callers do not accidentally clean "../" into the prefix.
func (r Root) RepoFilename(filename string) string {
	return RepoFilename(r.Prefix, filename)
}

// RepoFilename maps a worktree-relative filename under prefix to a
// repository-relative path.
func RepoFilename(prefix, filename string) string {
	clean := filepath.ToSlash(filepath.Clean(filename))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return clean
	}
	if prefix == "" || prefix == "." {
		return clean
	}
	return path.Join(prefix, clean)
}

func prefixWithinRepo(repoPath, worktreePath string) (string, error) {
	rel, err := filepath.Rel(repoPath, worktreePath)
	if err != nil {
		return "", fmt.Errorf("compare repository and worktree paths: %w", err)
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return "", nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repository %s must be the worktree path %s or one of its parents", repoPath, worktreePath)
	}
	return filepath.ToSlash(rel), nil
}

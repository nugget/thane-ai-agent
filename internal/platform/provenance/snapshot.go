package provenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Snapshot returns the current worktree content paired with its latest file
// revision while holding the store's write lock.
func (s *Store) Snapshot(ctx context.Context, filename string) (Revision, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readSnapshot(ctx, s.path, filename)
}

// Snapshot returns the current worktree content paired with its latest file
// revision while holding the verifier's read lock.
func (v *Verifier) Snapshot(ctx context.Context, filename string) (Revision, string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return readSnapshot(ctx, v.path, filename)
}

func readSnapshot(ctx context.Context, repoPath, filename string) (Revision, string, error) {
	if err := validateReaderFilename(filename); err != nil {
		return Revision{}, "", fmt.Errorf("read snapshot: %w", err)
	}
	content, err := os.ReadFile(filepath.Join(repoPath, filename))
	if err != nil {
		return Revision{}, "", fmt.Errorf("read snapshot of %s: %w", filename, err)
	}
	hasHead, err := repositoryHasHead(ctx, repoPath)
	if err != nil {
		return Revision{}, "", err
	}
	if !hasHead {
		return Revision{}, string(content), nil
	}
	commit, err := runGitText(ctx, repoPath, "rev-list", "-1", "--end-of-options", "HEAD", "--", filename)
	if err != nil {
		return Revision{}, "", fmt.Errorf("resolve snapshot revision: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return Revision{}, string(content), nil
	}
	revision, err := describeRevision(ctx, repoPath, filename, commit, "HEAD")
	if err != nil {
		return Revision{}, "", err
	}
	return revision, string(content), nil
}

func repositoryHasHead(ctx context.Context, repoPath string) (bool, error) {
	if _, err := runGitText(ctx, repoPath, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

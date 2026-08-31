package provenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RevisionAbsent is the expected-revision token for a conditional write that
// must create a file rather than replace one. It is deliberately a word, not
// an empty string: empty retains the existing unconditional-write contract.
const RevisionAbsent = "absent"

// RevisionConflictError reports that a conditional write was based on a
// revision that is no longer current. Expected is the caller's token; Actual
// is the newest commit touching the file, RevisionAbsent when the file has no
// history, or "worktree_dirty" when uncommitted bytes make a comparison
// unsafe.
type RevisionConflictError struct {
	// Filename is the repository-relative file whose precondition failed.
	Filename string
	// Expected is the revision token supplied by the caller.
	Expected string
	// Actual is the current token or a named state such as worktree_dirty.
	Actual string
}

type conditionalBase struct {
	current Revision
	head    string
}

// Error implements error.
func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("revision conflict for %s: expected %s, current is %s", e.Filename, e.Expected, e.Actual)
}

// WriteIfRevision writes and commits content only when filename is still at
// expectedRevision. The comparison and commit share the store mutex, and the
// HEAD update itself compares the parent, so concurrent managed writes and git
// ref updates cannot land between them. An already-dirty target is rejected.
// Editors that ignore Thane's coordination can still race in the narrow window
// between the final worktree check and replacement; callers must not interpret
// this as a general filesystem lock for arbitrary external writers.
// Use [RevisionAbsent] to require creation of a file with no current history.
// The returned revision is the exact commit produced by this call (or the
// existing current revision when the content was already identical).
func (s *Store) WriteIfRevision(ctx context.Context, filename, content, message, expectedRevision string) (Revision, error) {
	if err := validateFilename(filename); err != nil {
		return Revision{}, fmt.Errorf("provenance: %w", err)
	}
	expectedRevision = strings.TrimSpace(expectedRevision)
	if expectedRevision == "" {
		return Revision{}, fmt.Errorf("provenance: expected revision is required for conditional write")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	base, err := s.checkExpectedRevisionLocked(ctx, filename, expectedRevision)
	if err != nil {
		return Revision{}, err
	}
	commitHash, err := s.commitFileContentAtHead(ctx, filename, []byte(content), message, base.head)
	if err != nil {
		return Revision{}, fmt.Errorf("provenance: commit %s: %w", filename, err)
	}
	if commitHash != "" {
		s.logger.Info("provenance file committed",
			"file", filename,
			"bytes", len(content),
			"message", messageSubject(message),
		)
		return Revision{Commit: commitHash, Short: shorten(commitHash)}, nil
	}
	return base.current, nil
}

// checkExpectedRevisionLocked compares one file's latest committed revision
// while the caller holds s.mu. A dirty target is always a conflict: resolving
// only committed history and then overwriting uncommitted operator bytes would
// satisfy the hash comparison while violating its purpose.
func (s *Store) checkExpectedRevisionLocked(ctx context.Context, filename, expectedRevision string) (conditionalBase, error) {
	head, err := repositoryHead(ctx, s.path)
	if err != nil {
		return conditionalBase{}, fmt.Errorf("provenance: resolve repository head: %w", err)
	}
	dirty, err := s.fileDirtyAtHead(ctx, filename, head)
	if err != nil {
		return conditionalBase{}, fmt.Errorf("provenance: inspect worktree state for %s: %w", filename, err)
	}
	if dirty {
		return conditionalBase{}, &RevisionConflictError{Filename: filename, Expected: expectedRevision, Actual: "worktree_dirty"}
	}

	current, hasCurrent, err := s.currentFileRevisionAtHead(ctx, filename, head)
	if err != nil {
		return conditionalBase{}, fmt.Errorf("provenance: resolve current revision for %s: %w", filename, err)
	}
	base := conditionalBase{current: current, head: head}
	if strings.EqualFold(expectedRevision, RevisionAbsent) {
		if hasCurrent {
			return conditionalBase{}, &RevisionConflictError{Filename: filename, Expected: RevisionAbsent, Actual: current.Short}
		}
		if _, statErr := os.Stat(filepath.Join(s.path, filename)); statErr == nil {
			return conditionalBase{}, &RevisionConflictError{Filename: filename, Expected: RevisionAbsent, Actual: "worktree_dirty"}
		} else if !os.IsNotExist(statErr) {
			return conditionalBase{}, fmt.Errorf("provenance: stat %s: %w", filename, statErr)
		}
		return base, nil
	}
	if !hasCurrent {
		return conditionalBase{}, &RevisionConflictError{Filename: filename, Expected: expectedRevision, Actual: RevisionAbsent}
	}

	expected, err := resolveRevision(ctx, s.path, filename, expectedRevision)
	if err != nil {
		return conditionalBase{}, fmt.Errorf("provenance: resolve expected revision %q for %s: %w", expectedRevision, filename, err)
	}
	if expected.Commit != current.Commit {
		return conditionalBase{}, &RevisionConflictError{Filename: filename, Expected: expected.Short, Actual: current.Short}
	}
	return base, nil
}

// currentFileRevisionAtHead distinguishes a path that exists in the captured
// tree from one that merely has history. A deletion commit is therefore
// RevisionAbsent until a later create restores the path.
func (s *Store) currentFileRevisionAtHead(ctx context.Context, filename, head string) (Revision, bool, error) {
	if head == "" {
		return Revision{}, false, nil
	}
	exists, err := runGitText(ctx, s.path, "ls-tree", "-z", "--name-only", "--full-tree", head, "--", filename)
	if err != nil {
		return Revision{}, false, err
	}
	if exists == "" {
		return Revision{}, false, nil
	}
	commit, err := runGitText(ctx, s.path, "rev-list", "-1", "--end-of-options", head, "--", filename)
	if err != nil {
		return Revision{}, false, err
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return Revision{}, false, nil
	}
	revision, err := describeRevision(ctx, s.path, filename, commit, head)
	if err != nil {
		return Revision{}, false, err
	}
	return revision, true, nil
}

func (s *Store) fileDirtyAtHead(ctx context.Context, filename, head string) (bool, error) {
	if head == "" {
		if _, err := os.Lstat(filepath.Join(s.path, filename)); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		return s.fileTracked(ctx, filename)
	}
	err := s.git(ctx, nil, nil, "--literal-pathspecs", "diff", "--quiet", head, "--", filename)
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

func repositoryHead(ctx context.Context, repoPath string) (string, error) {
	hasHead, err := repositoryHasHead(ctx, repoPath)
	if err != nil || !hasHead {
		return "", err
	}
	head, err := runGitText(ctx, repoPath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head), nil
}

// commitFileContentAtHead builds the candidate commit through an isolated
// index, then advances HEAD only if it still names expectedHead. Neither the
// shared index nor the visible document is changed before that guarded ref
// update succeeds.
func (s *Store) commitFileContentAtHead(ctx context.Context, filename string, content []byte, message, expectedHead string) (string, error) {
	gitDirText, err := runGitText(ctx, s.path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("locate git directory: %w", err)
	}
	gitDir := strings.TrimSpace(gitDirText)

	prepared, err := prepareCommittedFile(gitDir, content, 0o644)
	if err != nil {
		return "", fmt.Errorf("prepare worktree file: %w", err)
	}
	defer prepared.remove()

	indexFile, err := os.CreateTemp(gitDir, "thane-index-*.tmp")
	if err != nil {
		return "", fmt.Errorf("prepare isolated index: %w", err)
	}
	indexPath := indexFile.Name()
	if closeErr := indexFile.Close(); closeErr != nil {
		os.Remove(indexPath)
		return "", fmt.Errorf("close isolated index: %w", closeErr)
	}
	if err := os.Remove(indexPath); err != nil {
		return "", fmt.Errorf("initialize isolated index: %w", err)
	}
	defer os.Remove(indexPath)
	indexEnv := []string{"GIT_INDEX_FILE=" + indexPath}

	if expectedHead == "" {
		if err := s.gitWithEnv(ctx, indexEnv, nil, nil, "read-tree", "--empty"); err != nil {
			return "", fmt.Errorf("git read-tree --empty: %w", err)
		}
	} else if err := s.gitWithEnv(ctx, indexEnv, nil, nil, "read-tree", expectedHead); err != nil {
		return "", fmt.Errorf("git read-tree: %w", err)
	}

	var blobBuf bytes.Buffer
	if err := s.git(ctx, bytes.NewReader(content), &blobBuf, "hash-object", "-w", "--stdin"); err != nil {
		return "", fmt.Errorf("git hash-object blob: %w", err)
	}
	blob := strings.TrimSpace(blobBuf.String())
	if err := s.gitWithEnv(ctx, indexEnv, nil, nil,
		"update-index", "--add", "--cacheinfo", "100644", blob, filename); err != nil {
		return "", fmt.Errorf("git update-index: %w", err)
	}

	var treeBuf bytes.Buffer
	if err := s.gitWithEnv(ctx, indexEnv, nil, &treeBuf, "write-tree"); err != nil {
		return "", fmt.Errorf("git write-tree: %w", err)
	}
	tree := strings.TrimSpace(treeBuf.String())
	if expectedHead != "" {
		baseTree, err := runGitText(ctx, s.path, "rev-parse", expectedHead+"^{tree}")
		if err != nil {
			return "", fmt.Errorf("resolve parent tree: %w", err)
		}
		if tree == strings.TrimSpace(baseTree) {
			s.logger.Debug("no changes to commit", "files", []string{filename})
			return "", nil
		}
	}

	commitHash, err := s.writeSignedCommitObject(ctx, tree, expectedHead, message)
	if err != nil {
		return "", err
	}
	expectedRef := expectedHead
	if expectedRef == "" {
		expectedRef = strings.Repeat("0", len(commitHash))
	}
	if err := s.git(ctx, nil, nil, "update-ref", "HEAD", commitHash, expectedRef); err != nil {
		return "", fmt.Errorf("git update-ref: %w", err)
	}

	if err := prepared.install(filepath.Join(s.path, filename)); err != nil {
		rollbackErr := s.rollbackHead(commitHash, expectedHead)
		if rollbackErr == nil {
			return "", fmt.Errorf("materialize worktree file after guarded commit (HEAD rolled back): %w", err)
		}
		return "", fmt.Errorf("materialize worktree file after commit %s: %w (HEAD rollback also failed: %v)", shorten(commitHash), err, rollbackErr)
	}

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitTimeout)
	defer cancel()
	if err := s.git(cleanupCtx, nil, nil, "reset", "--mixed", commitHash, "--", filename); err != nil {
		s.logger.Warn("conditional provenance commit succeeded but index cleanup failed",
			"commit", shorten(commitHash),
			"file", filename,
			"error", err,
		)
	}
	return commitHash, nil
}

func (s *Store) rollbackHead(commitHash, previousHead string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if previousHead == "" {
		return s.git(ctx, nil, nil, "update-ref", "-d", "HEAD", commitHash)
	}
	return s.git(ctx, nil, nil, "update-ref", "HEAD", previousHead, commitHash)
}

type preparedCommittedFile struct {
	path string
}

func prepareCommittedFile(gitDir string, content []byte, perm os.FileMode) (*preparedCommittedFile, error) {
	tmp, err := os.CreateTemp(gitDir, "thane-worktree-*.tmp")
	if err != nil {
		return nil, err
	}
	prepared := &preparedCommittedFile{path: tmp.Name()}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		prepared.remove()
		return nil, err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		prepared.remove()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		prepared.remove()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		prepared.remove()
		return nil, err
	}
	return prepared, nil
}

func (f *preparedCommittedFile) install(target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Rename(f.path, target); err != nil {
		return err
	}
	f.path = ""
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (f *preparedCommittedFile) remove() {
	if f != nil && f.path != "" {
		_ = os.Remove(f.path)
	}
}

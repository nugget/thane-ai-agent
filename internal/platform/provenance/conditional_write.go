package provenance

import (
	"bytes"
	"context"
	"fmt"
	"os"
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

// Error implements error with a retry-oriented message suitable for passing
// through model-facing document tools.
func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("revision conflict for %s: expected %s, current is %s; read the current document and retry against its revision", e.Filename, e.Expected, e.Actual)
}

// WriteIfRevision writes and commits content only when filename is still at
// expectedRevision. The comparison and commit share the store mutex, so a
// concurrent managed write or remote fast-forward cannot land between them.
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

	if err := s.checkExpectedRevisionLocked(ctx, filename, expectedRevision); err != nil {
		return Revision{}, err
	}
	absPath := filepath.Join(s.path, filename)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return Revision{}, fmt.Errorf("provenance: create directory for %s: %w", filename, err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return Revision{}, fmt.Errorf("provenance: write %s: %w", filename, err)
	}
	committed, err := s.commitFile(ctx, filename, message)
	if err != nil {
		return Revision{}, fmt.Errorf("provenance: commit %s: %w", filename, err)
	}
	if committed {
		s.logger.Info("provenance file committed",
			"file", filename,
			"bytes", len(content),
			"message", messageSubject(message),
		)
	}
	revision, found, err := s.currentFileRevisionLocked(ctx, filename)
	if err != nil {
		return Revision{}, fmt.Errorf("provenance: resolve committed revision for %s: %w", filename, err)
	}
	if !found {
		return Revision{}, fmt.Errorf("provenance: committed file %s has no revision", filename)
	}
	return revision, nil
}

// checkExpectedRevisionLocked compares one file's latest committed revision
// while the caller holds s.mu. A dirty target is always a conflict: resolving
// only committed history and then overwriting uncommitted operator bytes would
// satisfy the hash comparison while violating its purpose.
func (s *Store) checkExpectedRevisionLocked(ctx context.Context, filename, expectedRevision string) error {
	dirty, err := s.fileDirty(ctx, filename)
	if err != nil {
		return fmt.Errorf("provenance: inspect worktree state for %s: %w", filename, err)
	}
	if dirty {
		return &RevisionConflictError{Filename: filename, Expected: expectedRevision, Actual: "worktree_dirty"}
	}

	current, hasCurrent, err := s.currentFileRevisionLocked(ctx, filename)
	if err != nil {
		return fmt.Errorf("provenance: resolve current revision for %s: %w", filename, err)
	}
	if strings.EqualFold(expectedRevision, RevisionAbsent) {
		if hasCurrent {
			return &RevisionConflictError{Filename: filename, Expected: RevisionAbsent, Actual: current.Short}
		}
		if _, statErr := os.Stat(filepath.Join(s.path, filename)); statErr == nil {
			return &RevisionConflictError{Filename: filename, Expected: RevisionAbsent, Actual: "worktree_dirty"}
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("provenance: stat %s: %w", filename, statErr)
		}
		return nil
	}
	if !hasCurrent {
		return &RevisionConflictError{Filename: filename, Expected: expectedRevision, Actual: RevisionAbsent}
	}

	expected, err := resolveRevision(ctx, s.path, filename, expectedRevision)
	if err != nil {
		return fmt.Errorf("provenance: resolve expected revision %q for %s: %w", expectedRevision, filename, err)
	}
	if expected.Commit != current.Commit {
		return &RevisionConflictError{Filename: filename, Expected: expected.Short, Actual: current.Short}
	}
	return nil
}

// currentFileRevisionLocked distinguishes a missing file history from a git
// failure. resolveRevision deliberately presents both as errors, which is
// useful for readers but unsafe for RevisionAbsent: an unavailable repository
// must never be mistaken for permission to create.
func (s *Store) currentFileRevisionLocked(ctx context.Context, filename string) (Revision, bool, error) {
	hasHead, err := repositoryHasHead(ctx, s.path)
	if err != nil {
		return Revision{}, false, err
	}
	if !hasHead {
		return Revision{}, false, nil
	}
	commit, err := runGitText(ctx, s.path, "rev-list", "-1", "--end-of-options", "HEAD", "--", filename)
	if err != nil {
		return Revision{}, false, err
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return Revision{}, false, nil
	}
	revision, err := describeRevision(ctx, s.path, filename, commit, "HEAD")
	if err != nil {
		return Revision{}, false, err
	}
	return revision, true, nil
}

func (s *Store) fileDirty(ctx context.Context, filename string) (bool, error) {
	var out bytes.Buffer
	if err := s.git(ctx, nil, &out, "--literal-pathspecs", "status", "--porcelain=v1", "--", filename); err != nil {
		return false, err
	}
	return strings.TrimSpace(out.String()) != "", nil
}

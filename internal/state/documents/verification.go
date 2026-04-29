package documents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Store) verifyDocumentForConsumer(ctx context.Context, root, relPath, consumer string) error {
	root = normalizeRootName(root)
	relPath = filepath.ToSlash(filepath.Clean(relPath))
	policy := s.rootPolicy(root)
	if !policy.Git.Enabled || policy.Git.VerifySignatures == VerificationNone {
		return nil
	}

	verifier := s.rootVerifier(root)
	if verifier == nil {
		result := SignatureVerification{
			Status:   SignatureUnavailable,
			Mode:     policy.Git.VerifySignatures,
			Message:  "signature verification is configured but no verifier is available",
			Consumer: consumer,
		}
		s.recordRootVerification(root, result)
		return s.handleVerificationFailure(root, relPath, consumer, result)
	}

	result, err := verifier.Verify(ctx, relPath)
	if result.Status == "" {
		result.Status = SignatureFailed
	}
	result.Mode = policy.Git.VerifySignatures
	result.Consumer = consumer
	result.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil && result.Message == "" {
		result.Message = err.Error()
	}
	s.recordRootVerification(root, result)
	if err != nil || result.Status != SignatureTrusted {
		return s.handleVerificationFailure(root, relPath, consumer, result)
	}
	return nil
}

// VerifyRef verifies a managed semantic ref for policy-sensitive consumers.
func (s *Store) VerifyRef(ctx context.Context, ref string, consumer string) error {
	if s == nil {
		return nil
	}
	root, relPath, err := parseRef(ref)
	if err != nil {
		return err
	}
	if !rootExists(s.roots, root) {
		return fmt.Errorf("unknown document root %q", root)
	}
	return s.verifyDocumentForConsumer(ctx, root, relPath, consumer)
}

// VerifyPath verifies an absolute file path when it belongs to a managed
// document root. Paths outside configured roots are ignored.
func (s *Store) VerifyPath(ctx context.Context, path string, consumer string) error {
	if s == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	root, relPath, ok, err := s.rootRefForPath(path)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return s.verifyDocumentForConsumer(ctx, root, relPath, consumer)
}

func (s *Store) rootRefForPath(path string) (root string, relPath string, ok bool, err error) {
	targetAbs, err := filepath.Abs(path)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve document path for verification: %w", err)
	}
	targetResolved, err := evalSymlinksAllowingMissing(targetAbs)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve document path for verification: %w", err)
	}

	type candidate struct {
		root string
		path string
	}
	var candidates []candidate
	for root := range s.roots {
		rootPath, err := s.resolveRootPath(root)
		if err != nil {
			continue
		}
		if !pathWithinRoot(rootPath, targetResolved) {
			continue
		}
		candidates = append(candidates, candidate{root: root, path: rootPath})
	}
	if len(candidates) == 0 {
		return "", "", false, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return len(candidates[i].path) > len(candidates[j].path)
	})
	rel, err := filepath.Rel(candidates[0].path, targetResolved)
	if err != nil {
		return "", "", false, fmt.Errorf("compute managed document ref: %w", err)
	}
	return candidates[0].root, filepath.ToSlash(filepath.Clean(rel)), true, nil
}

// evalSymlinksAllowingMissing resolves symlinks in path the same way
// [filepath.EvalSymlinks] does, but tolerates a missing leaf (or an
// arbitrarily long missing tail) by walking up to the longest
// existing prefix, resolving symlinks there, then re-joining the
// non-existent components verbatim.
//
// Verifier callers exercise this on three legitimate "file does not
// exist" paths:
//   - file-tool writes that create a new file inside a managed root
//   - inject-files configured but not yet present on disk
//   - edits where the dir tree leading up to the file exists but the
//     file itself was just removed
//
// Returning an error in those cases would either make required-mode
// startup brittle (any missing inject-file becomes fatal for a reason
// unrelated to signing) or silently disable verification for
// new-file writes. Both are worse than carrying the abstract path
// through the rest of the lookup, which still correctly classifies
// the path as in-root or out-of-root via prefix containment.
func evalSymlinksAllowingMissing(path string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// Walk up to the longest existing ancestor.
	current := path
	var trailing []string
	for {
		parent := filepath.Dir(current)
		trailing = append([]string{filepath.Base(current)}, trailing...)
		if parent == current {
			// Reached the root without finding an existing
			// ancestor — return the abstract abs path; downstream
			// prefix-containment will classify it as outside any
			// managed root.
			return filepath.Clean(path), nil
		}
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return filepath.Join(append([]string{resolved}, trailing...)...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		current = parent
	}
}

func (s *Store) handleVerificationFailure(root, relPath, consumer string, result SignatureVerification) error {
	message := strings.TrimSpace(result.Message)
	if message == "" {
		message = "signature verification failed"
	}
	fields := []any{
		"root", root,
		"path", relPath,
		"mode", result.Mode,
		"consumer", consumer,
		"status", result.Status,
		"message", message,
	}
	if result.Commit != "" {
		fields = append(fields, "commit", result.Commit)
	}
	if result.Mode == VerificationWarn {
		s.logger.Warn("document root signature verification warning", fields...)
		return nil
	}
	return fmt.Errorf("document %s:%s blocked by signature policy for %s: %s", root, relPath, consumer, message)
}

func (s *Store) recordRootVerification(root string, result SignatureVerification) {
	if s == nil {
		return
	}
	root = normalizeRootName(root)
	if root == "" {
		return
	}
	if result.CheckedAt == "" {
		result.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	s.verificationMu.Lock()
	defer s.verificationMu.Unlock()
	s.verification[root] = result
}

func (s *Store) rootVerificationSummary(ctx context.Context, root string) *SignatureVerification {
	root = normalizeRootName(root)
	policy := s.rootPolicy(root)
	if !policy.Git.Enabled || policy.Git.VerifySignatures == VerificationNone {
		return nil
	}

	verifier := s.rootVerifier(root)
	if verifier == nil {
		result := SignatureVerification{
			Status:    SignatureUnavailable,
			Mode:      policy.Git.VerifySignatures,
			Message:   "signature verification is configured but no verifier is available",
			CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Consumer:  "root_summary",
		}
		s.recordRootVerification(root, result)
		return &result
	}

	result, err := verifier.VerifyRoot(ctx)
	if result.Status == "" {
		result.Status = SignatureFailed
	}
	result.Mode = policy.Git.VerifySignatures
	result.Consumer = "root_summary"
	result.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil && result.Message == "" {
		result.Message = err.Error()
	}
	s.recordRootVerification(root, result)
	return &result
}

package documents

import (
	"context"
	"fmt"
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
	targetResolved, err := filepath.EvalSymlinks(targetAbs)
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

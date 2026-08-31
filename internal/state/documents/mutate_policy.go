package documents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func (s *Store) writeDocumentFile(ctx context.Context, root, relPath, raw string) error {
	_, err := s.writeDocumentFileAtRevision(ctx, root, relPath, raw, "")
	return err
}

// writeDocumentFileAtRevision applies an optional compare-and-write
// precondition. Empty preserves the unconditional mutation contract used by
// existing callers; a non-empty revision requires a writer that can compare
// and commit atomically.
func (s *Store) writeDocumentFileAtRevision(ctx context.Context, root, relPath, raw, expectedRevision string) (string, error) {
	absPath, err := s.resolveDocumentWritePath(root, relPath)
	if err != nil {
		return "", err
	}
	if err := s.ensureRootAuthoringAllowed(root); err != nil {
		return "", err
	}
	if writer := s.rootWriter(root); writer != nil {
		message := documentWriteMessage("doc_write", root, relPath, raw)
		expectedRevision = strings.TrimSpace(expectedRevision)
		if expectedRevision != "" {
			revision, err := writer.WriteIfRevision(ctx, relPath, raw, message, expectedRevision)
			if err != nil {
				return "", fmt.Errorf("write %s with expected_revision %q: %w", makeRef(root, relPath), expectedRevision, err)
			}
			if err := s.refreshDocumentWrite(ctx, root, relPath); err != nil {
				return "", err
			}
			return revision, nil
		} else if err := writer.Write(ctx, relPath, raw, message); err != nil {
			return "", fmt.Errorf("write document through root policy: %w", err)
		}
		if err := s.refreshDocumentWrite(ctx, root, relPath); err != nil {
			return "", err
		}
		return "", nil
	}
	if strings.TrimSpace(expectedRevision) != "" {
		return "", fmt.Errorf("expected_revision requires a revision-backed document root; root %q writes directly to the filesystem", root)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return "", fmt.Errorf("create document directories: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(absPath), ".thane-doc-*")
	if err != nil {
		return "", fmt.Errorf("create temp document: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(raw); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temp document: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp document: %w", err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		return "", fmt.Errorf("replace document: %w", err)
	}
	if err := s.refreshDocumentWrite(ctx, root, relPath); err != nil {
		return "", err
	}
	return "", nil
}

func (s *Store) refreshDocumentWrite(ctx context.Context, root, relPath string) error {
	if !s.rootPolicy(root).Indexing {
		if err := s.deleteIndexedDocument(ctx, root, relPath); err != nil {
			return err
		}
		s.touchLastRefresh(time.Now())
		return nil
	}
	if err := s.upsertFile(ctx, root, relPath); err != nil {
		return fmt.Errorf("refresh indexed document: %w", err)
	}
	s.touchLastRefresh(time.Now())
	return nil
}

func (s *Store) removeDocumentFile(ctx context.Context, root, relPath string) error {
	absPath, err := s.resolveDocumentPath(root, relPath)
	if err != nil {
		return err
	}
	if err := s.ensureRootAuthoringAllowed(root); err != nil {
		return err
	}
	if writer := s.rootWriter(root); writer != nil {
		if err := writer.Delete(ctx, relPath, documentMutationMessage("doc_delete", root, relPath)); err != nil {
			return fmt.Errorf("delete document through root policy: %w", err)
		}
	} else if err := os.Remove(absPath); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if err := s.deleteIndexedDocument(ctx, root, relPath); err != nil {
		return err
	}
	if rootPath, err := s.resolveRootPath(root); err == nil {
		s.pruneEmptyDocumentDirs(rootPath, filepath.Dir(absPath))
	}
	return nil
}

func (s *Store) ensureRootAuthoringAllowed(root string) error {
	mode := s.rootPolicy(root).Authoring
	switch mode {
	case "", AuthoringManaged:
		return nil
	case AuthoringReadOnly, AuthoringRestricted:
		return fmt.Errorf("document root %q authoring is %q; managed mutations are not allowed", root, mode)
	default:
		return fmt.Errorf("document root %q has unsupported authoring mode %q", root, mode)
	}
}

func documentMutationMessage(action, root, relPath string) string {
	return action + " " + makeRef(root, relPath)
}

// facetSubjectMaxRunes clamps a status_line borrowed into a commit
// subject. Published projections are already budgeted below this, so
// the clamp only fires on content that arrived through a non-publish
// write (doc_write can put anything under a reserved heading) — and a
// commit subject is annotation, not the content itself, so clipping
// here is honest where clipping a projection would not be.
const facetSubjectMaxRunes = 120

// documentWriteMessage composes the commit message for a document
// write. For an unfaceted document it is the same mechanical subject
// documentMutationMessage produces. When the content carries facet
// sections, the message borrows them: the status_line joins the
// subject — so the root's git log reads as a timeline of the
// document's own one-line verdicts — and the digest becomes the commit
// body, the actionable summary at the moment of the write. No writer
// cooperation is needed: the facet headings are the contract, so every
// write of a faceted document gets this regardless of which tool or
// author produced it.
func documentWriteMessage(action, root, relPath, raw string) string {
	subject := documentMutationMessage(action, root, relPath)
	_, body := splitFrontmatter(raw)
	payload, ok := looppkg.ParseFacetSections(body)
	if !ok {
		return subject
	}
	if verdict, ok := payload.FacetByKey("status_line"); ok {
		if line := strings.TrimSpace(verdict); line != "" {
			line = strings.Join(strings.Fields(line), " ")
			if runes := []rune(line); len(runes) > facetSubjectMaxRunes {
				line = string(runes[:facetSubjectMaxRunes-1]) + "…"
			}
			subject += " — " + line
		}
	}
	if digest, ok := payload.FacetByKey("digest"); ok {
		if summary := strings.TrimSpace(digest); summary != "" {
			subject += "\n\n" + summary
		}
	}
	return subject
}

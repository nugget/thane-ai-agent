package documents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func (s *Store) writeDocumentFile(ctx context.Context, root, relPath, raw string) error {
	absPath, err := s.resolveDocumentWritePath(root, relPath)
	if err != nil {
		return err
	}
	if err := s.ensureRootAuthoringAllowed(root); err != nil {
		return err
	}
	if writer := s.rootWriter(root); writer != nil {
		if err := writer.Write(ctx, relPath, raw, documentMutationMessage("doc_write", root, relPath)); err != nil {
			return fmt.Errorf("write document through root policy: %w", err)
		}
		if err := s.refreshDocumentWrite(ctx, root, relPath); err != nil {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("create document directories: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(absPath), ".thane-doc-*")
	if err != nil {
		return fmt.Errorf("create temp document: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp document: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp document: %w", err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		return fmt.Errorf("replace document: %w", err)
	}
	if err := s.refreshDocumentWrite(ctx, root, relPath); err != nil {
		return err
	}
	return nil
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

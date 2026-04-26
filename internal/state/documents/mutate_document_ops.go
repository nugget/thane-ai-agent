package documents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DeleteArgs removes one managed document by semantic ref.
type DeleteArgs struct {
	Ref string `json:"ref"`
}

// MoveArgs relocates one managed document to a new semantic ref.
type MoveArgs struct {
	Ref            string `json:"ref"`
	DestinationRef string `json:"destination_ref"`
	Overwrite      bool   `json:"overwrite,omitempty"`
}

// CopyArgs duplicates one managed document at a new semantic ref.
type CopyArgs struct {
	Ref            string `json:"ref"`
	DestinationRef string `json:"destination_ref"`
	Overwrite      bool   `json:"overwrite,omitempty"`
}

// DeleteResult summarizes one managed document deletion.
type DeleteResult struct {
	Action      string   `json:"action"`
	DeletedRef  string   `json:"deleted_ref"`
	Root        string   `json:"root"`
	Path        string   `json:"path"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	ModifiedAt  string   `json:"modified_at"`
	WordCount   int      `json:"word_count"`
	SizeBytes   int64    `json:"size_bytes"`
}

// MoveResult summarizes one managed document move/rename.
type MoveResult struct {
	Action      string   `json:"action"`
	FromRef     string   `json:"from_ref"`
	ToRef       string   `json:"to_ref"`
	FromRoot    string   `json:"from_root"`
	FromPath    string   `json:"from_path"`
	ToRoot      string   `json:"to_root"`
	ToPath      string   `json:"to_path"`
	Overwrote   bool     `json:"overwrote,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	ModifiedAt  string   `json:"modified_at"`
	WordCount   int      `json:"word_count"`
	SizeBytes   int64    `json:"size_bytes"`
}

// CopyResult summarizes one managed document copy.
type CopyResult struct {
	Action      string   `json:"action"`
	FromRef     string   `json:"from_ref"`
	ToRef       string   `json:"to_ref"`
	FromRoot    string   `json:"from_root"`
	FromPath    string   `json:"from_path"`
	ToRoot      string   `json:"to_root"`
	ToPath      string   `json:"to_path"`
	Overwrote   bool     `json:"overwrote,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	ModifiedAt  string   `json:"modified_at"`
	WordCount   int      `json:"word_count"`
	SizeBytes   int64    `json:"size_bytes"`
}

func (s *Store) Delete(ctx context.Context, args DeleteArgs) (*DeleteResult, error) {
	root, relPath, err := parseRef(args.Ref)
	if err != nil {
		return nil, err
	}
	absPath, err := s.resolveDocumentPath(root, relPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}

	record, _, _, err := s.readDocumentFile(absPath, root, relPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}

	if err := s.removeDocumentFile(ctx, root, relPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}
	return deleteResultFromRecord(record), nil
}

func (s *Store) Move(ctx context.Context, args MoveArgs) (*MoveResult, error) {
	srcRoot, srcRelPath, err := parseRef(args.Ref)
	if err != nil {
		return nil, err
	}
	dstRoot, dstRelPath, err := parseRef(args.DestinationRef)
	if err != nil {
		return nil, err
	}
	if srcRoot == dstRoot && srcRelPath == dstRelPath {
		return nil, fmt.Errorf("destination_ref must differ from ref")
	}

	srcAbsPath, err := s.resolveDocumentPath(srcRoot, srcRelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}
	dstAbsPath, err := s.resolveDocumentWritePath(dstRoot, dstRelPath)
	if err != nil {
		return nil, err
	}

	sourceRecord, _, _, err := s.readDocumentFile(srcAbsPath, srcRoot, srcRelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}
	raw, err := os.ReadFile(srcAbsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, fmt.Errorf("read source document: %w", err)
	}

	destinationExists := false
	var originalDestinationRaw []byte
	if _, err := os.Stat(dstAbsPath); err == nil {
		destinationExists = true
		originalDestinationRaw, err = os.ReadFile(dstAbsPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("destination document disappeared during move: %s", args.DestinationRef)
			}
			return nil, fmt.Errorf("read destination document for rollback: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("check destination document: %w", err)
	}
	if destinationExists && !args.Overwrite {
		return nil, fmt.Errorf("destination document already exists at %s; retry with overwrite=true or choose a different destination_ref", args.DestinationRef)
	}

	if err := s.writeDocumentFile(ctx, dstRoot, dstRelPath, string(raw)); err != nil {
		return nil, err
	}
	if err := s.removeDocumentFile(ctx, srcRoot, srcRelPath); err != nil {
		if destinationExists {
			if restoreErr := s.writeDocumentFile(ctx, dstRoot, dstRelPath, string(originalDestinationRaw)); restoreErr != nil {
				if os.IsNotExist(err) {
					return nil, fmt.Errorf("document not found: %s (rollback restore failed: %v)", args.Ref, restoreErr)
				}
				return nil, fmt.Errorf("remove source document: %w (rollback restore failed: %v)", err, restoreErr)
			}
		} else {
			_ = os.Remove(dstAbsPath)
			_ = s.deleteIndexedDocument(ctx, dstRoot, dstRelPath)
		}
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, fmt.Errorf("remove source document: %w", err)
	}

	destinationRecord, _, _, err := s.readDocumentFile(dstAbsPath, dstRoot, dstRelPath)
	if err != nil {
		return nil, err
	}
	return moveResultFromRecords(sourceRecord, destinationRecord, destinationExists), nil
}

func (s *Store) Copy(ctx context.Context, args CopyArgs) (*CopyResult, error) {
	srcRoot, srcRelPath, err := parseRef(args.Ref)
	if err != nil {
		return nil, err
	}
	dstRoot, dstRelPath, err := parseRef(args.DestinationRef)
	if err != nil {
		return nil, err
	}
	if srcRoot == dstRoot && srcRelPath == dstRelPath {
		return nil, fmt.Errorf("destination_ref must differ from ref")
	}

	srcAbsPath, err := s.resolveDocumentPath(srcRoot, srcRelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}
	dstAbsPath, err := s.resolveDocumentWritePath(dstRoot, dstRelPath)
	if err != nil {
		return nil, err
	}

	sourceRecord, _, _, err := s.readDocumentFile(srcAbsPath, srcRoot, srcRelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}
	raw, err := os.ReadFile(srcAbsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, fmt.Errorf("read source document: %w", err)
	}

	destinationExists := false
	if _, err := os.Stat(dstAbsPath); err == nil {
		destinationExists = true
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("check destination document: %w", err)
	}
	if destinationExists && !args.Overwrite {
		return nil, fmt.Errorf("destination document already exists at %s; retry with overwrite=true or choose a different destination_ref", args.DestinationRef)
	}

	if err := s.writeDocumentFile(ctx, dstRoot, dstRelPath, string(raw)); err != nil {
		return nil, err
	}
	destinationRecord, _, _, err := s.readDocumentFile(dstAbsPath, dstRoot, dstRelPath)
	if err != nil {
		return nil, err
	}
	return copyResultFromRecords(sourceRecord, destinationRecord, destinationExists), nil
}

func (s *Store) deleteIndexedDocument(ctx context.Context, root, relPath string) error {
	if err := s.deleteIndexedDocumentRows(ctx, root, relPath); err != nil {
		return err
	}
	s.touchLastRefresh(time.Now())
	return nil
}

func (s *Store) touchLastRefresh(now time.Time) {
	s.refreshMu.Lock()
	s.lastRefresh = now
	s.refreshMu.Unlock()
}

func (s *Store) pruneEmptyDocumentDirs(rootPath, dir string) {
	rootPath = filepath.Clean(rootPath)
	dir = filepath.Clean(dir)
	for dir != rootPath && dir != "." && dir != string(filepath.Separator) {
		if err := os.Remove(dir); err != nil {
			break
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
}

func deleteResultFromRecord(record *DocumentRecord) *DeleteResult {
	return &DeleteResult{
		Action:      "doc_delete",
		DeletedRef:  record.Ref,
		Root:        record.Root,
		Path:        record.Path,
		Title:       record.Title,
		Description: record.Description,
		Tags:        append([]string(nil), record.Tags...),
		CreatedAt:   firstValue(record.Frontmatter, "created"),
		UpdatedAt:   firstValue(record.Frontmatter, "updated"),
		ModifiedAt:  record.ModifiedAt,
		WordCount:   record.WordCount,
		SizeBytes:   record.SizeBytes,
	}
}

func moveResultFromRecords(source, destination *DocumentRecord, overwrote bool) *MoveResult {
	return &MoveResult{
		Action:      "doc_move",
		FromRef:     source.Ref,
		ToRef:       destination.Ref,
		FromRoot:    source.Root,
		FromPath:    source.Path,
		ToRoot:      destination.Root,
		ToPath:      destination.Path,
		Overwrote:   overwrote,
		Title:       destination.Title,
		Description: destination.Description,
		Tags:        append([]string(nil), destination.Tags...),
		CreatedAt:   firstValue(destination.Frontmatter, "created"),
		UpdatedAt:   firstValue(destination.Frontmatter, "updated"),
		ModifiedAt:  destination.ModifiedAt,
		WordCount:   destination.WordCount,
		SizeBytes:   destination.SizeBytes,
	}
}

func copyResultFromRecords(source, destination *DocumentRecord, overwrote bool) *CopyResult {
	return &CopyResult{
		Action:      "doc_copy",
		FromRef:     source.Ref,
		ToRef:       destination.Ref,
		FromRoot:    source.Root,
		FromPath:    source.Path,
		ToRoot:      destination.Root,
		ToPath:      destination.Path,
		Overwrote:   overwrote,
		Title:       destination.Title,
		Description: destination.Description,
		Tags:        append([]string(nil), destination.Tags...),
		CreatedAt:   firstValue(destination.Frontmatter, "created"),
		UpdatedAt:   firstValue(destination.Frontmatter, "updated"),
		ModifiedAt:  destination.ModifiedAt,
		WordCount:   destination.WordCount,
		SizeBytes:   destination.SizeBytes,
	}
}

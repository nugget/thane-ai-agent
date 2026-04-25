package documents

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// SectionTransferArgs copies or moves one section into another managed doc.
type SectionTransferArgs struct {
	Ref                string `json:"ref"`
	Section            string `json:"section"`
	DestinationRef     string `json:"destination_ref"`
	DestinationSection string `json:"destination_section,omitempty"`
	DestinationLevel   int    `json:"destination_level,omitempty"`
}

// SectionTransferResult summarizes one section-level copy or move.
type SectionTransferResult struct {
	Action             string   `json:"action"`
	SourceRef          string   `json:"source_ref"`
	SourceRoot         string   `json:"source_root"`
	SourcePath         string   `json:"source_path"`
	SourceSection      string   `json:"source_section"`
	DestinationRef     string   `json:"destination_ref"`
	DestinationRoot    string   `json:"destination_root"`
	DestinationPath    string   `json:"destination_path"`
	DestinationSection string   `json:"destination_section"`
	DestinationLevel   int      `json:"destination_level"`
	DestinationExisted bool     `json:"destination_existed"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	CreatedAt          string   `json:"created_at,omitempty"`
	UpdatedAt          string   `json:"updated_at,omitempty"`
	ModifiedAt         string   `json:"modified_at"`
	WordCount          int      `json:"word_count"`
	SizeBytes          int64    `json:"size_bytes"`
}

func (s *Store) CopySection(ctx context.Context, args SectionTransferArgs) (*SectionTransferResult, error) {
	return s.transferSection(ctx, "doc_copy_section", args, false)
}

func (s *Store) MoveSection(ctx context.Context, args SectionTransferArgs) (*SectionTransferResult, error) {
	return s.transferSection(ctx, "doc_move_section", args, true)
}

func (s *Store) transferSection(ctx context.Context, action string, args SectionTransferArgs, removeSource bool) (*SectionTransferResult, error) {
	srcRoot, srcRelPath, err := parseRef(args.Ref)
	if err != nil {
		return nil, err
	}
	dstRoot, dstRelPath, err := parseRef(args.DestinationRef)
	if err != nil {
		return nil, err
	}

	srcAbsPath, err := s.resolveDocumentPath(srcRoot, srcRelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}
	sourceRecord, srcFrontmatterRaw, srcBody, err := s.readDocumentFile(srcAbsPath, srcRoot, srcRelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document not found: %s", args.Ref)
		}
		return nil, err
	}
	sourceSection, err := extractDocumentSection(srcBody, args.Section)
	if err != nil {
		return nil, err
	}

	dstAbsPath, err := s.resolveDocumentWritePath(dstRoot, dstRelPath)
	if err != nil {
		return nil, err
	}
	destinationExists := false
	var destinationRecord *DocumentRecord
	var dstFrontmatterRaw string
	var dstBody string
	if _, err := os.Stat(dstAbsPath); err == nil {
		destinationExists = true
		destinationRecord, dstFrontmatterRaw, dstBody, err = s.readDocumentFile(dstAbsPath, dstRoot, dstRelPath)
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("check destination document: %w", err)
	}

	destinationHeading := strings.TrimSpace(args.DestinationSection)
	if destinationHeading == "" {
		destinationHeading = sourceSection.Heading
	}
	destinationLevel := args.DestinationLevel
	if destinationLevel <= 0 {
		destinationLevel = sourceSection.Level
	}
	if destinationLevel <= 0 || destinationLevel > 6 {
		destinationLevel = 2
	}
	if removeSource && srcRoot == dstRoot && srcRelPath == dstRelPath {
		if strings.EqualFold(destinationHeading, sourceSection.Heading) || slugify(destinationHeading) == sourceSection.Slug {
			return nil, fmt.Errorf("doc_move_section cannot target the same section in the same document; use doc_edit to rename or restructure it in place")
		}
	}

	updatedDestinationBody, resolvedHeading, err := upsertDocumentSection(dstBody, destinationHeading, destinationHeading, destinationLevel, sectionBodyContent(sourceSection))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var renderedDestination string
	if destinationExists {
		renderedDestination = renderDocumentFromParts(touchDocumentFrontmatter(dstFrontmatterRaw, destinationRecord, now), updatedDestinationBody)
	} else {
		meta := mergeDocumentFrontmatter(nil, "", "", nil, nil, now)
		renderedDestination = renderDocument(meta, updatedDestinationBody)
	}
	if err := s.writeDocumentFile(ctx, dstRoot, dstRelPath, renderedDestination); err != nil {
		return nil, err
	}

	if removeSource {
		updatedSourceBody, _, err := deleteDocumentSection(srcBody, args.Section)
		if err != nil {
			return nil, err
		}
		renderedSource := renderDocumentFromParts(touchDocumentFrontmatter(srcFrontmatterRaw, sourceRecord, now), updatedSourceBody)
		if err := s.writeDocumentFile(ctx, srcRoot, srcRelPath, renderedSource); err != nil {
			return nil, err
		}
	}

	destinationRecord, _, _, err = s.readDocumentFile(dstAbsPath, dstRoot, dstRelPath)
	if err != nil {
		return nil, err
	}
	return sectionTransferResultFromRecord(action, sourceRecord, sourceSection, destinationRecord, resolvedHeading, destinationLevel, destinationExists), nil
}

func sectionTransferResultFromRecord(action string, source *DocumentRecord, sourceSection Section, destination *DocumentRecord, destinationSection string, destinationLevel int, destinationExisted bool) *SectionTransferResult {
	return &SectionTransferResult{
		Action:             action,
		SourceRef:          source.Ref,
		SourceRoot:         source.Root,
		SourcePath:         source.Path,
		SourceSection:      sourceSection.Heading,
		DestinationRef:     destination.Ref,
		DestinationRoot:    destination.Root,
		DestinationPath:    destination.Path,
		DestinationSection: destinationSection,
		DestinationLevel:   destinationLevel,
		DestinationExisted: destinationExisted,
		Title:              destination.Title,
		Description:        destination.Description,
		Tags:               append([]string(nil), destination.Tags...),
		CreatedAt:          firstValue(destination.Frontmatter, "created"),
		UpdatedAt:          firstValue(destination.Frontmatter, "updated"),
		ModifiedAt:         destination.ModifiedAt,
		WordCount:          destination.WordCount,
		SizeBytes:          destination.SizeBytes,
	}
}

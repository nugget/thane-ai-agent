package documents

import "time"

type modelMutationResult struct {
	Action        string   `json:"action"`
	Ref           string   `json:"ref"`
	Root          string   `json:"root"`
	Path          string   `json:"path"`
	Existed       bool     `json:"existed"`
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	CreatedDelta  string   `json:"created_delta,omitempty"`
	UpdatedDelta  string   `json:"updated_delta,omitempty"`
	ModifiedDelta string   `json:"modified_delta,omitempty"`
	WordCount     int      `json:"word_count"`
	SizeBytes     int64    `json:"size_bytes"`
	Section       string   `json:"section,omitempty"`
	Window        string   `json:"window,omitempty"`
}

type modelDeleteResult struct {
	Action        string   `json:"action"`
	DeletedRef    string   `json:"deleted_ref"`
	Root          string   `json:"root"`
	Path          string   `json:"path"`
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	CreatedDelta  string   `json:"created_delta,omitempty"`
	UpdatedDelta  string   `json:"updated_delta,omitempty"`
	ModifiedDelta string   `json:"modified_delta,omitempty"`
	WordCount     int      `json:"word_count"`
	SizeBytes     int64    `json:"size_bytes"`
}

type modelMoveResult struct {
	Action        string   `json:"action"`
	FromRef       string   `json:"from_ref"`
	ToRef         string   `json:"to_ref"`
	FromRoot      string   `json:"from_root"`
	FromPath      string   `json:"from_path"`
	ToRoot        string   `json:"to_root"`
	ToPath        string   `json:"to_path"`
	Overwrote     bool     `json:"overwrote,omitempty"`
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	CreatedDelta  string   `json:"created_delta,omitempty"`
	UpdatedDelta  string   `json:"updated_delta,omitempty"`
	ModifiedDelta string   `json:"modified_delta,omitempty"`
	WordCount     int      `json:"word_count"`
	SizeBytes     int64    `json:"size_bytes"`
}

type modelCopyResult struct {
	Action        string   `json:"action"`
	FromRef       string   `json:"from_ref"`
	ToRef         string   `json:"to_ref"`
	FromRoot      string   `json:"from_root"`
	FromPath      string   `json:"from_path"`
	ToRoot        string   `json:"to_root"`
	ToPath        string   `json:"to_path"`
	Overwrote     bool     `json:"overwrote,omitempty"`
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	CreatedDelta  string   `json:"created_delta,omitempty"`
	UpdatedDelta  string   `json:"updated_delta,omitempty"`
	ModifiedDelta string   `json:"modified_delta,omitempty"`
	WordCount     int      `json:"word_count"`
	SizeBytes     int64    `json:"size_bytes"`
}

type modelSectionTransferResult struct {
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
	CreatedDelta       string   `json:"created_delta,omitempty"`
	UpdatedDelta       string   `json:"updated_delta,omitempty"`
	ModifiedDelta      string   `json:"modified_delta,omitempty"`
	WordCount          int      `json:"word_count"`
	SizeBytes          int64    `json:"size_bytes"`
}

type modelCommitResult struct {
	IntakeID string       `json:"intake_id"`
	Action   IntakeAction `json:"action"`
	Ref      string       `json:"ref,omitempty"`
	Status   string       `json:"status"`
	Result   any          `json:"result,omitempty"`
}

func toModelMutationResult(result *MutationResult, now time.Time) *modelMutationResult {
	if result == nil {
		return nil
	}
	return &modelMutationResult{
		Action:        result.Action,
		Ref:           result.Ref,
		Root:          result.Root,
		Path:          result.Path,
		Existed:       result.Existed,
		Title:         result.Title,
		Description:   result.Description,
		Tags:          append([]string(nil), result.Tags...),
		CreatedDelta:  modelDelta(result.CreatedAt, now),
		UpdatedDelta:  modelDelta(result.UpdatedAt, now),
		ModifiedDelta: modelDelta(result.ModifiedAt, now),
		WordCount:     result.WordCount,
		SizeBytes:     result.SizeBytes,
		Section:       result.Section,
		Window:        result.Window,
	}
}

func toModelDeleteResult(result *DeleteResult, now time.Time) *modelDeleteResult {
	if result == nil {
		return nil
	}
	return &modelDeleteResult{
		Action:        result.Action,
		DeletedRef:    result.DeletedRef,
		Root:          result.Root,
		Path:          result.Path,
		Title:         result.Title,
		Description:   result.Description,
		Tags:          append([]string(nil), result.Tags...),
		CreatedDelta:  modelDelta(result.CreatedAt, now),
		UpdatedDelta:  modelDelta(result.UpdatedAt, now),
		ModifiedDelta: modelDelta(result.ModifiedAt, now),
		WordCount:     result.WordCount,
		SizeBytes:     result.SizeBytes,
	}
}

func toModelMoveResult(result *MoveResult, now time.Time) *modelMoveResult {
	if result == nil {
		return nil
	}
	return &modelMoveResult{
		Action:        result.Action,
		FromRef:       result.FromRef,
		ToRef:         result.ToRef,
		FromRoot:      result.FromRoot,
		FromPath:      result.FromPath,
		ToRoot:        result.ToRoot,
		ToPath:        result.ToPath,
		Overwrote:     result.Overwrote,
		Title:         result.Title,
		Description:   result.Description,
		Tags:          append([]string(nil), result.Tags...),
		CreatedDelta:  modelDelta(result.CreatedAt, now),
		UpdatedDelta:  modelDelta(result.UpdatedAt, now),
		ModifiedDelta: modelDelta(result.ModifiedAt, now),
		WordCount:     result.WordCount,
		SizeBytes:     result.SizeBytes,
	}
}

func toModelCopyResult(result *CopyResult, now time.Time) *modelCopyResult {
	if result == nil {
		return nil
	}
	return &modelCopyResult{
		Action:        result.Action,
		FromRef:       result.FromRef,
		ToRef:         result.ToRef,
		FromRoot:      result.FromRoot,
		FromPath:      result.FromPath,
		ToRoot:        result.ToRoot,
		ToPath:        result.ToPath,
		Overwrote:     result.Overwrote,
		Title:         result.Title,
		Description:   result.Description,
		Tags:          append([]string(nil), result.Tags...),
		CreatedDelta:  modelDelta(result.CreatedAt, now),
		UpdatedDelta:  modelDelta(result.UpdatedAt, now),
		ModifiedDelta: modelDelta(result.ModifiedAt, now),
		WordCount:     result.WordCount,
		SizeBytes:     result.SizeBytes,
	}
}

func toModelSectionTransferResult(result *SectionTransferResult, now time.Time) *modelSectionTransferResult {
	if result == nil {
		return nil
	}
	return &modelSectionTransferResult{
		Action:             result.Action,
		SourceRef:          result.SourceRef,
		SourceRoot:         result.SourceRoot,
		SourcePath:         result.SourcePath,
		SourceSection:      result.SourceSection,
		DestinationRef:     result.DestinationRef,
		DestinationRoot:    result.DestinationRoot,
		DestinationPath:    result.DestinationPath,
		DestinationSection: result.DestinationSection,
		DestinationLevel:   result.DestinationLevel,
		DestinationExisted: result.DestinationExisted,
		Title:              result.Title,
		Description:        result.Description,
		Tags:               append([]string(nil), result.Tags...),
		CreatedDelta:       modelDelta(result.CreatedAt, now),
		UpdatedDelta:       modelDelta(result.UpdatedAt, now),
		ModifiedDelta:      modelDelta(result.ModifiedAt, now),
		WordCount:          result.WordCount,
		SizeBytes:          result.SizeBytes,
	}
}

func toModelCommitResult(result CommitResult, now time.Time) modelCommitResult {
	return modelCommitResult{
		IntakeID: result.IntakeID,
		Action:   result.Action,
		Ref:      result.Ref,
		Status:   result.Status,
		Result:   modelCommitPayload(result.Result, now),
	}
}

func modelCommitPayload(v any, now time.Time) any {
	switch typed := v.(type) {
	case *MutationResult:
		return toModelMutationResult(typed, now)
	case *DeleteResult:
		return toModelDeleteResult(typed, now)
	case *MoveResult:
		return toModelMoveResult(typed, now)
	case *CopyResult:
		return toModelCopyResult(typed, now)
	case *SectionTransferResult:
		return toModelSectionTransferResult(typed, now)
	default:
		return v
	}
}

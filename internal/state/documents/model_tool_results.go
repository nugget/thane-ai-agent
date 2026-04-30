package documents

import (
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

type modelRootSummary struct {
	Root              string                      `json:"root"`
	Policy            RootPolicySummary           `json:"policy"`
	Verification      *modelSignatureVerification `json:"verification,omitempty"`
	DocumentCount     int                         `json:"document_count"`
	TotalSizeBytes    int64                       `json:"total_size_bytes"`
	TotalWordCount    int                         `json:"total_word_count"`
	LastModifiedDelta string                      `json:"last_modified_delta,omitempty"`
	TopTags           []string                    `json:"top_tags,omitempty"`
	TopDirectories    []BrowseDirectory           `json:"top_directories,omitempty"`
	RecentDocuments   []modelRootDocumentHint     `json:"recent_documents,omitempty"`
}

type modelSignatureVerification struct {
	Status       SignatureStatus  `json:"status"`
	Mode         VerificationMode `json:"mode,omitempty"`
	Commit       string           `json:"commit,omitempty"`
	Message      string           `json:"message,omitempty"`
	CheckedDelta string           `json:"checked_delta,omitempty"`
	Consumer     string           `json:"consumer,omitempty"`
}

type modelRootDocumentHint struct {
	Ref           string `json:"ref"`
	Path          string `json:"path"`
	Title         string `json:"title"`
	ModifiedDelta string `json:"modified_delta,omitempty"`
}

type modelDocumentSummary struct {
	Root          string              `json:"root"`
	Ref           string              `json:"ref"`
	Path          string              `json:"path"`
	Title         string              `json:"title"`
	Summary       string              `json:"summary,omitempty"`
	Tags          []string            `json:"tags,omitempty"`
	Frontmatter   map[string][]string `json:"frontmatter,omitempty"`
	ModifiedDelta string              `json:"modified_delta,omitempty"`
	WordCount     int                 `json:"word_count"`
}

type modelSearchResult struct {
	Filters modelSearchFilters     `json:"filters"`
	Results []modelDocumentSummary `json:"results,omitempty"`
}

type modelSearchFilters struct {
	Root                string              `json:"root,omitempty"`
	PathPrefix          string              `json:"path_prefix,omitempty"`
	Query               string              `json:"query,omitempty"`
	Tags                []string            `json:"tags,omitempty"`
	Frontmatter         map[string][]string `json:"frontmatter,omitempty"`
	FrontmatterKeys     []string            `json:"frontmatter_keys,omitempty"`
	ModifiedAfterDelta  string              `json:"modified_after_delta,omitempty"`
	ModifiedBeforeDelta string              `json:"modified_before_delta,omitempty"`
	Limit               int                 `json:"limit"`
}

type modelBrowseResult struct {
	Root        string                 `json:"root"`
	PathPrefix  string                 `json:"path_prefix,omitempty"`
	Directories []BrowseDirectory      `json:"directories,omitempty"`
	Documents   []modelDocumentSummary `json:"documents,omitempty"`
}

type modelDocumentRecord struct {
	Root          string              `json:"root"`
	Ref           string              `json:"ref"`
	Path          string              `json:"path"`
	Title         string              `json:"title"`
	Description   string              `json:"description,omitempty"`
	Tags          []string            `json:"tags,omitempty"`
	Frontmatter   map[string][]string `json:"frontmatter,omitempty"`
	Body          string              `json:"body"`
	Outline       []Section           `json:"outline,omitempty"`
	ModifiedDelta string              `json:"modified_delta,omitempty"`
	WordCount     int                 `json:"word_count"`
	SizeBytes     int64               `json:"size_bytes"`
}

type modelBacklink struct {
	Ref              string   `json:"ref"`
	Path             string   `json:"path"`
	Title            string   `json:"title"`
	ModifiedDelta    string   `json:"modified_delta,omitempty"`
	Targets          []string `json:"targets,omitempty"`
	TargetsTruncated bool     `json:"targets_truncated,omitempty"`
}

type modelLinksResult struct {
	Ref                string          `json:"ref"`
	Mode               string          `json:"mode"`
	Limit              int             `json:"limit,omitempty"`
	PerBacklinkLimit   int             `json:"per_backlink_limit,omitempty"`
	Outgoing           []DocumentLink  `json:"outgoing,omitempty"`
	OutgoingTruncated  bool            `json:"outgoing_truncated,omitempty"`
	Backlinks          []modelBacklink `json:"backlinks,omitempty"`
	BacklinksTruncated bool            `json:"backlinks_truncated,omitempty"`
}

type modelValuesResult struct {
	Root   string       `json:"root,omitempty"`
	Key    string       `json:"key"`
	Values []ValueCount `json:"values,omitempty"`
}

func modelRootSummaries(roots []RootSummary, now time.Time) []modelRootSummary {
	out := make([]modelRootSummary, 0, len(roots))
	for _, root := range roots {
		out = append(out, modelRootSummary{
			Root:              root.Root,
			Policy:            root.Policy,
			Verification:      modelVerification(root.Verification, now),
			DocumentCount:     root.DocumentCount,
			TotalSizeBytes:    root.TotalSizeBytes,
			TotalWordCount:    root.TotalWordCount,
			LastModifiedDelta: modelDelta(root.LastModifiedAt, now),
			TopTags:           append([]string(nil), root.TopTags...),
			TopDirectories:    append([]BrowseDirectory(nil), root.TopDirectories...),
			RecentDocuments:   modelRootDocumentHints(root.RecentDocuments, now),
		})
	}
	return out
}

func modelVerification(v *SignatureVerification, now time.Time) *modelSignatureVerification {
	if v == nil {
		return nil
	}
	return &modelSignatureVerification{
		Status:       v.Status,
		Mode:         v.Mode,
		Commit:       v.Commit,
		Message:      v.Message,
		CheckedDelta: modelDelta(v.CheckedAt, now),
		Consumer:     v.Consumer,
	}
}

func modelRootDocumentHints(docs []RootDocumentHint, now time.Time) []modelRootDocumentHint {
	out := make([]modelRootDocumentHint, 0, len(docs))
	for _, doc := range docs {
		out = append(out, modelRootDocumentHint{
			Ref:           doc.Ref,
			Path:          doc.Path,
			Title:         doc.Title,
			ModifiedDelta: modelDelta(doc.ModifiedAt, now),
		})
	}
	return out
}

func modelDocumentSummaries(docs []DocumentSummary, now time.Time) []modelDocumentSummary {
	out := make([]modelDocumentSummary, 0, len(docs))
	for _, doc := range docs {
		out = append(out, toModelDocumentSummary(doc, now))
	}
	return out
}

func toModelDocumentSummary(doc DocumentSummary, now time.Time) modelDocumentSummary {
	return modelDocumentSummary{
		Root:          doc.Root,
		Ref:           doc.Ref,
		Path:          doc.Path,
		Title:         doc.Title,
		Summary:       doc.Summary,
		Tags:          append([]string(nil), doc.Tags...),
		Frontmatter:   modelFrontmatter(doc.Frontmatter, now),
		ModifiedDelta: modelDelta(doc.ModifiedAt, now),
		WordCount:     doc.WordCount,
	}
}

func toModelSearchResult(args SearchArgs, query SearchQuery, results []DocumentSummary, now time.Time) modelSearchResult {
	return modelSearchResult{
		Filters: modelSearchFilters{
			Root:                query.Root,
			PathPrefix:          query.PathPrefix,
			Query:               query.Query,
			Tags:                append([]string(nil), query.Tags...),
			Frontmatter:         modelFrontmatter(query.Frontmatter, now),
			FrontmatterKeys:     modelFrontmatterKeys(query.FrontmatterKeys),
			ModifiedAfterDelta:  modelInputDelta(args.ModifiedAfter, now),
			ModifiedBeforeDelta: modelInputDelta(args.ModifiedBefore, now),
			Limit:               query.Limit,
		},
		Results: modelDocumentSummaries(results, now),
	}
}

func toModelBrowseResult(result *BrowseResult, now time.Time) *modelBrowseResult {
	if result == nil {
		return nil
	}
	return &modelBrowseResult{
		Root:        result.Root,
		PathPrefix:  result.PathPrefix,
		Directories: append([]BrowseDirectory(nil), result.Directories...),
		Documents:   modelDocumentSummaries(result.Documents, now),
	}
}

func toModelDocumentRecord(record *DocumentRecord, now time.Time) *modelDocumentRecord {
	if record == nil {
		return nil
	}
	return &modelDocumentRecord{
		Root:          record.Root,
		Ref:           record.Ref,
		Path:          record.Path,
		Title:         record.Title,
		Description:   record.Description,
		Tags:          append([]string(nil), record.Tags...),
		Frontmatter:   modelFrontmatter(record.Frontmatter, now),
		Body:          record.Body,
		Outline:       append([]Section(nil), record.Outline...),
		ModifiedDelta: modelDelta(record.ModifiedAt, now),
		WordCount:     record.WordCount,
		SizeBytes:     record.SizeBytes,
	}
}

func toModelLinksResult(result *LinksResult, now time.Time) *modelLinksResult {
	if result == nil {
		return nil
	}
	backlinks := make([]modelBacklink, 0, len(result.Backlinks))
	for _, backlink := range result.Backlinks {
		backlinks = append(backlinks, modelBacklink{
			Ref:              backlink.Ref,
			Path:             backlink.Path,
			Title:            backlink.Title,
			ModifiedDelta:    modelDelta(backlink.ModifiedAt, now),
			Targets:          append([]string(nil), backlink.Targets...),
			TargetsTruncated: backlink.TargetsTruncated,
		})
	}
	return &modelLinksResult{
		Ref:                result.Ref,
		Mode:               result.Mode,
		Limit:              result.Limit,
		PerBacklinkLimit:   result.PerBacklinkLimit,
		Outgoing:           append([]DocumentLink(nil), result.Outgoing...),
		OutgoingTruncated:  result.OutgoingTruncated,
		Backlinks:          backlinks,
		BacklinksTruncated: result.BacklinksTruncated,
	}
}

func toModelValuesResult(root string, key string, values []ValueCount, now time.Time) modelValuesResult {
	out := append([]ValueCount(nil), values...)
	if deltaKey, ok := frontmatterDeltaFieldName(key); ok {
		key = deltaKey
		out = modelDeltaValueCounts(values, now)
	}
	return modelValuesResult{
		Root:   root,
		Key:    key,
		Values: out,
	}
}

func modelDeltaValueCounts(values []ValueCount, now time.Time) []ValueCount {
	counts := make(map[string]int, len(values))
	for _, value := range values {
		delta := modelDelta(value.Value, now)
		if delta == "" {
			continue
		}
		counts[delta] += value.Count
	}
	return asValueCounts(counts)
}

func modelFrontmatter(frontmatter map[string][]string, now time.Time) map[string][]string {
	if len(frontmatter) == 0 {
		return nil
	}
	out := make(map[string][]string, len(frontmatter))
	keys := make([]string, 0, len(frontmatter))
	for key := range frontmatter {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		values := frontmatter[key]
		if deltaKey, ok := frontmatterDeltaFieldName(key); ok {
			if _, exists := out[deltaKey]; exists {
				continue
			}
			deltas := make([]string, 0, len(values))
			for _, value := range values {
				delta := modelDelta(value, now)
				if delta == "" {
					continue
				}
				deltas = append(deltas, delta)
			}
			if len(deltas) == 0 {
				continue
			}
			out[deltaKey] = deltas
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	return out
}

func frontmatterDeltaFieldName(key string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "created", "created_at":
		return "created_delta", true
	case "updated", "updated_at":
		return "updated_delta", true
	case GeneratedFieldAt:
		return "generated_delta", true
	default:
		return "", false
	}
}

func modelFrontmatterKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if deltaKey, ok := frontmatterDeltaFieldName(key); ok {
			out = append(out, deltaKey)
			continue
		}
		out = append(out, key)
	}
	return out
}

func modelInputDelta(value string, now time.Time) string {
	if value == "" {
		return ""
	}
	ts, err := promptfmt.ParseTimeOrDelta(value, now)
	if err != nil {
		return ""
	}
	return promptfmt.FormatDeltaOnly(ts, now)
}

func modelDelta(value string, now time.Time) string {
	if value == "" {
		return ""
	}
	ts, err := database.ParseTimestamp(value)
	if err != nil {
		return ""
	}
	return promptfmt.FormatDeltaOnly(ts, now)
}

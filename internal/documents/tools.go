package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/promptfmt"
)

const (
	maxToolResultBytes         = 16 * 1024
	toolResultPreviewBudget    = maxToolResultBytes - 512
	defaultDocLinksLimit       = 20
	maxDocLinksLimit           = 100
	defaultBacklinkTargetLimit = 10
	maxBacklinkTargetLimit     = 50
)

// Tools exposes model-facing document navigation tools.
type Tools struct {
	store *Store
}

// NewTools creates a document tool surface.
func NewTools(store *Store) *Tools {
	return &Tools{store: store}
}

// BrowseArgs requests one rooted browse step through an indexed corpus.
type BrowseArgs struct {
	Root       string `json:"root"`
	PathPrefix string `json:"path_prefix,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// SearchArgs requests structured document search over indexed roots.
type SearchArgs struct {
	Root            string              `json:"root,omitempty"`
	PathPrefix      string              `json:"path_prefix,omitempty"`
	Query           string              `json:"query,omitempty"`
	Tags            []string            `json:"tags,omitempty"`
	Frontmatter     map[string][]string `json:"frontmatter,omitempty"`
	FrontmatterKeys []string            `json:"frontmatter_keys,omitempty"`
	ModifiedAfter   string              `json:"modified_after,omitempty"`
	ModifiedBefore  string              `json:"modified_before,omitempty"`
	Limit           int                 `json:"limit,omitempty"`
}

// RefArgs identifies one managed document by canonical semantic ref.
type RefArgs struct {
	Ref string `json:"ref"`
}

// SectionArgs selects one document section by ref and optional heading.
type SectionArgs struct {
	Ref     string `json:"ref"`
	Section string `json:"section,omitempty"`
}

// ValuesArgs requests observed values for one frontmatter key.
type ValuesArgs struct {
	Root  string `json:"root,omitempty"`
	Key   string `json:"key"`
	Limit int    `json:"limit,omitempty"`
}

// LinksArgs requests outgoing links, backlinks, or both for one document.
type LinksArgs struct {
	Ref              string `json:"ref"`
	Mode             string `json:"mode,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	PerBacklinkLimit int    `json:"per_backlink_limit,omitempty"`
}

// Read returns one indexed document payload.
func (t *Tools) Read(ctx context.Context, args RefArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	doc, err := t.store.Read(ctx, args.Ref)
	if err != nil {
		return "", err
	}
	return marshalToolResult(doc)
}

// Roots returns summaries of the indexed document roots.
func (t *Tools) Roots(ctx context.Context) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	roots, err := t.store.Roots(ctx)
	if err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"roots": roots,
	})
}

// Browse returns one rooted browse step through an indexed corpus.
func (t *Tools) Browse(ctx context.Context, args BrowseArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Root == "" {
		return "", fmt.Errorf("root is required")
	}
	result, err := t.store.Browse(ctx, args.Root, args.PathPrefix, args.Limit)
	if err != nil {
		return "", err
	}
	return marshalToolResult(result)
}

// Search returns compact summaries for documents matching the structured filters.
func (t *Tools) Search(ctx context.Context, args SearchArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	query := SearchQuery{
		Root:            args.Root,
		PathPrefix:      args.PathPrefix,
		Query:           args.Query,
		Tags:            args.Tags,
		Frontmatter:     args.Frontmatter,
		FrontmatterKeys: args.FrontmatterKeys,
		Limit:           clampLimit(args.Limit, 20, 100),
	}
	now := nowUTC()
	if args.ModifiedAfter != "" {
		bound, err := promptfmt.ParseTimeOrDelta(args.ModifiedAfter, now)
		if err != nil {
			return "", fmt.Errorf("modified_after must be RFC3339 or signed delta like -604800s: %w", err)
		}
		query.ModifiedAfter = &bound
	}
	if args.ModifiedBefore != "" {
		bound, err := promptfmt.ParseTimeOrDelta(args.ModifiedBefore, now)
		if err != nil {
			return "", fmt.Errorf("modified_before must be RFC3339 or signed delta like -3600s: %w", err)
		}
		query.ModifiedBefore = &bound
	}
	if query.ModifiedAfter != nil && query.ModifiedBefore != nil && query.ModifiedAfter.After(*query.ModifiedBefore) {
		return "", fmt.Errorf("modified_after must be earlier than modified_before")
	}
	results, err := t.store.Search(ctx, query)
	if err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"filters": map[string]any{
			"root":             args.Root,
			"path_prefix":      args.PathPrefix,
			"query":            args.Query,
			"tags":             args.Tags,
			"frontmatter":      args.Frontmatter,
			"frontmatter_keys": args.FrontmatterKeys,
			"modified_after":   args.ModifiedAfter,
			"modified_before":  args.ModifiedBefore,
			"limit":            query.Limit,
		},
		"results": results,
	})
}

// Outline returns the heading tree for one indexed document.
func (t *Tools) Outline(ctx context.Context, args RefArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	outline, err := t.store.Outline(ctx, args.Ref)
	if err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"ref":     args.Ref,
		"outline": outline,
	})
}

// Section returns one named section, or the whole body when no selector is given.
func (t *Tools) Section(ctx context.Context, args SectionArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	section, err := t.store.Section(ctx, args.Ref, args.Section)
	if err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"ref":     args.Ref,
		"section": section,
	})
}

// Values returns observed frontmatter values for one key.
func (t *Tools) Values(ctx context.Context, args ValuesArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	values, err := t.store.Values(ctx, args.Root, args.Key, args.Limit)
	if err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"root":   args.Root,
		"key":    args.Key,
		"values": values,
	})
}

// Links returns outgoing links, backlinks, or both for one indexed document.
func (t *Tools) Links(ctx context.Context, args LinksArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	links, err := t.store.Links(
		ctx,
		args.Ref,
		args.Mode,
		clampPositiveLimit(args.Limit, defaultDocLinksLimit, maxDocLinksLimit),
		clampPositiveLimit(args.PerBacklinkLimit, defaultBacklinkTargetLimit, maxBacklinkTargetLimit),
	)
	if err != nil {
		return "", err
	}
	return marshalToolResult(links)
}

// Write creates or replaces one managed document.
func (t *Tools) Write(ctx context.Context, args WriteArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	result, err := t.store.Write(ctx, args)
	if err != nil {
		return "", err
	}
	return marshalToolResult(result)
}

// Edit applies one structured edit to a managed document.
func (t *Tools) Edit(ctx context.Context, args EditArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	result, err := t.store.Edit(ctx, args)
	if err != nil {
		return "", err
	}
	return marshalToolResult(result)
}

// JournalUpdate appends one journal-window entry to a managed document.
func (t *Tools) JournalUpdate(ctx context.Context, args JournalUpdateArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	result, err := t.store.JournalUpdate(ctx, args)
	if err != nil {
		return "", err
	}
	return marshalToolResult(result)
}

// Delete removes one managed document.
func (t *Tools) Delete(ctx context.Context, args DeleteArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	result, err := t.store.Delete(ctx, args)
	if err != nil {
		return "", err
	}
	return marshalToolResult(result)
}

// Move relocates one managed document to a new semantic ref.
func (t *Tools) Move(ctx context.Context, args MoveArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if args.DestinationRef == "" {
		return "", fmt.Errorf("destination_ref is required")
	}
	result, err := t.store.Move(ctx, args)
	if err != nil {
		return "", err
	}
	return marshalToolResult(result)
}

// Copy clones one managed document to a new semantic ref.
func (t *Tools) Copy(ctx context.Context, args CopyArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if args.DestinationRef == "" {
		return "", fmt.Errorf("destination_ref is required")
	}
	result, err := t.store.Copy(ctx, args)
	if err != nil {
		return "", err
	}
	return marshalToolResult(result)
}

// CopySection copies one section into another managed document.
func (t *Tools) CopySection(ctx context.Context, args SectionTransferArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if args.Section == "" {
		return "", fmt.Errorf("section is required")
	}
	if args.DestinationRef == "" {
		return "", fmt.Errorf("destination_ref is required")
	}
	result, err := t.store.CopySection(ctx, args)
	if err != nil {
		return "", err
	}
	return marshalToolResult(result)
}

// MoveSection moves one section into another managed document.
func (t *Tools) MoveSection(ctx context.Context, args SectionTransferArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if args.Section == "" {
		return "", fmt.Errorf("section is required")
	}
	if args.DestinationRef == "" {
		return "", fmt.Errorf("destination_ref is required")
	}
	result, err := t.store.MoveSection(ctx, args)
	if err != nil {
		return "", err
	}
	return marshalToolResult(result)
}

func marshalToolResult(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal document tool result: %w", err)
	}
	if len(data) > maxToolResultBytes {
		preview := truncateUTF8Bytes(data, toolResultPreviewBudget)
		data, err = json.MarshalIndent(map[string]any{
			"truncated":   true,
			"bytes_total": len(data),
			"bytes_shown": len(preview),
			"note":        fmt.Sprintf("result exceeded %d bytes; narrow the request by lowering limit, specifying root/path_prefix, or selecting a section", maxToolResultBytes),
			"preview":     preview,
		}, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal truncated document tool result: %w", err)
		}
	}
	return string(data), nil
}

func truncateUTF8Bytes(data []byte, maxBytes int) string {
	if len(data) <= maxBytes {
		return string(data)
	}
	truncated := data[:maxBytes]
	for len(truncated) > 0 && !utf8.Valid(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return string(truncated)
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

func clampPositiveLimit(limit int, def int, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

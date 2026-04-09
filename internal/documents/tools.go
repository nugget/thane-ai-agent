package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const (
	maxToolResultBytes      = 16 * 1024
	toolResultPreviewBudget = maxToolResultBytes - 512
)

// Tools exposes model-facing document navigation tools.
type Tools struct {
	store *Store
}

// NewTools creates a document tool surface.
func NewTools(store *Store) *Tools {
	return &Tools{store: store}
}

type BrowseArgs struct {
	Root       string `json:"root"`
	PathPrefix string `json:"path_prefix,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type SearchArgs struct {
	Root       string   `json:"root,omitempty"`
	PathPrefix string   `json:"path_prefix,omitempty"`
	Query      string   `json:"query,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Limit      int      `json:"limit,omitempty"`
}

type RefArgs struct {
	Ref string `json:"ref"`
}

type SectionArgs struct {
	Ref     string `json:"ref"`
	Section string `json:"section,omitempty"`
}

type ValuesArgs struct {
	Root  string `json:"root,omitempty"`
	Key   string `json:"key"`
	Limit int    `json:"limit,omitempty"`
}

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

func (t *Tools) Search(ctx context.Context, args SearchArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	results, err := t.store.Search(ctx, SearchQuery(args))
	if err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"results": results,
	})
}

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

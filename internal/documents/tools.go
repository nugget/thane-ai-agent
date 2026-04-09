package documents

import (
	"context"
	"encoding/json"
	"fmt"
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
	}), nil
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
	return marshalToolResult(result), nil
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
	}), nil
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
	}), nil
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
	}), nil
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
	}), nil
}

func marshalToolResult(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

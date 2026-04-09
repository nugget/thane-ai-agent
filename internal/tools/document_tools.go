package tools

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/documents"
)

// RegisterDocumentTools adds indexed document navigation tools to the registry.
func RegisterDocumentTools(r *Registry, dt *documents.Tools) {
	if r == nil || dt == nil {
		return
	}

	r.Register(&Tool{
		Name:                 "doc_read",
		Description:          "Read one managed markdown document by semantic ref like `kb:article.md`. Returns frontmatter, body, outline, and derived metadata in one payload. Large documents may be truncated by tool output limits, so use `doc_outline` plus `doc_section` when you need to navigate or read larger documents in full.",
		ContentResolveExempt: []string{"ref"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Canonical document ref like `kb:network/vlans.md`.",
				},
			},
			"required": []string{"ref"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			return dt.Read(ctx, documents.RefArgs{Ref: ref})
		},
	})

	r.Register(&Tool{
		Name:        "doc_roots",
		Description: "List indexed markdown roots with helpful corpus summaries such as document counts, total size, last modification, top tags, top directories, and recent example documents. Use first when the answer probably lives in a managed document corpus but you do not yet know which root to browse.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return dt.Roots(ctx)
		},
	})

	r.Register(&Tool{
		Name:        "doc_browse",
		Description: "Browse one indexed markdown root like a phone tree. Returns the immediate child directories and documents for a root/path prefix so you can navigate the corpus without brute-force file walking.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root": map[string]any{
					"type":        "string",
					"description": "Indexed root name without the trailing colon, for example `kb`, `scratchpad`, `generated`, or `core`.",
				},
				"path_prefix": map[string]any{
					"type":        "string",
					"description": "Optional relative directory prefix inside the root, such as `network` or `network/unifi`.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of directories and direct documents to return from this browse step (default 20, max 100).",
				},
			},
			"required": []string{"root"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			root, _ := args["root"].(string)
			if root == "" {
				return "", fmt.Errorf("root is required")
			}
			pathPrefix, _ := args["path_prefix"].(string)
			limit := numericArg(args["limit"], 20, 100)
			return dt.Browse(ctx, documents.BrowseArgs{
				Root:       root,
				PathPrefix: pathPrefix,
				Limit:      limit,
			})
		},
	})

	r.Register(&Tool{
		Name:        "doc_search",
		Description: "Search indexed markdown documents by root, path prefix, query, and tags. Returns compact document summaries with canonical refs like `kb:article.md`, not full bodies.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root": map[string]any{
					"type":        "string",
					"description": "Optional indexed root name without trailing colon. Omit to search across all indexed roots.",
				},
				"path_prefix": map[string]any{
					"type":        "string",
					"description": "Optional relative directory prefix inside the root.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Search text matched against title, path, summary, and tags.",
				},
				"tags": map[string]any{
					"type":        "array",
					"description": "Optional tags that all matching documents must contain.",
					"items": map[string]any{
						"type": "string",
					},
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results to return (default 20, max 100).",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			root, _ := args["root"].(string)
			pathPrefix, _ := args["path_prefix"].(string)
			query, _ := args["query"].(string)
			tags := documentStringSliceArg(args["tags"])
			limit := numericArg(args["limit"], 20, 100)
			return dt.Search(ctx, documents.SearchArgs{
				Root:       root,
				PathPrefix: pathPrefix,
				Query:      query,
				Tags:       tags,
				Limit:      limit,
			})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_outline",
		Description:          "Return the heading tree for one indexed markdown document. Use after doc_search or doc_browse when you know the document ref and need the structural map before reading a section.",
		ContentResolveExempt: []string{"ref"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Canonical document ref like `kb:network/vlans.md`.",
				},
			},
			"required": []string{"ref"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			return dt.Outline(ctx, documents.RefArgs{Ref: ref})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_section",
		Description:          "Return one named section from an indexed markdown document by heading text or slug. If `section` is omitted, returns the full document body without frontmatter.",
		ContentResolveExempt: []string{"ref", "section"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Canonical document ref like `kb:network/vlans.md`.",
				},
				"section": map[string]any{
					"type":        "string",
					"description": "Heading text or slug to retrieve, for example `Driveway Camera Notes` or `driveway-camera-notes`.",
				},
			},
			"required": []string{"ref"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			section, _ := args["section"].(string)
			return dt.Section(ctx, documents.SectionArgs{Ref: ref, Section: section})
		},
	})

	r.Register(&Tool{
		Name:        "doc_values",
		Description: "List observed values for one frontmatter key across indexed roots, especially keys like `tags`, `status`, or `area`. Use this to discover the corpus vocabulary before narrowing a search.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root": map[string]any{
					"type":        "string",
					"description": "Optional indexed root name without trailing colon. Omit to aggregate across all roots.",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Frontmatter key to inspect, for example `tags`, `status`, or `area`.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of values to return (default 20).",
				},
			},
			"required": []string{"key"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			if key == "" {
				return "", fmt.Errorf("key is required")
			}
			root, _ := args["root"].(string)
			limit := numericArg(args["limit"], 20, 100)
			return dt.Values(ctx, documents.ValuesArgs{Root: root, Key: key, Limit: limit})
		},
	})
	registerDocumentMutationTools(r, dt)
}

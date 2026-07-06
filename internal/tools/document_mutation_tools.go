package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func registerDocumentMutationTools(r *Registry, dt *documents.Tools) {
	registerDocumentLifecycleTools(r, dt)

	r.Register(&Tool{
		Name:                 "doc_write",
		Description:          "Write (replace) a managed markdown document by semantic ref like `kb:article.md`. This tool owns frontmatter integrity for title, description, tags, created, and updated timestamps, and it can append a stamped entry to a standard `Journal` section so the model can think in documents instead of filesystem paths. For brand-new documents prefer doc_create, which collision-checks the corpus and normalizes placement first — doc_write creating a fresh ref is for destinations that are already deliberate.",
		ContentResolveExempt: []string{"ref", "title", "description", "tags", "frontmatter"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Canonical document ref like `kb:network/vlans.md`.",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Optional title frontmatter override.",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional description frontmatter override.",
				},
				"tags": map[string]any{
					"type":        "array",
					"description": "Optional tags frontmatter override.",
					"items":       map[string]any{"type": "string"},
				},
				"frontmatter": map[string]any{
					"type":                 "object",
					"description":          "Optional extra frontmatter fields. Values may be strings or arrays of strings.",
					"additionalProperties": true,
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Markdown body content to write. Omit to preserve an existing document's body; pass an empty string to intentionally clear it. Creating a new document requires body. Every document tool's markdown parameter is named body.",
				},
				"journal_entry": map[string]any{
					"type":        "string",
					"description": "Optional timestamped note to append under the managed `Journal` section while writing the current document state.",
				},
			},
			"required": []string{"ref"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			// Confusable-parameter guards: doc_edit's vocabulary on
			// doc_write previously vanished silently — the unknown key was
			// ignored, an empty document was written, and success was
			// returned. A prod archivist run lost three dossiers this way
			// (the model sent content + mode: replace_body). Fail fast
			// with a redirect so the model self-corrects on retry.
			if _, hasContent := args["content"]; hasContent {
				return "", fmt.Errorf("doc_write has no %q parameter — markdown goes in %q (every document tool takes body). Re-call with body", "content", "body")
			}
			if _, hasMode := args["mode"]; hasMode {
				return "", fmt.Errorf("doc_write has no %q parameter — it always creates or replaces the whole document. For mode-based edits (replace_body, append_body, upsert_section, ...) use doc_edit", "mode")
			}
			title, _ := args["title"].(string)
			description, _ := args["description"].(string)
			return dt.Write(ctx, documents.WriteArgs{
				Ref:          ref,
				Title:        title,
				Description:  description,
				Tags:         documentStringSliceArg(args["tags"]),
				Frontmatter:  documentFrontmatterArg(args["frontmatter"]),
				Body:         optionalStringArg(args, "body"),
				JournalEntry: stringArg(args, "journal_entry"),
			})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_edit",
		Description:          "Edit a managed markdown document without leaving semantic refs behind. Supports metadata-only updates, whole-body replacement, body append/prepend, and section-aware upsert/delete operations.",
		ContentResolveExempt: []string{"ref", "mode", "section", "heading", "level", "title", "description", "tags", "frontmatter"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Canonical document ref like `kb:network/vlans.md`.",
				},
				"mode": map[string]any{
					"type":        "string",
					"description": "Edit mode: `metadata`, `replace_body`, `append_body`, `prepend_body`, `upsert_section`, or `delete_section`.",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Markdown text for the edit — the same parameter name doc_write uses. For the body modes this is the document's new body (whole or appended/prepended text); for upsert_section it is only that one section's text — never the whole document.",
				},
				"section": map[string]any{
					"type":        "string",
					"description": "Existing section heading or slug to target. Required for section edits and deletes.",
				},
				"heading": map[string]any{
					"type":        "string",
					"description": "Optional heading text for a newly inserted section. Defaults to `section`.",
				},
				"level": map[string]any{
					"type":        "integer",
					"description": "Heading level for `upsert_section` (default 2).",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Optional title frontmatter update.",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional description frontmatter update.",
				},
				"tags": map[string]any{
					"type":        "array",
					"description": "Optional tags frontmatter update.",
					"items":       map[string]any{"type": "string"},
				},
				"frontmatter": map[string]any{
					"type":                 "object",
					"description":          "Optional extra frontmatter fields. Values may be strings or arrays of strings.",
					"additionalProperties": true,
				},
			},
			"required": []string{"ref", "mode"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			mode, _ := args["mode"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			if mode == "" {
				return "", fmt.Errorf("mode is required")
			}
			// Rename guard: doc_edit's text parameter was unified with
			// doc_write's as body (the content/body split silently ate a
			// write's markdown — see doc_write's guard). Teach the rename
			// instead of silently ignoring the old key.
			if _, hasContent := args["content"]; hasContent {
				return "", fmt.Errorf("doc_edit's markdown parameter is %q (the %q parameter was renamed for consistency with doc_write) — re-call with body", "body", "content")
			}
			content, _ := args["body"].(string)
			section, _ := args["section"].(string)
			heading, _ := args["heading"].(string)
			title, _ := args["title"].(string)
			description, _ := args["description"].(string)
			return dt.Edit(ctx, documents.EditArgs{
				Ref:         ref,
				Mode:        mode,
				Body:        content,
				Section:     section,
				Heading:     heading,
				Level:       numericArg(args["level"], 2, 6),
				Title:       title,
				Description: description,
				Tags:        documentStringSliceArg(args["tags"]),
				Frontmatter: documentFrontmatterArg(args["frontmatter"]),
			})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_journal_update",
		Description:          "Append a timestamped note into a rolling managed journal document. The tool creates the document if needed, keeps created/updated timestamps current, groups entries by day/week/month window headings, and prunes older windows for you.",
		ContentResolveExempt: []string{"ref", "window", "max_windows", "heading_level", "title", "description", "tags", "frontmatter"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Canonical journal document ref like `kb:metacog/journal.md`.",
				},
				"entry": map[string]any{
					"type":        "string",
					"description": "Journal note content to append under the current rolling window.",
				},
				"window": map[string]any{
					"type":        "string",
					"description": "Window size for grouping entries: `day`, `week`, or `month` (default `day`).",
				},
				"max_windows": map[string]any{
					"type":        "integer",
					"description": "How many recent windows to retain before pruning older ones.",
				},
				"heading_level": map[string]any{
					"type":        "integer",
					"description": "Heading level for window sections (default 2).",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Optional bootstrap title when the journal document does not exist yet.",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional bootstrap description when the journal document does not exist yet.",
				},
				"tags": map[string]any{
					"type":        "array",
					"description": "Optional bootstrap tags for a new journal document.",
					"items":       map[string]any{"type": "string"},
				},
				"frontmatter": map[string]any{
					"type":                 "object",
					"description":          "Optional extra frontmatter fields. Values may be strings or arrays of strings.",
					"additionalProperties": true,
				},
			},
			"required": []string{"ref", "entry"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			entry, _ := args["entry"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			if entry == "" {
				return "", fmt.Errorf("entry is required")
			}
			window, _ := args["window"].(string)
			title, _ := args["title"].(string)
			description, _ := args["description"].(string)
			return dt.JournalUpdate(ctx, documents.JournalUpdateArgs{
				Ref:          ref,
				Entry:        entry,
				Window:       window,
				MaxWindows:   numericArg(args["max_windows"], 0, 365),
				HeadingLevel: numericArg(args["heading_level"], 2, 6),
				Title:        title,
				Description:  description,
				Tags:         documentStringSliceArg(args["tags"]),
				Frontmatter:  documentFrontmatterArg(args["frontmatter"]),
			})
		},
	})
}

func numericArg(v any, def, max int) int {
	n, ok := numericValue(v)
	if !ok || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func numericValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float32:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
		if f, err := strconv.ParseFloat(string(n), 64); err == nil {
			return int(f), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func optionalStringArg(args map[string]any, key string) *string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	s, _ := v.(string)
	return &s
}

func documentStringSliceArg(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func documentFrontmatterArg(v any) map[string][]string {
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string][]string, len(raw))
	for key, value := range raw {
		key = documentFrontmatterKey(key)
		if key == "" {
			continue
		}
		switch typed := value.(type) {
		case string:
			typed = strings.TrimSpace(typed)
			if typed != "" {
				out[key] = []string{typed}
			}
		case []any:
			values := make([]string, 0, len(typed))
			for _, item := range typed {
				if s, ok := item.(string); ok {
					s = strings.TrimSpace(s)
					if s != "" {
						values = append(values, s)
					}
				}
			}
			if len(values) > 0 {
				out[key] = values
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func documentFrontmatterKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return ""
	}
	return key
}

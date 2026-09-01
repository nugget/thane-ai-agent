package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

func registerDocumentMutationTools(r *Registry, dt *documents.Tools) {
	registerDocumentLifecycleTools(r, dt)

	r.Register(&Tool{
		Name:                 documents.DocumentBodyWriteToolName,
		Description:          "Write one undifferentiated Markdown body for the unusual managed document that intentionally has no projection ladder. This is not a filesystem bypass: Thane still owns metadata, root policy, revision protection, and Git. Most documents belong on doc_write instead. This tool cannot write or mutate a faceted document; doc_read and doc_search return the exact write_tool for an existing target. New documents in the reserved dossiers root must be direct children; existing nested legacy refs remain writable.",
		ContentResolveExempt: []string{"ref", "title", "description", "tags", "frontmatter"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Canonical document ref like `kb:network/vlans.md`. A new `dossiers:` document must be a direct child; an existing nested legacy ref remains writable.",
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
					"description": "Markdown body content to write. Omit to preserve an existing document's body; pass an empty string to intentionally clear it. Creating a new document requires body. Ordinary content-mutation tools name their markdown parameter body; structured publish tools expose projections such as full instead.",
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
			// Confusable-parameter guards: doc_edit's vocabulary on a body
			// write previously vanished silently — the unknown key was
			// ignored, an empty document was written, and success was
			// returned. A prod archivist run lost three dossiers this way
			// (the model sent content + mode: replace_body). Fail fast
			// with a redirect so the model self-corrects on retry.
			if _, hasContent := args["content"]; hasContent {
				return "", fmt.Errorf("%s has no %q parameter; body-only Markdown goes in %q. Re-call with body, or use doc_write for projections", documents.DocumentBodyWriteToolName, "content", "body")
			}
			if _, hasMode := args["mode"]; hasMode {
				return "", fmt.Errorf("%s has no %q parameter; it always creates or replaces the whole body. For mode-based body edits use doc_edit", documents.DocumentBodyWriteToolName, "mode")
			}
			if _, hasRevision := args["expected_revision"]; hasRevision {
				return "", fmt.Errorf("%s has no %q parameter; Thane tracks revision preconditions automatically. Omit it and read an existing document before replacement", documents.DocumentBodyWriteToolName, "expected_revision")
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
				ReceiptScope: documentRevisionScope(ctx),
			})
		},
	})

	registerDocumentWriteTool(r, dt)

	r.Register(&Tool{
		Name:                 "doc_edit",
		Description:          "Edit a body-only managed Markdown document without leaving semantic refs behind. Faceted documents use doc_write or the narrower write_tool returned by doc_read/doc_search because a section edit could leave compact projections stale. This supports metadata-only updates, whole-body replacement, body append/prepend, and section-aware upsert/delete operations for the exceptional body-only shape.",
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
					"description": "Markdown text for the body-only edit — the same parameter name doc_body_write uses. For the body modes this is the document's new body (whole or appended/prepended text); for upsert_section it is only that one section's text — never the whole document, and never the section's heading line (the heading is rendered automatically from `section`/`heading`; a leading duplicate heading line is stripped).",
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
			// doc_body_write's as body (the content/body split silently ate a
			// write's markdown — see doc_write's guard). Teach the rename
			// instead of silently ignoring the old key.
			if _, hasContent := args["content"]; hasContent {
				return "", fmt.Errorf("doc_edit's markdown parameter is %q (the %q parameter was renamed for consistency with doc_body_write) — re-call with body", "body", "content")
			}
			if _, hasRevision := args["expected_revision"]; hasRevision {
				return "", fmt.Errorf("doc_edit has no %q parameter — Thane tracks revision preconditions automatically. Omit it and retry", "expected_revision")
			}
			content, _ := args["body"].(string)
			section, _ := args["section"].(string)
			heading, _ := args["heading"].(string)
			title, _ := args["title"].(string)
			description, _ := args["description"].(string)
			return dt.Edit(ctx, documents.EditArgs{
				Ref:          ref,
				Mode:         mode,
				Body:         content,
				Section:      section,
				Heading:      heading,
				Level:        numericArg(args["level"], 2, 6),
				Title:        title,
				Description:  description,
				Tags:         documentStringSliceArg(args["tags"]),
				Frontmatter:  documentFrontmatterArg(args["frontmatter"]),
				ReceiptScope: documentRevisionScope(ctx),
			})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_journal_update",
		Description:          "Append a timestamped note into an ordinary rolling managed journal document. Faceted and contract-owned documents use the write_tool returned by doc_read/doc_search instead because every projection must move together. For ordinary journals, the tool creates the document if needed, keeps created/updated timestamps current, groups entries by day/week/month window headings, and prunes older windows for you. On revision-backed roots Thane automatically protects the update against intervening edits; a conflict makes no change and returns a bounded diff to reconcile before retrying.",
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
			if _, hasRevision := args["expected_revision"]; hasRevision {
				return "", fmt.Errorf("doc_journal_update has no %q parameter — Thane tracks revision preconditions automatically. Omit it and retry", "expected_revision")
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
				ReceiptScope: documentRevisionScope(ctx),
			})
		},
	})
}

func registerDocumentWriteTool(r *Registry, dt *documents.Tools) {
	properties := map[string]any{
		"ref": map[string]any{
			"type":        "string",
			"description": "Canonical document ref like `kb:network/vlans.md`. The same tool works across every managed root; a new `dossiers:` document must be a direct child, while an existing nested legacy ref remains writable.",
		},
		"title": map[string]any{
			"type":        "string",
			"description": "Optional title frontmatter override. Omit to preserve an existing title or derive a new document's title from its ref.",
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
	}
	for _, key := range documentfacets.Keys() {
		field, ok := documentfacets.FieldByKey(key)
		if !ok {
			continue
		}
		description := field.Guidance + documentfacets.FormatGuidance(field.Format)
		if field.MaxRunes > 0 {
			description = fmt.Sprintf("%s Maximum %d characters — a ceiling, not a target; compose comfortably under it.", description, field.MaxRunes)
		}
		if key == "teaser" || key == "digest" {
			description += " Optional when first adopting a document, but required on every later publish if the document already carries this projection."
		}
		properties[key] = map[string]any{
			"type":        "string",
			"description": description,
		}
	}
	allowed := make(map[string]struct{}, len(properties))
	for key := range properties {
		allowed[key] = struct{}{}
	}

	r.Register(&Tool{
		Name:               documents.DocumentWriteToolName,
		Description:        "Create, adopt, or update the normal managed document as logical projections in any document root. Pass status_line, optional teaser/digest, and full as separate fields; Thane validates them together, stores its own durable manifest and Markdown codec, and protects revision-backed writes automatically. A legacy or body-only document self-migrates on this first structured write. New documents in the reserved dossiers root must be direct children; existing nested legacy refs remain writable. Read an existing document first and preserve every projection it already publishes. If doc_read names a narrower owner such as contact_dossier_write or publish_output_*, use that tool instead.",
		SkipContentResolve: true,
		Parameters: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   []string{"ref", "status_line", "full"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			unexpected := make([]string, 0)
			for key := range args {
				if _, ok := allowed[key]; !ok {
					unexpected = append(unexpected, key)
				}
			}
			if len(unexpected) > 0 {
				sort.Strings(unexpected)
				return "", fmt.Errorf("doc_write accepts only ref, title, description, tags, status_line, teaser, digest, and full; remove unsupported parameter(s) [%s]", strings.Join(unexpected, ", "))
			}
			return dt.Publish(ctx, documents.PublishArgs{
				Ref:          stringArg(args, "ref"),
				Title:        stringArg(args, "title"),
				Description:  stringArg(args, "description"),
				Tags:         documentStringSliceArg(args["tags"]),
				StatusLine:   stringArg(args, "status_line"),
				Teaser:       optionalStringArg(args, "teaser"),
				Digest:       optionalStringArg(args, "digest"),
				Full:         stringArg(args, "full"),
				ReceiptScope: documentRevisionScope(ctx),
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

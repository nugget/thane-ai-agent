package tools

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func registerDocumentIntakeTools(r *Registry, dt *documents.Tools) {
	r.Register(&Tool{
		Name: "doc_create",
		Description: "Create a new managed markdown document safely — the default way to make a document exist. Runs the corpus-aware placement analysis (related-document search, title/tags/path normalization, root policy) and, when placement is clean, writes the document in the same call. " +
			"When a similar document already exists or policy wants review, nothing is written: the result comes back created=false with the analysis and an intake_id for doc_commit. " +
			"Prefer this over doc_write for any brand-new document; doc_write's create is for destinations that are already deliberate.",
		ContentResolveExempt: []string{"root", "title", "ref", "tags", "path_prefix"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root": map[string]any{
					"type":        "string",
					"description": "Target managed root without trailing colon, such as `kb` or `scratchpad`. Required unless only one document root exists.",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Complete markdown body for the new document, written when placement is clean (outer whitespace is trimmed; frontmatter is managed by the tool).",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Optional title hint; the tool may normalize it against corpus conventions.",
				},
				"ref": map[string]any{
					"type":        "string",
					"description": "Optional explicit document ref such as `kb:network/unifi/vlans.md`. Use only when the destination is already intentional.",
				},
				"tags": map[string]any{
					"type":        "array",
					"description": "Optional desired tags, normalized against observed corpus vocabulary.",
					"items":       map[string]any{"type": "string"},
				},
				"path_prefix": map[string]any{
					"type":        "string",
					"description": "Optional directory hint inside the root, such as `network/unifi`.",
				},
				"intent": map[string]any{
					"type":        "string",
					"description": "Optional note on what the document is for; improves placement analysis.",
				},
			},
			"required": []string{"body"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			// The vocabulary invariant: every document tool's markdown
			// parameter is named body (#1201).
			if _, hasContent := args["content"]; hasContent {
				return "", fmt.Errorf("doc_create has no %q parameter — markdown goes in %q (every document tool takes body)", "content", "body")
			}
			body, _ := args["body"].(string)
			root, _ := args["root"].(string)
			title, _ := args["title"].(string)
			ref, _ := args["ref"].(string)
			pathPrefix, _ := args["path_prefix"].(string)
			intent, _ := args["intent"].(string)
			return dt.Create(ctx, documents.CreateArgs{
				Root:         root,
				Body:         body,
				DesiredTitle: title,
				DesiredRef:   ref,
				Tags:         documentStringSliceArg(args["tags"]),
				PathPrefix:   pathPrefix,
				Intent:       intent,
			})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_intake",
		Description:          "Analyze where proposed new knowledge belongs in a managed markdown corpus before writing it — the deliberate two-step flow. It searches related documents, normalizes title/tags/path, checks root policy, and returns an intake_id plus a commit_plan for doc_commit. For the common create case, doc_create runs this analysis and commits in one call; reach for doc_intake when you want to inspect the plan first or when the knowledge may belong in an existing document (update/append).",
		ContentResolveExempt: []string{"root", "desired_title", "desired_ref", "tags", "path_prefix"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"root": map[string]any{
					"type":        "string",
					"description": "Target managed root without trailing colon, such as `kb` or `scratchpad`. Required unless only one document root exists.",
				},
				"intent": map[string]any{
					"type":        "string",
					"description": "What the knowledge is for and whether you expect to create, update, append, or journal it.",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "Short semantic summary of the proposed knowledge. Required when body_snippet and content_digest are absent.",
				},
				"body_snippet": map[string]any{
					"type":        "string",
					"description": "Representative markdown snippet or full draft content for similarity and title/path inference.",
				},
				"content_digest": map[string]any{
					"type":        "string",
					"description": "Optional compact digest when the full body is too large for intake.",
				},
				"desired_title": map[string]any{
					"type":        "string",
					"description": "Optional title hint. The tool may normalize it but will not invent a path solely from the model's hint.",
				},
				"desired_ref": map[string]any{
					"type":        "string",
					"description": "Optional explicit document ref such as `kb:network/unifi/vlans.md`. Use only when the destination is already intentional.",
				},
				"tags": map[string]any{
					"type":        "array",
					"description": "Optional desired tags. Intake normalizes them against observed corpus vocabulary.",
					"items":       map[string]any{"type": "string"},
				},
				"path_prefix": map[string]any{
					"type":        "string",
					"description": "Optional directory hint inside the root, such as `network/unifi`.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			root, _ := args["root"].(string)
			intent, _ := args["intent"].(string)
			summary, _ := args["summary"].(string)
			bodySnippet, _ := args["body_snippet"].(string)
			contentDigest, _ := args["content_digest"].(string)
			desiredTitle, _ := args["desired_title"].(string)
			desiredRef, _ := args["desired_ref"].(string)
			pathPrefix, _ := args["path_prefix"].(string)
			return dt.Intake(ctx, documents.IntakeArgs{
				Root:          root,
				Intent:        intent,
				Summary:       summary,
				BodySnippet:   bodySnippet,
				ContentDigest: contentDigest,
				DesiredTitle:  desiredTitle,
				DesiredRef:    desiredRef,
				Tags:          documentStringSliceArg(args["tags"]),
				PathPrefix:    pathPrefix,
			})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_commit",
		Description:          "Commit a prior doc_intake result through managed document mutations. Pass the intake_id from doc_intake, choose the approved action, and set confirm=true when doc_intake returned a caution or when overriding its recommendation.",
		ContentResolveExempt: []string{"intake_id", "action", "section", "heading", "window", "confirm"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"intake_id": map[string]any{
					"type":        "string",
					"description": "The intake_id returned by doc_intake.",
				},
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"create_new", "update_existing", "append_existing", "draft_for_review"},
					"description": "Approved action. Omit only when accepting a ready doc_intake recommendation.",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Full markdown body, section content, or journal entry to commit. Required for create_new, update_existing, and append_existing.",
				},
				"section": map[string]any{
					"type":        "string",
					"description": "For update_existing, upsert this section instead of appending to the body.",
				},
				"heading": map[string]any{
					"type":        "string",
					"description": "Optional heading for an upserted update_existing section.",
				},
				"window": map[string]any{
					"type":        "string",
					"enum":        []string{"day", "week", "month"},
					"description": "For append_existing, journal window grouping. Default: day.",
				},
				"confirm": map[string]any{
					"type":        "boolean",
					"description": "Set true only after reconsidering a caution from doc_intake or intentionally overriding its recommendation.",
				},
			},
			"required": []string{"intake_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			intakeID, _ := args["intake_id"].(string)
			if intakeID == "" {
				return "", fmt.Errorf("intake_id is required")
			}
			action, _ := args["action"].(string)
			body, _ := args["body"].(string)
			section, _ := args["section"].(string)
			heading, _ := args["heading"].(string)
			window, _ := args["window"].(string)
			confirm, _ := args["confirm"].(bool)
			return dt.Commit(ctx, documents.CommitArgs{
				IntakeID: intakeID,
				Action:   documents.IntakeAction(action),
				Body:     body,
				Section:  section,
				Heading:  heading,
				Window:   window,
				Confirm:  confirm,
			})
		},
	})
}

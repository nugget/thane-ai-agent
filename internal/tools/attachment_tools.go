package tools

import "context"

// registerAttachmentTools adds attachment query and vision analysis
// tools to the registry.
func (r *Registry) registerAttachmentTools() {
	if r.attachmentTools == nil {
		return
	}

	r.Register(&Tool{
		Name: "attachment_list",
		Description: "List received attachments (images, documents, etc.) with optional filters. " +
			"Shows metadata including file name, type, sender, channel, and any vision description. " +
			"Use to browse what attachments have been received.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"conversation_id": map[string]any{
					"type":        "string",
					"description": "Filter to attachments from a specific conversation",
				},
				"channel": map[string]any{
					"type":        "string",
					"description": "Filter by source channel (e.g. \"signal\", \"email\")",
				},
				"sender": map[string]any{
					"type":        "string",
					"description": "Filter by sender identifier (phone number, email address)",
				},
				"content_type": map[string]any{
					"type":        "string",
					"description": "Filter by MIME type prefix (e.g. \"image/\" for all images, \"application/pdf\" for PDFs)",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum number of results (default: 20, max: 50)",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.attachmentTools.List(ctx, args)
		},
	})

	r.Register(&Tool{
		Name: "attachment_describe",
		Description: "Get or generate a vision description for an image attachment. " +
			"Returns the cached description if available, or triggers vision analysis " +
			"if not yet analyzed. Supports re-analysis with a different model or custom prompt " +
			"to improve descriptions as better models become available.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The attachment UUID to describe",
				},
				"reanalyze": map[string]any{
					"type":        "boolean",
					"description": "Force fresh analysis even if a cached description exists (default: false)",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Override the vision model for this analysis (e.g. to use a better model)",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Custom analysis prompt (e.g. \"List all text visible in this image\")",
				},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.attachmentTools.Describe(ctx, args)
		},
	})

	r.Register(&Tool{
		Name: "attachment_search",
		Description: "Search attachment metadata by text. Matches against file names, " +
			"vision descriptions, senders, and channels. Use to find specific attachments " +
			"when you know what you're looking for.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search text to match against attachment metadata",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum number of results (default: 10)",
				},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.attachmentTools.Search(ctx, args)
		},
	})
}

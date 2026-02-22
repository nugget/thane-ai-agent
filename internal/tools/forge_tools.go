package tools

import "context"

// forgeHandler is the interface satisfied by forge.Tools. It is defined
// here (rather than importing forge) to break the import cycle: forge
// imports tools for ConversationIDFromContext, so tools cannot import
// forge.
type forgeHandler interface {
	HandleIssueCreate(ctx context.Context, args map[string]any) (string, error)
	HandleIssueUpdate(ctx context.Context, args map[string]any) (string, error)
	HandleIssueGet(ctx context.Context, args map[string]any) (string, error)
	HandleIssueList(ctx context.Context, args map[string]any) (string, error)
	HandleIssueComment(ctx context.Context, args map[string]any) (string, error)
	HandlePRList(ctx context.Context, args map[string]any) (string, error)
	HandlePRGet(ctx context.Context, args map[string]any) (string, error)
	HandlePRDiff(ctx context.Context, args map[string]any) (string, error)
	HandlePRFiles(ctx context.Context, args map[string]any) (string, error)
	HandlePRCommits(ctx context.Context, args map[string]any) (string, error)
	HandlePRReviews(ctx context.Context, args map[string]any) (string, error)
	HandlePRReview(ctx context.Context, args map[string]any) (string, error)
	HandlePRReviewComment(ctx context.Context, args map[string]any) (string, error)
	HandlePRChecks(ctx context.Context, args map[string]any) (string, error)
	HandlePRMerge(ctx context.Context, args map[string]any) (string, error)
	HandleReact(ctx context.Context, args map[string]any) (string, error)
	HandleRequestReview(ctx context.Context, args map[string]any) (string, error)
	HandleSearch(ctx context.Context, args map[string]any) (string, error)
}

// SetForgeTools adds code forge tools to the registry. The handler
// must implement all forge tool operations — in practice this is
// *forge.Tools.
func (r *Registry) SetForgeTools(ft forgeHandler) {
	r.forgeTools = ft
	r.registerForgeTools()
}

func (r *Registry) registerForgeTools() {
	if r.forgeTools == nil {
		return
	}

	// --- Issues ---

	r.Register(&Tool{
		Name: "forge_issue_create",
		Description: "Create a new issue on a code forge (GitHub/Gitea). " +
			"Returns the issue number and URL.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{
					"type":        "string",
					"description": "Repository name — 'owner/repo' or just 'repo' (uses default owner)",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Issue title",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Issue body (markdown). Supports temp:LABEL references.",
				},
				"labels": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Labels to apply",
				},
				"assignees": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Usernames to assign",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Forge account name (default: primary account)",
				},
			},
			"required": []string{"repo", "title"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleIssueCreate(ctx, args)
		},
	})

	r.Register(&Tool{
		Name: "forge_issue_update",
		Description: "Update an existing issue. Only provided fields are changed. " +
			"WARNING: 'body' REPLACES the entire issue body — it does not append. " +
			"Omit body to leave it unchanged.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{
					"type":        "string",
					"description": "Repository name — 'owner/repo' or just 'repo'",
				},
				"number": map[string]any{
					"type":        "integer",
					"description": "Issue number",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "New title (omit to leave unchanged)",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "New body — REPLACES entire body (omit to leave unchanged). Supports temp:LABEL.",
				},
				"state": map[string]any{
					"type":        "string",
					"description": "New state: 'open' or 'closed'",
				},
				"labels": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "REPLACES all labels (omit to leave unchanged)",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Forge account name (default: primary)",
				},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleIssueUpdate(ctx, args)
		},
	})

	r.Register(&Tool{
		Name: "forge_issue_get",
		Description: "Get a single issue by number. Returns full details including " +
			"title, body, state, labels, assignees, and timestamps.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{
					"type":        "string",
					"description": "Repository name — 'owner/repo' or just 'repo'",
				},
				"number": map[string]any{
					"type":        "integer",
					"description": "Issue number",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Forge account name (default: primary)",
				},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleIssueGet(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_issue_list",
		Description: "List issues in a repository. Filterable by state, labels, and assignee.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{
					"type":        "string",
					"description": "Repository name — 'owner/repo' or just 'repo'",
				},
				"state": map[string]any{
					"type":        "string",
					"description": "Filter by state: 'open' (default), 'closed', 'all'",
				},
				"labels": map[string]any{
					"type":        "string",
					"description": "Comma-separated label filter",
				},
				"assignee": map[string]any{
					"type":        "string",
					"description": "Filter by assignee username",
				},
				"sort": map[string]any{
					"type":        "string",
					"description": "Sort by: 'created' (default), 'updated', 'comments'",
				},
				"direction": map[string]any{
					"type":        "string",
					"description": "Sort direction: 'desc' (default), 'asc'",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max results (default 30, max 100)",
				},
				"page": map[string]any{
					"type":        "integer",
					"description": "Page number (default 1)",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Forge account name (default: primary)",
				},
			},
			"required": []string{"repo"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleIssueList(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_issue_comment",
		Description: "Post a comment on an issue or pull request.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{
					"type":        "string",
					"description": "Repository name — 'owner/repo' or just 'repo'",
				},
				"number": map[string]any{
					"type":        "integer",
					"description": "Issue or PR number",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Comment body (markdown). Supports temp:LABEL references.",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Forge account name (default: primary)",
				},
			},
			"required": []string{"repo", "number", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleIssueComment(ctx, args)
		},
	})

	// --- Pull Requests ---

	r.Register(&Tool{
		Name:        "forge_pr_list",
		Description: "List pull requests in a repository. Filterable by state, base branch, and head branch.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":      map[string]any{"type": "string", "description": "Repository name"},
				"state":     map[string]any{"type": "string", "description": "Filter: 'open' (default), 'closed', 'all'"},
				"base":      map[string]any{"type": "string", "description": "Filter by base branch"},
				"head":      map[string]any{"type": "string", "description": "Filter by head branch"},
				"limit":     map[string]any{"type": "integer", "description": "Max results (default 30)"},
				"page":      map[string]any{"type": "integer", "description": "Page number (default 1)"},
				"account":   map[string]any{"type": "string", "description": "Forge account name"},
				"sort":      map[string]any{"type": "string", "description": "Sort by: 'created', 'updated', 'popularity'"},
				"direction": map[string]any{"type": "string", "description": "Sort direction: 'desc', 'asc'"},
			},
			"required": []string{"repo"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRList(ctx, args)
		},
	})

	r.Register(&Tool{
		Name: "forge_pr_get",
		Description: "Get a single pull request by number. Returns full metadata including " +
			"title, body, state, branches, mergeable status, additions/deletions, and URL.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":    map[string]any{"type": "string", "description": "Repository name"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
				"account": map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRGet(ctx, args)
		},
	})

	r.Register(&Tool{
		Name: "forge_pr_diff",
		Description: "Get the unified diff for a pull request. Truncated at max_lines " +
			"(default 2000). For large PRs, use forge_pr_files for per-file patches instead.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":      map[string]any{"type": "string", "description": "Repository name"},
				"number":    map[string]any{"type": "integer", "description": "PR number"},
				"max_lines": map[string]any{"type": "integer", "description": "Max diff lines (default 2000)"},
				"account":   map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRDiff(ctx, args)
		},
	})

	r.Register(&Tool{
		Name: "forge_pr_files",
		Description: "List files changed in a pull request with status, additions, deletions, " +
			"and per-file patches.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":    map[string]any{"type": "string", "description": "Repository name"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
				"account": map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRFiles(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_commits",
		Description: "List commits in a pull request with SHA, message, author, and date.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":    map[string]any{"type": "string", "description": "Repository name"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
				"account": map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRCommits(ctx, args)
		},
	})

	r.Register(&Tool{
		Name: "forge_pr_reviews",
		Description: "List reviews on a pull request. Includes inline comments nested " +
			"under each review (e.g., Copilot review feedback at specific diff lines).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":    map[string]any{"type": "string", "description": "Repository name"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
				"account": map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRReviews(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_review",
		Description: "Submit a review on a pull request (approve, comment, or request changes).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":    map[string]any{"type": "string", "description": "Repository name"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
				"event":   map[string]any{"type": "string", "description": "Review action: 'APPROVE', 'COMMENT', or 'REQUEST_CHANGES'"},
				"body":    map[string]any{"type": "string", "description": "Review summary. Supports temp:LABEL."},
				"account": map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number", "event", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRReview(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_review_comment",
		Description: "Post an inline comment on a pull request diff at a specific file and line.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":    map[string]any{"type": "string", "description": "Repository name"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
				"body":    map[string]any{"type": "string", "description": "Comment text. Supports temp:LABEL."},
				"path":    map[string]any{"type": "string", "description": "File path in the diff"},
				"line":    map[string]any{"type": "integer", "description": "Line number in the diff"},
				"side":    map[string]any{"type": "string", "description": "Diff side: 'LEFT' or 'RIGHT' (default 'RIGHT')"},
				"account": map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number", "body", "path", "line"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRReviewComment(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_checks",
		Description: "List CI check runs for a pull request with status and conclusion.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":    map[string]any{"type": "string", "description": "Repository name"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
				"account": map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRChecks(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_merge",
		Description: "Merge a pull request. Default method is squash.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":           map[string]any{"type": "string", "description": "Repository name"},
				"number":         map[string]any{"type": "integer", "description": "PR number"},
				"method":         map[string]any{"type": "string", "description": "Merge method: 'squash' (default), 'merge', 'rebase'"},
				"commit_title":   map[string]any{"type": "string", "description": "Custom commit title"},
				"commit_message": map[string]any{"type": "string", "description": "Custom commit message. Supports temp:LABEL."},
				"account":        map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRMerge(ctx, args)
		},
	})

	// --- Reactions ---

	r.Register(&Tool{
		Name: "forge_react",
		Description: "Add an emoji reaction to an issue, PR, or specific comment. " +
			"Omit comment_id to react to the issue/PR itself.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":       map[string]any{"type": "string", "description": "Repository name"},
				"number":     map[string]any{"type": "integer", "description": "Issue or PR number"},
				"comment_id": map[string]any{"type": "integer", "description": "React to a specific comment (omit for issue/PR)"},
				"emoji":      map[string]any{"type": "string", "description": "Reaction: +1, -1, laugh, confused, heart, hooray, rocket, eyes"},
				"account":    map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number", "emoji"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleReact(ctx, args)
		},
	})

	// --- Review requests ---

	r.Register(&Tool{
		Name:        "forge_pr_request_review",
		Description: "Request reviews from specified users on a pull request.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":      map[string]any{"type": "string", "description": "Repository name"},
				"number":    map[string]any{"type": "integer", "description": "PR number"},
				"reviewers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Usernames to request review from"},
				"account":   map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"repo", "number", "reviewers"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleRequestReview(ctx, args)
		},
	})

	// --- Search ---

	r.Register(&Tool{
		Name:        "forge_search",
		Description: "Search a code forge using its native search syntax.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":   map[string]any{"type": "string", "description": "Search query (forge-native syntax)"},
				"kind":    map[string]any{"type": "string", "description": "Search type: 'issues', 'code', 'commits'"},
				"limit":   map[string]any{"type": "integer", "description": "Max results (default 20)"},
				"account": map[string]any{"type": "string", "description": "Forge account name"},
			},
			"required": []string{"query", "kind"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleSearch(ctx, args)
		},
	})
}

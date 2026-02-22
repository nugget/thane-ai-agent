package tools

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/forge"
)

// SetForgeTools adds forge tools to the registry.
func (r *Registry) SetForgeTools(ft *forge.Tools) {
	r.forgeTools = ft
	r.registerForgeTools()
}

func (r *Registry) registerForgeTools() {
	if r.forgeTools == nil {
		return
	}

	r.Register(&Tool{
		Name:        "forge_issue_create",
		Description: "Create a new issue on a code forge (GitHub). Returns the issue number and URL.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":    map[string]any{"type": "string", "description": "Repository in owner/repo format (e.g. acme/myapp)"},
				"title":   map[string]any{"type": "string", "description": "Issue title"},
				"body":    map[string]any{"type": "string", "description": "Issue body text (supports temp: references)"},
				"labels":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Label names to apply"},
				"assignees": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Usernames to assign",
				},
			},
			"required": []string{"repo", "title"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleIssueCreate(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_issue_update",
		Description: "Update an existing issue on a code forge. Only provided fields are changed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":   map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":      map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":    map[string]any{"type": "integer", "description": "Issue number to update"},
				"title":     map[string]any{"type": "string", "description": "New title"},
				"body":      map[string]any{"type": "string", "description": "New body text"},
				"state":     map[string]any{"type": "string", "enum": []string{"open", "closed"}, "description": "New state"},
				"labels":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Replacement label set"},
				"assignees": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Replacement assignee set"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleIssueUpdate(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_issue_get",
		Description: "Fetch a single issue by number from a code forge.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":    map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":  map[string]any{"type": "integer", "description": "Issue number"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleIssueGet(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_issue_list",
		Description: "List issues in a repository. Supports filtering by state, labels, and assignee.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":   map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":      map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"state":     map[string]any{"type": "string", "enum": []string{"open", "closed", "all"}, "description": "Issue state filter (default: open)"},
				"labels":    map[string]any{"type": "string", "description": "Comma-separated label names to filter by"},
				"assignee":  map[string]any{"type": "string", "description": "Filter by assignee username"},
				"sort":      map[string]any{"type": "string", "description": "Sort field: created, updated, comments"},
				"direction": map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "description": "Sort direction"},
				"limit":     map[string]any{"type": "integer", "description": "Maximum results to return"},
				"page":      map[string]any{"type": "integer", "description": "Page number for pagination"},
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
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":    map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":  map[string]any{"type": "integer", "description": "Issue or PR number"},
				"body":    map[string]any{"type": "string", "description": "Comment text (supports temp: references)"},
			},
			"required": []string{"repo", "number", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleIssueComment(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_list",
		Description: "List pull requests in a repository.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":   map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":      map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"state":     map[string]any{"type": "string", "enum": []string{"open", "closed", "all"}, "description": "PR state filter (default: open)"},
				"head":      map[string]any{"type": "string", "description": "Filter by head branch (user:branch format)"},
				"base":      map[string]any{"type": "string", "description": "Filter by base branch"},
				"sort":      map[string]any{"type": "string", "description": "Sort field: created, updated, popularity, long-running"},
				"direction": map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "description": "Sort direction"},
				"limit":     map[string]any{"type": "integer", "description": "Maximum results to return"},
				"page":      map[string]any{"type": "integer", "description": "Page number for pagination"},
			},
			"required": []string{"repo"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRList(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_get",
		Description: "Fetch details of a single pull request including merge status and review state.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":    map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRGet(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_files",
		Description: "List the files changed in a pull request with add/delete counts.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":    map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRFiles(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_diff",
		Description: "Retrieve the raw unified diff for a pull request. Use max_lines to cap output size.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":   map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":      map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":    map[string]any{"type": "integer", "description": "PR number"},
				"max_lines": map[string]any{"type": "integer", "description": "Truncate diff after this many lines (0 = no limit)"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRDiff(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_commits",
		Description: "List the commits included in a pull request.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":    map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRCommits(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_reviews",
		Description: "List reviews on a pull request, including inline comments.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":    map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRReviews(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_review",
		Description: "Submit a review on a pull request (APPROVE, REQUEST_CHANGES, or COMMENT).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":    map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
				"event":   map[string]any{"type": "string", "enum": []string{"APPROVE", "REQUEST_CHANGES", "COMMENT"}, "description": "Review event type"},
				"body":    map[string]any{"type": "string", "description": "Review body text (supports temp: references)"},
			},
			"required": []string{"repo", "number", "event"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRReview(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_review_comment",
		Description: "Add an inline review comment to a specific line in a pull request diff.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":     map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":        map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":      map[string]any{"type": "integer", "description": "PR number"},
				"path":        map[string]any{"type": "string", "description": "File path relative to repo root"},
				"line":        map[string]any{"type": "integer", "description": "Line number in the diff"},
				"side":        map[string]any{"type": "string", "enum": []string{"LEFT", "RIGHT"}, "description": "Which side of the diff (default: RIGHT)"},
				"body":        map[string]any{"type": "string", "description": "Comment text"},
				"in_reply_to": map[string]any{"type": "integer", "description": "ID of the parent comment to reply to"},
			},
			"required": []string{"repo", "number", "path", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRReviewComment(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_checks",
		Description: "List CI check runs for a pull request's head commit.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":    map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":  map[string]any{"type": "integer", "description": "PR number"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRChecks(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_pr_merge",
		Description: "Merge a pull request using the specified strategy (merge, squash, or rebase).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":        map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":           map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":         map[string]any{"type": "integer", "description": "PR number"},
				"method":         map[string]any{"type": "string", "enum": []string{"merge", "squash", "rebase"}, "description": "Merge strategy (default: merge)"},
				"commit_title":   map[string]any{"type": "string", "description": "Override merge commit title (merge/squash only)"},
				"commit_message": map[string]any{"type": "string", "description": "Override merge commit message (merge/squash only)"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandlePRMerge(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_reaction",
		Description: "Add an emoji reaction to an issue, pull request, or comment.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":    map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":       map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":     map[string]any{"type": "integer", "description": "Issue or PR number"},
				"emoji":      map[string]any{"type": "string", "description": "Reaction emoji: +1, -1, laugh, confused, heart, hooray, rocket, eyes"},
				"comment_id": map[string]any{"type": "integer", "description": "Comment ID to react to (0 or omit to react to the issue/PR itself)"},
			},
			"required": []string{"repo", "number", "emoji"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleReaction(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_request_review",
		Description: "Request reviews from specific users on a pull request.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":   map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"repo":      map[string]any{"type": "string", "description": "Repository in owner/repo format"},
				"number":    map[string]any{"type": "integer", "description": "PR number"},
				"reviewers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "GitHub usernames to request review from"},
			},
			"required": []string{"repo", "number", "reviewers"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleRequestReview(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_search",
		Description: "Search a code forge for issues, code, or commits matching a query.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{"type": "string", "description": "Forge account name (default: primary account)"},
				"query":   map[string]any{"type": "string", "description": "Search query string (supports GitHub search syntax)"},
				"kind":    map[string]any{"type": "string", "enum": []string{"issue", "code", "commit"}, "description": "Type of entity to search (default: issue)"},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleSearch(ctx, args)
		},
	})
}

package forge

import (
	"context"

	toolpkg "github.com/nugget/thane-ai-agent/internal/tools"
)

// forgeAccountDescription is the model-facing text for every forge
// tool's account parameter. It has to state the binding rule, not just
// the primary-account default: a bound loop reads both this and its
// spec, and of the two it is likelier to act on the one attached to
// the tool it is about to call.
const forgeAccountDescription = "Forge account name. Omit to use this loop's bound account, or the primary account when unbound; naming a different account than the one you are bound to is refused."

// Name implements [tools.Provider].
func (t *Tools) Name() string { return "forge" }

// Tools implements [tools.Provider]. Forge owns these declarations so its
// schemas, account policy, and handlers evolve as one model-facing contract.
func (t *Tools) Tools() []*toolpkg.Tool {
	if t == nil {
		return nil
	}
	declared := t.coreToolDefinitions()
	return append(declared, t.subscriptionToolDefinitions()...)
}

func accountParameter() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": forgeAccountDescription,
	}
}

func (t *Tools) coreToolDefinitions() []*toolpkg.Tool {
	if t == nil {
		return nil
	}
	return []*toolpkg.Tool{

		// --- Issues ---

		{
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
					"account": accountParameter(),
				},
				"required": []string{"repo", "title"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleIssueCreate(ctx, args)
			},
		},

		{
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
					"account": accountParameter(),
				},
				"required": []string{"repo", "number"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleIssueUpdate(ctx, args)
			},
		},

		{
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
					"account": accountParameter(),
				},
				"required": []string{"repo", "number"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleIssueGet(ctx, args)
			},
		},

		{
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
					"account": accountParameter(),
				},
				"required": []string{"repo"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleIssueList(ctx, args)
			},
		},

		{
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
					"account": accountParameter(),
				},
				"required": []string{"repo", "number", "body"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleIssueComment(ctx, args)
			},
		},

		// --- Pull Requests ---

		{
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
					"account":   accountParameter(),
					"sort":      map[string]any{"type": "string", "description": "Sort by: 'created', 'updated', 'popularity'"},
					"direction": map[string]any{"type": "string", "description": "Sort direction: 'desc', 'asc'"},
				},
				"required": []string{"repo"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRList(ctx, args)
			},
		},

		{
			Name: "forge_pr_get",
			Description: "Get a single pull request by number. Returns full metadata including " +
				"title, body, state, branches, mergeable status, additions/deletions, and URL.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":    map[string]any{"type": "string", "description": "Repository name"},
					"number":  map[string]any{"type": "integer", "description": "PR number"},
					"account": accountParameter(),
				},
				"required": []string{"repo", "number"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRGet(ctx, args)
			},
		},

		{
			Name: "forge_pr_diff",
			Description: "Get the unified diff for a pull request. Truncated at max_lines " +
				"(default 2000). For large PRs, use forge_pr_files for per-file patches instead.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":      map[string]any{"type": "string", "description": "Repository name"},
					"number":    map[string]any{"type": "integer", "description": "PR number"},
					"max_lines": map[string]any{"type": "integer", "description": "Max diff lines (default 2000)"},
					"account":   accountParameter(),
				},
				"required": []string{"repo", "number"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRDiff(ctx, args)
			},
		},

		{
			Name: "forge_pr_files",
			Description: "List files changed in a pull request with status, additions, deletions, " +
				"and per-file patches.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":    map[string]any{"type": "string", "description": "Repository name"},
					"number":  map[string]any{"type": "integer", "description": "PR number"},
					"account": accountParameter(),
				},
				"required": []string{"repo", "number"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRFiles(ctx, args)
			},
		},

		{
			Name:        "forge_pr_commits",
			Description: "List commits in a pull request with SHA, message, author, and date.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":    map[string]any{"type": "string", "description": "Repository name"},
					"number":  map[string]any{"type": "integer", "description": "PR number"},
					"account": accountParameter(),
				},
				"required": []string{"repo", "number"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRCommits(ctx, args)
			},
		},

		{
			Name: "forge_pr_reviews",
			Description: "List reviews on a pull request. Includes inline comments nested " +
				"under each review (e.g., Copilot review feedback at specific diff lines).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":    map[string]any{"type": "string", "description": "Repository name"},
					"number":  map[string]any{"type": "integer", "description": "PR number"},
					"account": accountParameter(),
				},
				"required": []string{"repo", "number"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRReviews(ctx, args)
			},
		},

		{
			Name:        "forge_pr_review",
			Description: "Submit a review on a pull request (approve, comment, or request changes).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":    map[string]any{"type": "string", "description": "Repository name"},
					"number":  map[string]any{"type": "integer", "description": "PR number"},
					"event":   map[string]any{"type": "string", "description": "Review action: 'APPROVE', 'COMMENT', or 'REQUEST_CHANGES'"},
					"body":    map[string]any{"type": "string", "description": "Review summary. Supports temp:LABEL."},
					"account": accountParameter(),
				},
				"required": []string{"repo", "number", "event", "body"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRReview(ctx, args)
			},
		},

		{
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
					"account": accountParameter(),
				},
				"required": []string{"repo", "number", "body", "path", "line"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRReviewComment(ctx, args)
			},
		},

		{
			Name:        "forge_pr_checks",
			Description: "List CI check runs for a pull request with status and conclusion.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":    map[string]any{"type": "string", "description": "Repository name"},
					"number":  map[string]any{"type": "integer", "description": "PR number"},
					"account": accountParameter(),
				},
				"required": []string{"repo", "number"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRChecks(ctx, args)
			},
		},

		{
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
					"account":        accountParameter(),
				},
				"required": []string{"repo", "number"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandlePRMerge(ctx, args)
			},
		},

		// --- Reactions ---

		{
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
					"account":    accountParameter(),
				},
				"required": []string{"repo", "number", "emoji"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleReact(ctx, args)
			},
		},

		// --- Review requests ---

		{
			Name:        "forge_pr_request_review",
			Description: "Request reviews from specified users on a pull request.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":      map[string]any{"type": "string", "description": "Repository name"},
					"number":    map[string]any{"type": "integer", "description": "PR number"},
					"reviewers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Usernames to request review from"},
					"account":   accountParameter(),
				},
				"required": []string{"repo", "number", "reviewers"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleRequestReview(ctx, args)
			},
		},

		// --- Search ---

		{
			Name:        "forge_search",
			Description: "Search a code forge using its native search syntax.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "Search query (forge-native syntax)"},
					"kind":    map[string]any{"type": "string", "description": "Search type: 'issues', 'code', 'commits'"},
					"limit":   map[string]any{"type": "integer", "description": "Max results (default 20)"},
					"account": accountParameter(),
				},
				"required": []string{"query", "kind"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleSearch(ctx, args)
			},
		},
	}
}

// Package forge provides a provider-agnostic interface for interacting with
// code forge services such as GitHub and Gitea. It exposes a uniform set of
// types and tool handlers that the agent can use regardless of the underlying
// hosting platform.
package forge

import "time"

// Issue represents a single issue on a code forge.
type Issue struct {
	// Number is the forge-assigned issue number.
	Number int
	// Title is the issue title.
	Title string
	// Body is the issue description body.
	Body string
	// State is the current state, e.g. "open" or "closed".
	State string
	// Labels lists the label names applied to the issue.
	Labels []string
	// Assignees lists the usernames of assigned users.
	Assignees []string
	// Author is the username of the issue creator.
	Author string
	// CreatedAt is when the issue was created.
	CreatedAt time.Time
	// UpdatedAt is when the issue was last updated.
	UpdatedAt time.Time
	// URL is the web URL of the issue.
	URL string
	// CommentCount is the total number of comments on the issue.
	CommentCount int
}

// IssueUpdate carries the fields to change when updating an issue.
// A nil pointer field means "leave unchanged". A nil slice means "leave unchanged".
type IssueUpdate struct {
	// Title is the new title, or nil to leave unchanged.
	Title *string
	// Body is the new body text, or nil to leave unchanged.
	Body *string
	// State is the new state ("open"/"closed"), or nil to leave unchanged.
	State *string
	// Labels replaces the label set. Nil means leave unchanged.
	Labels []string
	// Assignees replaces the assignee set. Nil means leave unchanged.
	Assignees []string
}

// Comment represents a comment on an issue or pull request.
type Comment struct {
	// ID is the forge-assigned comment identifier.
	ID int64
	// Body is the comment text.
	Body string
	// Author is the username of the comment author.
	Author string
	// CreatedAt is when the comment was posted.
	CreatedAt time.Time
	// URL is the web URL of the comment.
	URL string
}

// PullRequest represents a pull request on a code forge.
type PullRequest struct {
	// Number is the forge-assigned PR number.
	Number int
	// Title is the PR title.
	Title string
	// Body is the PR description.
	Body string
	// State is the current state, e.g. "open" or "closed".
	State string
	// Author is the username of the PR creator.
	Author string
	// Head is the source branch or ref.
	Head string
	// Base is the target branch.
	Base string
	// Mergeable indicates whether the PR can be merged cleanly; nil means unknown.
	Mergeable *bool
	// ReviewState is the aggregate review decision (e.g. "APPROVED", "CHANGES_REQUESTED").
	ReviewState string
	// Additions is the number of lines added.
	Additions int
	// Deletions is the number of lines deleted.
	Deletions int
	// ChangedFiles is the number of files changed.
	ChangedFiles int
	// URL is the web URL of the pull request.
	URL string
	// CreatedAt is when the PR was opened.
	CreatedAt time.Time
	// Draft indicates whether the PR is a draft.
	Draft bool
}

// ChangedFile describes a single file changed in a pull request.
type ChangedFile struct {
	// Filename is the path of the file relative to the repo root.
	Filename string
	// Status is the change status: "added", "modified", "removed", etc.
	Status string
	// Additions is the number of lines added in this file.
	Additions int
	// Deletions is the number of lines deleted in this file.
	Deletions int
	// Patch is the unified diff patch for this file.
	Patch string
}

// Review represents a pull request review submission.
type Review struct {
	// ID is the forge-assigned review identifier.
	ID int64
	// Author is the username of the reviewer.
	Author string
	// State is the review verdict: "APPROVED", "CHANGES_REQUESTED", "COMMENTED", etc.
	State string
	// Body is the top-level review comment body.
	Body string
	// SubmittedAt is when the review was submitted.
	SubmittedAt time.Time
	// InlineComments holds the individual line-level comments attached to this review.
	InlineComments []ReviewComment
}

// ReviewComment is a line-level comment within a pull request review.
type ReviewComment struct {
	// ID is the forge-assigned comment identifier.
	ID int64
	// Path is the file path this comment is attached to.
	Path string
	// Line is the line number in the diff the comment applies to.
	Line int
	// Side is which side of the diff: "LEFT" (before) or "RIGHT" (after).
	Side string
	// Body is the comment text.
	Body string
	// InReplyTo is the ID of the parent comment for threaded replies; 0 for top-level.
	InReplyTo int64
}

// CheckRun represents a CI/CD check run result for a commit or PR.
type CheckRun struct {
	// Name is the check run name.
	Name string
	// Status is the run status: "queued", "in_progress", "completed".
	Status string
	// Conclusion is the final outcome when Status is "completed":
	// "success", "failure", "neutral", "cancelled", "skipped", "timed_out", etc.
	Conclusion string
	// StartedAt is when the check run started.
	StartedAt time.Time
	// CompletedAt is when the check run finished.
	CompletedAt time.Time
	// DetailsURL is the URL to the full check run details page.
	DetailsURL string
}

// Commit is a lightweight representation of a git commit.
type Commit struct {
	// SHA is the full commit hash.
	SHA string
	// Message is the commit message.
	Message string
	// Author is the name or username of the commit author.
	Author string
	// Date is the commit author date.
	Date time.Time
}

// MergeOptions controls how a pull request is merged.
type MergeOptions struct {
	// Method is the merge strategy: "merge", "squash", or "rebase".
	Method string
	// CommitTitle overrides the merge commit title (for merge/squash).
	CommitTitle string
	// CommitMessage overrides the merge commit body (for merge/squash).
	CommitMessage string
}

// ListOptions filters list operations on issues and pull requests.
type ListOptions struct {
	// State filters by state: "open", "closed", or "all".
	State string
	// Labels is a comma-separated list of label names to filter by.
	Labels string
	// Assignee filters by assignee username.
	Assignee string
	// Sort specifies the sort field: "created", "updated", "comments".
	Sort string
	// Direction is the sort direction: "asc" or "desc".
	Direction string
	// Limit caps the number of results returned.
	Limit int
	// Page is the 1-based page number for pagination.
	Page int
	// Base filters PRs by base branch name.
	Base string
	// Head filters PRs by head branch name (or "user:branch" format).
	Head string
}

// SearchKind identifies the type of entity to search for.
type SearchKind string

const (
	// SearchKindIssues searches issues and pull requests.
	SearchKindIssues SearchKind = "issue"
	// SearchKindCode searches source code.
	SearchKindCode SearchKind = "code"
	// SearchKindCommits searches commit messages.
	SearchKindCommits SearchKind = "commit"
)

// SearchResult is a single result from a forge search query.
type SearchResult struct {
	// Kind identifies the type of result: "issue", "code", or "commit".
	Kind string
	// Number is the issue/PR number, if applicable.
	Number int
	// Title is the issue/PR title or file path, if applicable.
	Title string
	// URL is the web URL of the result.
	URL string
	// Snippet is a short excerpt of matching text.
	Snippet string
	// Path is the file path for code search results.
	Path string
	// SHA is the commit hash for commit search results.
	SHA string
}

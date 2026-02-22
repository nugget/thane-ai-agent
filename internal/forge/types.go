package forge

import "time"

// SearchKind identifies the type of forge search to perform.
type SearchKind string

const (
	// SearchIssues searches issues and pull requests.
	SearchIssues SearchKind = "issues"
	// SearchCode searches file contents.
	SearchCode SearchKind = "code"
	// SearchCommits searches commit messages and metadata.
	SearchCommits SearchKind = "commits"
)

// Issue represents a forge issue or pull request issue entry.
type Issue struct {
	// Number is the issue number (e.g., #42).
	Number int
	// Title is the issue title.
	Title string
	// Body is the full issue body in markdown.
	Body string
	// State is the issue lifecycle state: "open" or "closed".
	State string
	// Labels lists the label names applied to this issue.
	Labels []string
	// Assignees lists the usernames assigned to this issue.
	Assignees []string
	// Author is the username who created the issue.
	Author string
	// URL is the web URL for the issue.
	URL string
	// CreatedAt is when the issue was created.
	CreatedAt time.Time
	// UpdatedAt is when the issue was last modified.
	UpdatedAt time.Time
	// CommentCount is the number of comments on the issue.
	CommentCount int
}

// IssueUpdate holds fields for updating an issue. Pointer fields
// distinguish "not provided" (nil) from "set to empty value".
type IssueUpdate struct {
	// Title replaces the issue title when non-nil.
	Title *string
	// Body REPLACES the entire issue body when non-nil — it does not
	// append. Leave nil to keep the existing body unchanged.
	Body *string
	// State changes the issue state when non-nil: "open" or "closed".
	State *string
	// Labels REPLACES all labels when non-nil. Pass an empty slice to
	// remove all labels.
	Labels *[]string
	// Assignees REPLACES all assignees when non-nil.
	Assignees *[]string
}

// PullRequest represents a forge pull request.
type PullRequest struct {
	// Number is the PR number (e.g., #99).
	Number int
	// Title is the PR title.
	Title string
	// Body is the full PR description in markdown.
	Body string
	// State is the PR lifecycle state: "open", "closed", or "merged".
	State string
	// Author is the username who opened the PR.
	Author string
	// Head is the source branch name.
	Head string
	// Base is the target branch name.
	Base string
	// Mergeable indicates whether the PR can be merged cleanly. Nil
	// means the mergeability check has not completed yet.
	Mergeable *bool
	// ReviewState is the aggregate review status (e.g., "approved").
	ReviewState string
	// Additions is the total lines added across all files.
	Additions int
	// Deletions is the total lines removed across all files.
	Deletions int
	// ChangedFiles is the number of files modified.
	ChangedFiles int
	// URL is the web URL for the PR.
	URL string
	// CreatedAt is when the PR was opened.
	CreatedAt time.Time
	// UpdatedAt is when the PR was last modified.
	UpdatedAt time.Time
}

// Comment represents a comment on an issue or pull request.
type Comment struct {
	// ID is the forge-assigned comment identifier.
	ID int64
	// Body is the comment text in markdown.
	Body string
	// Author is the username who posted the comment.
	Author string
	// URL is the web URL for the comment.
	URL string
	// CreatedAt is when the comment was posted.
	CreatedAt time.Time
}

// ChangedFile represents a file changed in a pull request.
type ChangedFile struct {
	// Filename is the path of the changed file.
	Filename string
	// Status describes the change type: "added", "modified", "removed",
	// or "renamed".
	Status string
	// Additions is the number of lines added in this file.
	Additions int
	// Deletions is the number of lines removed in this file.
	Deletions int
	// Patch is the unified diff patch for this file, if available.
	Patch string
}

// Commit represents a single commit in a pull request.
type Commit struct {
	// SHA is the abbreviated commit hash (typically 7 characters).
	SHA string
	// Message is the full commit message.
	Message string
	// Author is the commit author name.
	Author string
	// Date is when the commit was authored.
	Date time.Time
}

// Review represents a pull request review with optional inline comments.
type Review struct {
	// ID is the forge-assigned review identifier.
	ID int64
	// Author is the username who submitted the review.
	Author string
	// State is the review verdict: "APPROVED", "CHANGES_REQUESTED",
	// "COMMENTED", or "DISMISSED".
	State string
	// Body is the review summary text.
	Body string
	// SubmittedAt is when the review was submitted.
	SubmittedAt time.Time
	// InlineComments are line-level comments attached to this review.
	InlineComments []*ReviewComment
}

// ReviewSubmission holds the parameters for submitting a new review.
type ReviewSubmission struct {
	// Event is the review action: "APPROVE", "COMMENT", or
	// "REQUEST_CHANGES".
	Event string
	// Body is the review summary text.
	Body string
}

// ReviewComment represents an inline comment on a pull request diff.
type ReviewComment struct {
	// ID is the forge-assigned comment identifier (zero for new comments).
	ID int64
	// Body is the comment text.
	Body string
	// Path is the file path in the diff.
	Path string
	// Line is the line number in the diff.
	Line int
	// Side selects the diff side: "LEFT" or "RIGHT" (default "RIGHT").
	Side string
}

// CheckRun represents a CI check run on a pull request.
type CheckRun struct {
	// Name is the check run name (e.g., "CI / lint").
	Name string
	// Status is the check lifecycle: "queued", "in_progress", or
	// "completed".
	Status string
	// Conclusion is the outcome when completed: "success", "failure",
	// "neutral", "cancelled", "skipped", "timed_out", or
	// "action_required".
	Conclusion string
	// StartedAt is when the check run started, if available.
	StartedAt *time.Time
	// CompletedAt is when the check run finished, if available.
	CompletedAt *time.Time
	// DetailsURL links to the full check run output.
	DetailsURL string
}

// MergeOptions controls how a pull request is merged.
type MergeOptions struct {
	// Method is the merge strategy: "squash" (default), "merge", or
	// "rebase".
	Method string
	// CommitTitle overrides the merge commit title.
	CommitTitle string
	// CommitMessage overrides the merge commit body. Supports
	// temp:LABEL references.
	CommitMessage string
}

// MergeResult reports the outcome of a pull request merge.
type MergeResult struct {
	// SHA is the merge commit hash.
	SHA string
	// Message is the merge result message from the forge.
	Message string
}

// ListOptions controls pagination and filtering for list operations.
type ListOptions struct {
	// State filters by lifecycle state: "open", "closed", or "all".
	State string
	// Labels is a comma-separated label filter for issues.
	Labels string
	// Assignee filters issues by assignee username.
	Assignee string
	// Base filters PRs by target branch.
	Base string
	// Head filters PRs by source branch.
	Head string
	// Sort controls result ordering: "created", "updated", or
	// "comments".
	Sort string
	// Direction controls sort direction: "asc" or "desc".
	Direction string
	// Limit caps the number of results per page (default 30, max 100).
	Limit int
	// Page selects the result page (1-indexed, default 1).
	Page int
}

// SearchResult represents a single search result from a forge.
type SearchResult struct {
	// Number is the issue or PR number (zero for non-issue results).
	Number int
	// Title is the result title or abbreviated SHA for commits.
	Title string
	// URL is the web URL for the result.
	URL string
	// Body is a snippet for code results or description for issues.
	Body string
}

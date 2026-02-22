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

// Issue represents a forge issue.
type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"` // "open" or "closed"
	Labels    []string  `json:"labels,omitempty"`
	Assignees []string  `json:"assignees,omitempty"`
	Author    string    `json:"author"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Comments  int       `json:"comments"`
}

// IssueUpdate holds fields for updating an issue. Pointer fields
// distinguish "not provided" (nil) from "set to empty". When Body is
// non-nil it REPLACES the entire issue body — it does not append.
type IssueUpdate struct {
	Title     *string   `json:"title,omitempty"`
	Body      *string   `json:"body,omitempty"`
	State     *string   `json:"state,omitempty"`
	Labels    *[]string `json:"labels,omitempty"`
	Assignees *[]string `json:"assignees,omitempty"`
}

// PullRequest represents a forge pull request.
type PullRequest struct {
	Number       int       `json:"number"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	State        string    `json:"state"`
	Author       string    `json:"author"`
	Head         string    `json:"head"`
	Base         string    `json:"base"`
	Mergeable    *bool     `json:"mergeable,omitempty"`
	ReviewState  string    `json:"review_state,omitempty"`
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	ChangedFiles int       `json:"changed_files"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Comment represents a comment on an issue or pull request.
type Comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// ChangedFile represents a file changed in a pull request.
type ChangedFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"` // "added", "modified", "removed", "renamed"
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
}

// Commit represents a single commit.
type Commit struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// Review represents a pull request review with optional inline comments.
type Review struct {
	ID             int64            `json:"id"`
	Author         string           `json:"author"`
	State          string           `json:"state"` // "APPROVED", "CHANGES_REQUESTED", "COMMENTED", "DISMISSED"
	Body           string           `json:"body"`
	SubmittedAt    time.Time        `json:"submitted_at"`
	InlineComments []*ReviewComment `json:"inline_comments,omitempty"`
}

// ReviewSubmission holds the parameters for submitting a new review.
type ReviewSubmission struct {
	Event string `json:"event"` // "APPROVE", "COMMENT", "REQUEST_CHANGES"
	Body  string `json:"body"`
}

// ReviewComment represents an inline comment on a pull request diff.
type ReviewComment struct {
	ID   int64  `json:"id,omitempty"`
	Body string `json:"body"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side,omitempty"` // "LEFT" or "RIGHT"
}

// CheckRun represents a CI check run on a pull request.
type CheckRun struct {
	Name        string     `json:"name"`
	Status      string     `json:"status"`     // "queued", "in_progress", "completed"
	Conclusion  string     `json:"conclusion"` // "success", "failure", "neutral", etc.
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	DetailsURL  string     `json:"details_url,omitempty"`
}

// MergeOptions controls how a pull request is merged.
type MergeOptions struct {
	Method        string `json:"method,omitempty"` // "squash" (default), "merge", "rebase"
	CommitTitle   string `json:"commit_title,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`
}

// MergeResult reports the outcome of a pull request merge.
type MergeResult struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
}

// ListOptions controls pagination and filtering for list operations.
type ListOptions struct {
	State     string `json:"state,omitempty"`  // "open", "closed", "all"
	Labels    string `json:"labels,omitempty"` // comma-separated label filter
	Assignee  string `json:"assignee,omitempty"`
	Base      string `json:"base,omitempty"`      // PR filter: base branch
	Head      string `json:"head,omitempty"`      // PR filter: head branch
	Sort      string `json:"sort,omitempty"`      // "created", "updated", "comments"
	Direction string `json:"direction,omitempty"` // "asc", "desc"
	Limit     int    `json:"limit,omitempty"`
	Page      int    `json:"page,omitempty"`
}

// SearchResult represents a single search result from a forge.
type SearchResult struct {
	Number int    `json:"number,omitempty"` // for issues/PRs
	Title  string `json:"title"`
	URL    string `json:"url"`
	Body   string `json:"body,omitempty"` // snippet for code, description for issues
}

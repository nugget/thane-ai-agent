package forge

import "context"

// ForgeProvider defines the operations available against a code forge service.
// Implementations exist for GitHub; Gitea support is planned.
//
// All repo parameters use "owner/name" format (e.g. "acme/myapp").
type ForgeProvider interface {
	// CreateIssue opens a new issue in the specified repository.
	CreateIssue(ctx context.Context, repo string, issue *Issue) (*Issue, error)

	// UpdateIssue modifies an existing issue identified by number.
	UpdateIssue(ctx context.Context, repo string, number int, update *IssueUpdate) (*Issue, error)

	// GetIssue fetches the details of a single issue by number.
	GetIssue(ctx context.Context, repo string, number int) (*Issue, error)

	// ListIssues returns a filtered list of issues from the repository.
	ListIssues(ctx context.Context, repo string, opts *ListOptions) ([]*Issue, error)

	// AddComment appends a new comment to an issue or pull request.
	AddComment(ctx context.Context, repo string, number int, body string) (*Comment, error)

	// ListPRs returns a filtered list of pull requests from the repository.
	ListPRs(ctx context.Context, repo string, opts *ListOptions) ([]*PullRequest, error)

	// GetPR fetches the details of a single pull request by number.
	GetPR(ctx context.Context, repo string, number int) (*PullRequest, error)

	// GetPRFiles returns the list of files changed in a pull request.
	GetPRFiles(ctx context.Context, repo string, number int) ([]*ChangedFile, error)

	// GetPRDiff returns the raw unified diff for a pull request.
	GetPRDiff(ctx context.Context, repo string, number int) (string, error)

	// ListPRCommits returns the commits included in a pull request.
	ListPRCommits(ctx context.Context, repo string, number int) ([]*Commit, error)

	// ListPRReviews returns the reviews submitted on a pull request, with
	// any inline comments nested under their respective review.
	ListPRReviews(ctx context.Context, repo string, number int) ([]*Review, error)

	// SubmitReview posts a new review on a pull request.
	SubmitReview(ctx context.Context, repo string, number int, review *Review) error

	// AddReviewComment adds a single inline comment to a pull request diff.
	AddReviewComment(ctx context.Context, repo string, number int, comment *ReviewComment) error

	// ListChecks returns the CI check runs associated with a pull request.
	ListChecks(ctx context.Context, repo string, number int) ([]*CheckRun, error)

	// MergePR merges a pull request using the specified options.
	MergePR(ctx context.Context, repo string, number int, opts *MergeOptions) error

	// AddReaction adds an emoji reaction to an issue/PR or to a specific comment.
	// When commentID is 0, the reaction is applied to the issue/PR itself.
	AddReaction(ctx context.Context, repo string, number int, commentID int64, emoji string) error

	// RequestReview requests reviews from the listed GitHub usernames.
	RequestReview(ctx context.Context, repo string, number int, reviewers []string) error

	// Search queries the forge for issues, code, or commits matching the query.
	Search(ctx context.Context, query string, kind SearchKind) ([]SearchResult, error)
}

// Package forge provides a pluggable code forge interface for issue,
// pull request, and code review management. Each forge provider
// (GitHub, Gitea, GitLab) implements the [ForgeProvider] interface and
// is registered by name with the [Manager]. The agent accesses forge
// operations through tool handlers defined in [Tools].
package forge

import "context"

// ForgeProvider is the interface that forge backends implement. All
// repository parameters use the "owner/repo" format — bare repo name
// resolution happens in the tool layer, not the provider.
type ForgeProvider interface {
	// Name returns the provider identifier (e.g., "github", "gitea").
	Name() string

	// --- Issues ---

	// CreateIssue creates a new issue and returns it with the
	// server-assigned number and URL.
	CreateIssue(ctx context.Context, repo string, issue *Issue) (*Issue, error)

	// UpdateIssue applies a partial update to an existing issue.
	// Only non-nil fields in the update are changed.
	UpdateIssue(ctx context.Context, repo string, number int, update *IssueUpdate) (*Issue, error)

	// GetIssue retrieves a single issue by number.
	GetIssue(ctx context.Context, repo string, number int) (*Issue, error)

	// ListIssues returns issues matching the given filters.
	ListIssues(ctx context.Context, repo string, opts *ListOptions) ([]*Issue, error)

	// AddComment posts a comment on an issue or pull request.
	AddComment(ctx context.Context, repo string, number int, body string) (*Comment, error)

	// --- Pull Requests ---

	// ListPRs returns pull requests matching the given filters.
	ListPRs(ctx context.Context, repo string, opts *ListOptions) ([]*PullRequest, error)

	// GetPR retrieves a single pull request by number.
	GetPR(ctx context.Context, repo string, number int) (*PullRequest, error)

	// GetPRFiles returns the files changed in a pull request.
	GetPRFiles(ctx context.Context, repo string, number int) ([]*ChangedFile, error)

	// GetPRDiff returns the unified diff for a pull request.
	GetPRDiff(ctx context.Context, repo string, number int) (string, error)

	// ListPRCommits returns commits in a pull request.
	ListPRCommits(ctx context.Context, repo string, number int) ([]*Commit, error)

	// ListPRReviews returns reviews for a pull request, including
	// inline comments nested under each review.
	ListPRReviews(ctx context.Context, repo string, number int) ([]*Review, error)

	// SubmitReview creates a new review on a pull request.
	SubmitReview(ctx context.Context, repo string, number int, review *ReviewSubmission) (*Review, error)

	// AddReviewComment posts an inline comment on a pull request diff.
	AddReviewComment(ctx context.Context, repo string, number int, comment *ReviewComment) (*ReviewComment, error)

	// ListChecks returns CI check runs for a pull request.
	ListChecks(ctx context.Context, repo string, number int) ([]*CheckRun, error)

	// MergePR merges a pull request using the specified method.
	MergePR(ctx context.Context, repo string, number int, opts *MergeOptions) (*MergeResult, error)

	// --- Reactions ---

	// AddReaction adds an emoji reaction to an issue/PR or a specific
	// comment. When commentID is 0, the reaction targets the
	// issue/PR itself.
	AddReaction(ctx context.Context, repo string, number int, commentID int64, emoji string) error

	// --- Review Management ---

	// RequestReview requests reviews from the specified users.
	RequestReview(ctx context.Context, repo string, number int, reviewers []string) error

	// --- Search ---

	// Search performs a forge-native search of the given kind.
	Search(ctx context.Context, query string, kind SearchKind, limit int) ([]SearchResult, error)
}

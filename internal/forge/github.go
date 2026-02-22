package forge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v69/github"
)

// rateLimitWarningThreshold triggers a log warning when the remaining
// rate limit drops below this value.
const rateLimitWarningThreshold = 100

// GitHub implements [ForgeProvider] for GitHub.com and GitHub Enterprise
// using the google/go-github SDK.
type GitHub struct {
	client *github.Client
	logger *slog.Logger
}

// NewGitHub creates a GitHub forge provider. The httpClient should be
// constructed via httpkit.NewClient. If baseURL is non-empty and not
// the default GitHub API URL, Enterprise URLs are configured.
func NewGitHub(httpClient *http.Client, token, baseURL string, logger *slog.Logger) (*GitHub, error) {
	client := github.NewClient(httpClient).WithAuthToken(token)

	if baseURL != "" && baseURL != "https://api.github.com" {
		var err error
		client, err = client.WithEnterpriseURLs(baseURL, baseURL)
		if err != nil {
			return nil, fmt.Errorf("configure enterprise URL: %w", err)
		}
	}

	return &GitHub{client: client, logger: logger}, nil
}

// Name returns "github".
func (g *GitHub) Name() string { return "github" }

// splitRepo splits "owner/repo" into its components.
func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo format %q, expected owner/repo", repo)
	}
	return parts[0], parts[1], nil
}

// checkRate logs a warning when the API rate limit is getting low.
func (g *GitHub) checkRate(resp *github.Response) {
	if resp == nil {
		return
	}
	remaining := resp.Rate.Remaining
	if remaining > 0 && remaining < rateLimitWarningThreshold {
		g.logger.Warn("github rate limit low",
			"remaining", remaining,
			"limit", resp.Rate.Limit,
			"reset", resp.Rate.Reset.Format(time.RFC3339),
		)
	}
}

// --- Issues ---

// CreateIssue creates a new issue on the repository.
func (g *GitHub) CreateIssue(ctx context.Context, repo string, issue *Issue) (*Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	req := &github.IssueRequest{
		Title: &issue.Title,
		Body:  &issue.Body,
	}
	if len(issue.Labels) > 0 {
		req.Labels = &issue.Labels
	}
	if len(issue.Assignees) > 0 {
		req.Assignees = &issue.Assignees
	}

	ghIssue, resp, err := g.client.Issues.Create(ctx, owner, name, req)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	g.checkRate(resp)

	return mapGitHubIssue(ghIssue), nil
}

// UpdateIssue applies a partial update to an existing issue.
func (g *GitHub) UpdateIssue(ctx context.Context, repo string, number int, update *IssueUpdate) (*Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	req := &github.IssueRequest{}
	if update.Title != nil {
		req.Title = update.Title
	}
	if update.Body != nil {
		req.Body = update.Body
	}
	if update.State != nil {
		req.State = update.State
	}
	if update.Labels != nil {
		req.Labels = update.Labels
	}
	if update.Assignees != nil {
		req.Assignees = update.Assignees
	}

	ghIssue, resp, err := g.client.Issues.Edit(ctx, owner, name, number, req)
	if err != nil {
		return nil, fmt.Errorf("update issue #%d: %w", number, err)
	}
	g.checkRate(resp)

	return mapGitHubIssue(ghIssue), nil
}

// GetIssue retrieves a single issue by number.
func (g *GitHub) GetIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghIssue, resp, err := g.client.Issues.Get(ctx, owner, name, number)
	if err != nil {
		return nil, fmt.Errorf("get issue #%d: %w", number, err)
	}
	g.checkRate(resp)

	return mapGitHubIssue(ghIssue), nil
}

// ListIssues returns issues matching the given filters.
func (g *GitHub) ListIssues(ctx context.Context, repo string, opts *ListOptions) ([]*Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghOpts := &github.IssueListByRepoOptions{
		ListOptions: github.ListOptions{
			PerPage: 30,
			Page:    1,
		},
	}
	if opts != nil {
		if opts.Limit > 0 && opts.Limit <= 100 {
			ghOpts.PerPage = opts.Limit
		}
		if opts.Page > 0 {
			ghOpts.Page = opts.Page
		}
		if opts.State != "" {
			ghOpts.State = opts.State
		}
		if opts.Labels != "" {
			ghOpts.Labels = strings.Split(opts.Labels, ",")
		}
		if opts.Assignee != "" {
			ghOpts.Assignee = opts.Assignee
		}
		if opts.Sort != "" {
			ghOpts.Sort = opts.Sort
		}
		if opts.Direction != "" {
			ghOpts.Direction = opts.Direction
		}
	}

	ghIssues, resp, err := g.client.Issues.ListByRepo(ctx, owner, name, ghOpts)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	g.checkRate(resp)

	issues := make([]*Issue, 0, len(ghIssues))
	for _, gi := range ghIssues {
		// Skip pull requests returned by the issues endpoint.
		if gi.PullRequestLinks != nil {
			continue
		}
		issues = append(issues, mapGitHubIssue(gi))
	}

	return issues, nil
}

// AddComment posts a comment on an issue or pull request.
func (g *GitHub) AddComment(ctx context.Context, repo string, number int, body string) (*Comment, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghComment, resp, err := g.client.Issues.CreateComment(ctx, owner, name, number, &github.IssueComment{
		Body: &body,
	})
	if err != nil {
		return nil, fmt.Errorf("add comment to #%d: %w", number, err)
	}
	g.checkRate(resp)

	return mapGitHubComment(ghComment), nil
}

// --- Pull Requests ---

// ListPRs returns pull requests matching the given filters.
func (g *GitHub) ListPRs(ctx context.Context, repo string, opts *ListOptions) ([]*PullRequest, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghOpts := &github.PullRequestListOptions{
		ListOptions: github.ListOptions{
			PerPage: 30,
			Page:    1,
		},
	}
	if opts != nil {
		if opts.Limit > 0 && opts.Limit <= 100 {
			ghOpts.PerPage = opts.Limit
		}
		if opts.Page > 0 {
			ghOpts.Page = opts.Page
		}
		if opts.State != "" {
			ghOpts.State = opts.State
		}
		if opts.Base != "" {
			ghOpts.Base = opts.Base
		}
		if opts.Head != "" {
			ghOpts.Head = opts.Head
		}
		if opts.Sort != "" {
			ghOpts.Sort = opts.Sort
		}
		if opts.Direction != "" {
			ghOpts.Direction = opts.Direction
		}
	}

	ghPRs, resp, err := g.client.PullRequests.List(ctx, owner, name, ghOpts)
	if err != nil {
		return nil, fmt.Errorf("list PRs: %w", err)
	}
	g.checkRate(resp)

	prs := make([]*PullRequest, 0, len(ghPRs))
	for _, gp := range ghPRs {
		prs = append(prs, mapGitHubPR(gp))
	}

	return prs, nil
}

// GetPR retrieves a single pull request by number.
func (g *GitHub) GetPR(ctx context.Context, repo string, number int) (*PullRequest, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghPR, resp, err := g.client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return nil, fmt.Errorf("get PR #%d: %w", number, err)
	}
	g.checkRate(resp)

	return mapGitHubPR(ghPR), nil
}

// GetPRFiles returns the files changed in a pull request.
func (g *GitHub) GetPRFiles(ctx context.Context, repo string, number int) ([]*ChangedFile, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghFiles, resp, err := g.client.PullRequests.ListFiles(ctx, owner, name, number, nil)
	if err != nil {
		return nil, fmt.Errorf("list PR #%d files: %w", number, err)
	}
	g.checkRate(resp)

	files := make([]*ChangedFile, 0, len(ghFiles))
	for _, f := range ghFiles {
		files = append(files, &ChangedFile{
			Filename:  f.GetFilename(),
			Status:    f.GetStatus(),
			Additions: f.GetAdditions(),
			Deletions: f.GetDeletions(),
			Patch:     f.GetPatch(),
		})
	}

	return files, nil
}

// GetPRDiff returns the unified diff for a pull request.
func (g *GitHub) GetPRDiff(ctx context.Context, repo string, number int) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}

	diff, resp, err := g.client.PullRequests.GetRaw(ctx, owner, name, number, github.RawOptions{
		Type: github.Diff,
	})
	if err != nil {
		return "", fmt.Errorf("get PR #%d diff: %w", number, err)
	}
	g.checkRate(resp)

	return diff, nil
}

// ListPRCommits returns commits in a pull request.
func (g *GitHub) ListPRCommits(ctx context.Context, repo string, number int) ([]*Commit, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghCommits, resp, err := g.client.PullRequests.ListCommits(ctx, owner, name, number, nil)
	if err != nil {
		return nil, fmt.Errorf("list PR #%d commits: %w", number, err)
	}
	g.checkRate(resp)

	commits := make([]*Commit, 0, len(ghCommits))
	for _, c := range ghCommits {
		sha := c.GetSHA()
		if len(sha) > 7 {
			sha = sha[:7]
		}
		commit := &Commit{
			SHA:     sha,
			Message: c.GetCommit().GetMessage(),
		}
		if c.GetCommit().GetAuthor() != nil {
			commit.Author = c.GetCommit().GetAuthor().GetName()
			commit.Date = c.GetCommit().GetAuthor().GetDate().Time
		}
		commits = append(commits, commit)
	}

	return commits, nil
}

// ListPRReviews returns reviews for a pull request, including inline
// comments nested under each review.
func (g *GitHub) ListPRReviews(ctx context.Context, repo string, number int) ([]*Review, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghReviews, resp, err := g.client.PullRequests.ListReviews(ctx, owner, name, number, nil)
	if err != nil {
		return nil, fmt.Errorf("list PR #%d reviews: %w", number, err)
	}
	g.checkRate(resp)

	reviews := make([]*Review, 0, len(ghReviews))
	for _, r := range ghReviews {
		review := &Review{
			ID:     r.GetID(),
			Author: r.GetUser().GetLogin(),
			State:  r.GetState(),
			Body:   r.GetBody(),
		}
		if r.SubmittedAt != nil {
			review.SubmittedAt = r.GetSubmittedAt().Time
		}

		// Fetch inline comments for this review.
		comments, commResp, err := g.client.PullRequests.ListReviewComments(ctx, owner, name, number, r.GetID(), nil)
		if err != nil {
			g.logger.Warn("failed to fetch review comments",
				"review_id", r.GetID(),
				"error", err,
			)
		} else {
			g.checkRate(commResp)
			for _, c := range comments {
				review.InlineComments = append(review.InlineComments, &ReviewComment{
					ID:   c.GetID(),
					Body: c.GetBody(),
					Path: c.GetPath(),
					Line: c.GetLine(),
					Side: c.GetSide(),
				})
			}
		}

		reviews = append(reviews, review)
	}

	return reviews, nil
}

// SubmitReview creates a new review on a pull request.
func (g *GitHub) SubmitReview(ctx context.Context, repo string, number int, review *ReviewSubmission) (*Review, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghReview, resp, err := g.client.PullRequests.CreateReview(ctx, owner, name, number, &github.PullRequestReviewRequest{
		Event: &review.Event,
		Body:  &review.Body,
	})
	if err != nil {
		return nil, fmt.Errorf("submit review on PR #%d: %w", number, err)
	}
	g.checkRate(resp)

	return &Review{
		ID:     ghReview.GetID(),
		Author: ghReview.GetUser().GetLogin(),
		State:  ghReview.GetState(),
		Body:   ghReview.GetBody(),
	}, nil
}

// AddReviewComment posts an inline comment on a pull request diff.
func (g *GitHub) AddReviewComment(ctx context.Context, repo string, number int, comment *ReviewComment) (*ReviewComment, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	side := comment.Side
	if side == "" {
		side = "RIGHT"
	}

	ghComment, resp, err := g.client.PullRequests.CreateComment(ctx, owner, name, number, &github.PullRequestComment{
		Body: &comment.Body,
		Path: &comment.Path,
		Line: &comment.Line,
		Side: &side,
	})
	if err != nil {
		return nil, fmt.Errorf("add review comment on PR #%d: %w", number, err)
	}
	g.checkRate(resp)

	return &ReviewComment{
		ID:   ghComment.GetID(),
		Body: ghComment.GetBody(),
		Path: ghComment.GetPath(),
		Line: ghComment.GetLine(),
		Side: ghComment.GetSide(),
	}, nil
}

// ListChecks returns CI check runs for a pull request.
func (g *GitHub) ListChecks(ctx context.Context, repo string, number int) ([]*CheckRun, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	// Get the PR to find the head SHA.
	pr, resp, err := g.client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return nil, fmt.Errorf("get PR #%d for checks: %w", number, err)
	}
	g.checkRate(resp)

	headSHA := pr.GetHead().GetSHA()
	if headSHA == "" {
		return nil, fmt.Errorf("PR #%d has no head SHA", number)
	}

	result, checkResp, err := g.client.Checks.ListCheckRunsForRef(ctx, owner, name, headSHA, nil)
	if err != nil {
		return nil, fmt.Errorf("list checks for PR #%d: %w", number, err)
	}
	g.checkRate(checkResp)

	checks := make([]*CheckRun, 0, result.GetTotal())
	for _, cr := range result.CheckRuns {
		check := &CheckRun{
			Name:       cr.GetName(),
			Status:     cr.GetStatus(),
			Conclusion: cr.GetConclusion(),
			DetailsURL: cr.GetDetailsURL(),
		}
		if cr.StartedAt != nil {
			t := cr.GetStartedAt().Time
			check.StartedAt = &t
		}
		if cr.CompletedAt != nil {
			t := cr.GetCompletedAt().Time
			check.CompletedAt = &t
		}
		checks = append(checks, check)
	}

	return checks, nil
}

// MergePR merges a pull request using the specified method.
func (g *GitHub) MergePR(ctx context.Context, repo string, number int, opts *MergeOptions) (*MergeResult, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	method := "squash"
	if opts != nil && opts.Method != "" {
		method = opts.Method
	}

	commitMsg := ""
	if opts != nil && opts.CommitMessage != "" {
		commitMsg = opts.CommitMessage
	}

	ghOpts := &github.PullRequestOptions{
		MergeMethod: method,
	}
	if opts != nil && opts.CommitTitle != "" {
		ghOpts.CommitTitle = opts.CommitTitle
	}

	result, resp, err := g.client.PullRequests.Merge(ctx, owner, name, number, commitMsg, ghOpts)
	if err != nil {
		return nil, fmt.Errorf("merge PR #%d: %w", number, err)
	}
	g.checkRate(resp)

	return &MergeResult{
		SHA:     result.GetSHA(),
		Message: result.GetMessage(),
	}, nil
}

// --- Reactions ---

// AddReaction adds an emoji reaction to an issue/PR or a specific comment.
func (g *GitHub) AddReaction(ctx context.Context, repo string, number int, commentID int64, emoji string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	if commentID > 0 {
		_, resp, err := g.client.Reactions.CreateIssueCommentReaction(ctx, owner, name, commentID, emoji)
		if err != nil {
			return fmt.Errorf("add reaction to comment %d: %w", commentID, err)
		}
		g.checkRate(resp)
	} else {
		_, resp, err := g.client.Reactions.CreateIssueReaction(ctx, owner, name, number, emoji)
		if err != nil {
			return fmt.Errorf("add reaction to #%d: %w", number, err)
		}
		g.checkRate(resp)
	}

	return nil
}

// --- Review Management ---

// RequestReview requests reviews from the specified users.
func (g *GitHub) RequestReview(ctx context.Context, repo string, number int, reviewers []string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	_, resp, err := g.client.PullRequests.RequestReviewers(ctx, owner, name, number, github.ReviewersRequest{
		Reviewers: reviewers,
	})
	if err != nil {
		return fmt.Errorf("request review on PR #%d: %w", number, err)
	}
	g.checkRate(resp)

	return nil
}

// --- Search ---

// Search performs a forge-native search of the given kind.
func (g *GitHub) Search(ctx context.Context, query string, kind SearchKind, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}

	opts := &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: limit},
	}

	switch kind {
	case SearchIssues:
		result, resp, err := g.client.Search.Issues(ctx, query, opts)
		if err != nil {
			return nil, fmt.Errorf("search issues: %w", err)
		}
		g.checkRate(resp)

		results := make([]SearchResult, 0, len(result.Issues))
		for _, i := range result.Issues {
			results = append(results, SearchResult{
				Number: i.GetNumber(),
				Title:  i.GetTitle(),
				URL:    i.GetHTMLURL(),
				Body:   truncate(i.GetBody(), 200),
			})
		}
		return results, nil

	case SearchCode:
		result, resp, err := g.client.Search.Code(ctx, query, opts)
		if err != nil {
			return nil, fmt.Errorf("search code: %w", err)
		}
		g.checkRate(resp)

		results := make([]SearchResult, 0, len(result.CodeResults))
		for _, c := range result.CodeResults {
			results = append(results, SearchResult{
				Title: c.GetName(),
				URL:   c.GetHTMLURL(),
				Body:  c.GetPath(),
			})
		}
		return results, nil

	case SearchCommits:
		result, resp, err := g.client.Search.Commits(ctx, query, opts)
		if err != nil {
			return nil, fmt.Errorf("search commits: %w", err)
		}
		g.checkRate(resp)

		results := make([]SearchResult, 0, len(result.Commits))
		for _, c := range result.Commits {
			msg := ""
			if c.Commit != nil {
				msg = truncate(c.Commit.GetMessage(), 200)
			}
			sha := c.GetSHA()
			if len(sha) > 7 {
				sha = sha[:7]
			}
			results = append(results, SearchResult{
				Title: sha,
				URL:   c.GetHTMLURL(),
				Body:  msg,
			})
		}
		return results, nil

	default:
		return nil, fmt.Errorf("unsupported search kind %q", kind)
	}
}

// --- Mapping helpers ---

func mapGitHubIssue(gi *github.Issue) *Issue {
	issue := &Issue{
		Number:    gi.GetNumber(),
		Title:     gi.GetTitle(),
		Body:      gi.GetBody(),
		State:     gi.GetState(),
		Author:    gi.GetUser().GetLogin(),
		URL:       gi.GetHTMLURL(),
		CreatedAt: gi.GetCreatedAt().Time,
		UpdatedAt: gi.GetUpdatedAt().Time,
		Comments:  gi.GetComments(),
	}
	for _, l := range gi.Labels {
		issue.Labels = append(issue.Labels, l.GetName())
	}
	for _, a := range gi.Assignees {
		issue.Assignees = append(issue.Assignees, a.GetLogin())
	}
	return issue
}

func mapGitHubComment(gc *github.IssueComment) *Comment {
	return &Comment{
		ID:        gc.GetID(),
		Body:      gc.GetBody(),
		Author:    gc.GetUser().GetLogin(),
		URL:       gc.GetHTMLURL(),
		CreatedAt: gc.GetCreatedAt().Time,
	}
}

func mapGitHubPR(gp *github.PullRequest) *PullRequest {
	pr := &PullRequest{
		Number:       gp.GetNumber(),
		Title:        gp.GetTitle(),
		Body:         gp.GetBody(),
		State:        gp.GetState(),
		Author:       gp.GetUser().GetLogin(),
		Head:         gp.GetHead().GetRef(),
		Base:         gp.GetBase().GetRef(),
		Mergeable:    gp.Mergeable,
		Additions:    gp.GetAdditions(),
		Deletions:    gp.GetDeletions(),
		ChangedFiles: gp.GetChangedFiles(),
		URL:          gp.GetHTMLURL(),
		CreatedAt:    gp.GetCreatedAt().Time,
		UpdatedAt:    gp.GetUpdatedAt().Time,
	}
	return pr
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

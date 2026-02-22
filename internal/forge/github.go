package forge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	gogithub "github.com/google/go-github/v69/github"
)

// githubProvider implements ForgeProvider using the go-github SDK.
type githubProvider struct {
	client *gogithub.Client
	owner  string // default owner for unqualified repo names
}

// splitRepo splits a "owner/repo" string into its two parts.
func splitRepo(repo string) (string, string, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q: expected owner/repo", repo)
	}
	return parts[0], parts[1], nil
}

// checkRateLimit logs a warning when remaining API calls drop below threshold.
func checkRateLimit(resp *gogithub.Response) {
	if resp == nil {
		return
	}
	if resp.Rate.Remaining < 100 {
		slog.Warn("forge: github rate limit low",
			"remaining", resp.Rate.Remaining,
			"reset", resp.Rate.Reset.Time,
		)
	}
}

// CreateIssue opens a new issue in the repository.
func (p *githubProvider) CreateIssue(ctx context.Context, repo string, issue *Issue) (*Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	req := &gogithub.IssueRequest{
		Title:     &issue.Title,
		Body:      &issue.Body,
		Labels:    &issue.Labels,
		Assignees: &issue.Assignees,
	}

	result, resp, err := p.client.Issues.Create(ctx, owner, name, req)
	if err != nil {
		return nil, fmt.Errorf("forge: create issue: %w", err)
	}
	checkRateLimit(resp)
	return convertIssue(result), nil
}

// UpdateIssue modifies an existing issue.
func (p *githubProvider) UpdateIssue(ctx context.Context, repo string, number int, update *IssueUpdate) (*Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	req := &gogithub.IssueRequest{}
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
		req.Labels = &update.Labels
	}
	if update.Assignees != nil {
		req.Assignees = &update.Assignees
	}

	result, resp, err := p.client.Issues.Edit(ctx, owner, name, number, req)
	if err != nil {
		return nil, fmt.Errorf("forge: update issue: %w", err)
	}
	checkRateLimit(resp)
	return convertIssue(result), nil
}

// GetIssue fetches a single issue by number.
func (p *githubProvider) GetIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	result, resp, err := p.client.Issues.Get(ctx, owner, name, number)
	if err != nil {
		return nil, fmt.Errorf("forge: get issue: %w", err)
	}
	checkRateLimit(resp)
	return convertIssue(result), nil
}

// ListIssues returns filtered issues from a repository.
func (p *githubProvider) ListIssues(ctx context.Context, repo string, opts *ListOptions) ([]*Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghOpts := &gogithub.IssueListByRepoOptions{
		State:     opts.State,
		Assignee:  opts.Assignee,
		Sort:      opts.Sort,
		Direction: opts.Direction,
		ListOptions: gogithub.ListOptions{
			Page:    opts.Page,
			PerPage: opts.Limit,
		},
	}
	if opts.Labels != "" {
		ghOpts.Labels = strings.Split(opts.Labels, ",")
	}
	if ghOpts.State == "" {
		ghOpts.State = "open"
	}

	results, resp, err := p.client.Issues.ListByRepo(ctx, owner, name, ghOpts)
	if err != nil {
		return nil, fmt.Errorf("forge: list issues: %w", err)
	}
	checkRateLimit(resp)

	issues := make([]*Issue, 0, len(results))
	for _, r := range results {
		// skip pull requests returned by the issues endpoint
		if r.IsPullRequest() {
			continue
		}
		issues = append(issues, convertIssue(r))
	}
	return issues, nil
}

// AddComment posts a new comment on an issue or pull request.
func (p *githubProvider) AddComment(ctx context.Context, repo string, number int, body string) (*Comment, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	result, resp, err := p.client.Issues.CreateComment(ctx, owner, name, number, &gogithub.IssueComment{
		Body: &body,
	})
	if err != nil {
		return nil, fmt.Errorf("forge: add comment: %w", err)
	}
	checkRateLimit(resp)
	return convertComment(result), nil
}

// ListPRs returns filtered pull requests from a repository.
func (p *githubProvider) ListPRs(ctx context.Context, repo string, opts *ListOptions) ([]*PullRequest, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	state := opts.State
	if state == "" {
		state = "open"
	}

	ghOpts := &gogithub.PullRequestListOptions{
		State:     state,
		Head:      opts.Head,
		Base:      opts.Base,
		Sort:      opts.Sort,
		Direction: opts.Direction,
		ListOptions: gogithub.ListOptions{
			Page:    opts.Page,
			PerPage: opts.Limit,
		},
	}

	results, resp, err := p.client.PullRequests.List(ctx, owner, name, ghOpts)
	if err != nil {
		return nil, fmt.Errorf("forge: list prs: %w", err)
	}
	checkRateLimit(resp)

	prs := make([]*PullRequest, 0, len(results))
	for _, r := range results {
		prs = append(prs, convertPR(r))
	}
	return prs, nil
}

// GetPR fetches a single pull request by number.
func (p *githubProvider) GetPR(ctx context.Context, repo string, number int) (*PullRequest, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	result, resp, err := p.client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return nil, fmt.Errorf("forge: get pr: %w", err)
	}
	checkRateLimit(resp)
	return convertPR(result), nil
}

// GetPRFiles returns the list of changed files in a pull request.
func (p *githubProvider) GetPRFiles(ctx context.Context, repo string, number int) ([]*ChangedFile, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	results, resp, err := p.client.PullRequests.ListFiles(ctx, owner, name, number, nil)
	if err != nil {
		return nil, fmt.Errorf("forge: get pr files: %w", err)
	}
	checkRateLimit(resp)

	files := make([]*ChangedFile, 0, len(results))
	for _, f := range results {
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

// GetPRDiff returns the raw unified diff for a pull request.
func (p *githubProvider) GetPRDiff(ctx context.Context, repo string, number int) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}

	diff, resp, err := p.client.PullRequests.GetRaw(ctx, owner, name, number, gogithub.RawOptions{
		Type: gogithub.Diff,
	})
	if err != nil {
		return "", fmt.Errorf("forge: get pr diff: %w", err)
	}
	checkRateLimit(resp)
	return diff, nil
}

// ListPRCommits returns the commits included in a pull request.
func (p *githubProvider) ListPRCommits(ctx context.Context, repo string, number int) ([]*Commit, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	results, resp, err := p.client.PullRequests.ListCommits(ctx, owner, name, number, nil)
	if err != nil {
		return nil, fmt.Errorf("forge: list pr commits: %w", err)
	}
	checkRateLimit(resp)

	commits := make([]*Commit, 0, len(results))
	for _, c := range results {
		commit := &Commit{
			SHA:     c.GetSHA(),
			Message: c.GetCommit().GetMessage(),
		}
		if author := c.GetCommit().GetAuthor(); author != nil {
			commit.Author = author.GetName()
			commit.Date = author.GetDate().Time
		}
		commits = append(commits, commit)
	}
	return commits, nil
}

// ListPRReviews returns reviews on a pull request with inline comments nested.
func (p *githubProvider) ListPRReviews(ctx context.Context, repo string, number int) ([]*Review, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	ghReviews, resp, err := p.client.PullRequests.ListReviews(ctx, owner, name, number, nil)
	if err != nil {
		return nil, fmt.Errorf("forge: list pr reviews: %w", err)
	}
	checkRateLimit(resp)

	// Fetch all review comments to nest them under their reviews.
	ghComments, resp, err := p.client.PullRequests.ListComments(ctx, owner, name, number, nil)
	if err != nil {
		return nil, fmt.Errorf("forge: list pr review comments: %w", err)
	}
	checkRateLimit(resp)

	// Group inline comments by their pull request review ID.
	byReview := make(map[int64][]ReviewComment, len(ghComments))
	for _, c := range ghComments {
		rid := c.GetPullRequestReviewID()
		inReplyTo := int64(0)
		if c.InReplyTo != nil {
			inReplyTo = *c.InReplyTo
		}
		byReview[rid] = append(byReview[rid], ReviewComment{
			ID:        c.GetID(),
			Path:      c.GetPath(),
			Line:      c.GetLine(),
			Side:      c.GetSide(),
			Body:      c.GetBody(),
			InReplyTo: inReplyTo,
		})
	}

	reviews := make([]*Review, 0, len(ghReviews))
	for _, r := range ghReviews {
		rev := &Review{
			ID:             r.GetID(),
			Author:         r.GetUser().GetLogin(),
			State:          r.GetState(),
			Body:           r.GetBody(),
			SubmittedAt:    r.GetSubmittedAt().Time,
			InlineComments: byReview[r.GetID()],
		}
		reviews = append(reviews, rev)
	}
	return reviews, nil
}

// SubmitReview posts a new review on a pull request.
func (p *githubProvider) SubmitReview(ctx context.Context, repo string, number int, review *Review) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	req := &gogithub.PullRequestReviewRequest{
		Body:  &review.Body,
		Event: &review.State,
	}

	_, resp, err := p.client.PullRequests.CreateReview(ctx, owner, name, number, req)
	if err != nil {
		return fmt.Errorf("forge: submit review: %w", err)
	}
	checkRateLimit(resp)
	return nil
}

// AddReviewComment adds a single inline comment to a pull request diff.
func (p *githubProvider) AddReviewComment(ctx context.Context, repo string, number int, comment *ReviewComment) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	req := &gogithub.PullRequestComment{
		Path: &comment.Path,
		Line: &comment.Line,
		Side: &comment.Side,
		Body: &comment.Body,
	}
	if comment.InReplyTo != 0 {
		req.InReplyTo = &comment.InReplyTo
	}

	_, resp, err := p.client.PullRequests.CreateComment(ctx, owner, name, number, req)
	if err != nil {
		return fmt.Errorf("forge: add review comment: %w", err)
	}
	checkRateLimit(resp)
	return nil
}

// ListChecks returns CI check runs for the head commit of a pull request.
func (p *githubProvider) ListChecks(ctx context.Context, repo string, number int) ([]*CheckRun, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	// Fetch the PR to get the head SHA.
	pr, resp, err := p.client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return nil, fmt.Errorf("forge: get pr for checks: %w", err)
	}
	checkRateLimit(resp)

	sha := pr.GetHead().GetSHA()

	result, resp, err := p.client.Checks.ListCheckRunsForRef(ctx, owner, name, sha, nil)
	if err != nil {
		return nil, fmt.Errorf("forge: list checks: %w", err)
	}
	checkRateLimit(resp)

	runs := make([]*CheckRun, 0, len(result.CheckRuns))
	for _, r := range result.CheckRuns {
		cr := &CheckRun{
			Name:       r.GetName(),
			Status:     r.GetStatus(),
			Conclusion: r.GetConclusion(),
			DetailsURL: r.GetDetailsURL(),
		}
		if r.StartedAt != nil {
			cr.StartedAt = r.StartedAt.Time
		}
		if r.CompletedAt != nil {
			cr.CompletedAt = r.CompletedAt.Time
		}
		runs = append(runs, cr)
	}
	return runs, nil
}

// MergePR merges a pull request.
func (p *githubProvider) MergePR(ctx context.Context, repo string, number int, opts *MergeOptions) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	options := &gogithub.PullRequestOptions{
		MergeMethod: opts.Method,
		CommitTitle: opts.CommitTitle,
	}

	_, resp, err := p.client.PullRequests.Merge(ctx, owner, name, number, opts.CommitMessage, options)
	if err != nil {
		return fmt.Errorf("forge: merge pr: %w", err)
	}
	checkRateLimit(resp)
	return nil
}

// AddReaction adds an emoji reaction to an issue/PR or to a specific comment.
// When commentID is 0, the reaction is applied to the issue/PR body itself.
func (p *githubProvider) AddReaction(ctx context.Context, repo string, number int, commentID int64, emoji string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	if commentID != 0 {
		_, resp, err := p.client.Reactions.CreateIssueCommentReaction(ctx, owner, name, commentID, emoji)
		if err != nil {
			return fmt.Errorf("forge: add reaction to comment: %w", err)
		}
		checkRateLimit(resp)
		return nil
	}

	_, resp, err := p.client.Reactions.CreateIssueReaction(ctx, owner, name, number, emoji)
	if err != nil {
		return fmt.Errorf("forge: add reaction: %w", err)
	}
	checkRateLimit(resp)
	return nil
}

// RequestReview requests reviews from the listed GitHub usernames.
func (p *githubProvider) RequestReview(ctx context.Context, repo string, number int, reviewers []string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	_, resp, err := p.client.PullRequests.RequestReviewers(ctx, owner, name, number, gogithub.ReviewersRequest{
		Reviewers: reviewers,
	})
	if err != nil {
		return fmt.Errorf("forge: request review: %w", err)
	}
	checkRateLimit(resp)
	return nil
}

// Search queries the forge for issues, code, or commits.
func (p *githubProvider) Search(ctx context.Context, query string, kind SearchKind) ([]SearchResult, error) {
	opts := &gogithub.SearchOptions{ListOptions: gogithub.ListOptions{PerPage: 30}}

	var results []SearchResult

	switch kind {
	case SearchKindIssues:
		r, resp, err := p.client.Search.Issues(ctx, query, opts)
		if err != nil {
			return nil, fmt.Errorf("forge: search issues: %w", err)
		}
		checkRateLimit(resp)
		for _, item := range r.Issues {
			results = append(results, SearchResult{
				Kind:    "issue",
				Number:  item.GetNumber(),
				Title:   item.GetTitle(),
				URL:     item.GetHTMLURL(),
				Snippet: item.GetBody(),
			})
		}

	case SearchKindCode:
		r, resp, err := p.client.Search.Code(ctx, query, opts)
		if err != nil {
			return nil, fmt.Errorf("forge: search code: %w", err)
		}
		checkRateLimit(resp)
		for _, item := range r.CodeResults {
			results = append(results, SearchResult{
				Kind: "code",
				Path: item.GetPath(),
				URL:  item.GetHTMLURL(),
			})
		}

	case SearchKindCommits:
		r, resp, err := p.client.Search.Commits(ctx, query, opts)
		if err != nil {
			return nil, fmt.Errorf("forge: search commits: %w", err)
		}
		checkRateLimit(resp)
		for _, item := range r.Commits {
			results = append(results, SearchResult{
				Kind:    "commit",
				SHA:     item.GetSHA(),
				Title:   item.GetCommit().GetMessage(),
				URL:     item.GetHTMLURL(),
				Snippet: item.GetCommit().GetMessage(),
			})
		}

	default:
		return nil, fmt.Errorf("forge: unsupported search kind %q", kind)
	}

	return results, nil
}

// convertIssue maps a go-github Issue to our Issue type.
func convertIssue(i *gogithub.Issue) *Issue {
	if i == nil {
		return nil
	}
	out := &Issue{
		Number:       i.GetNumber(),
		Title:        i.GetTitle(),
		Body:         i.GetBody(),
		State:        i.GetState(),
		Author:       i.GetUser().GetLogin(),
		CreatedAt:    i.GetCreatedAt().Time,
		UpdatedAt:    i.GetUpdatedAt().Time,
		URL:          i.GetHTMLURL(),
		CommentCount: i.GetComments(),
	}
	for _, l := range i.Labels {
		out.Labels = append(out.Labels, l.GetName())
	}
	for _, a := range i.Assignees {
		out.Assignees = append(out.Assignees, a.GetLogin())
	}
	return out
}

// convertComment maps a go-github IssueComment to our Comment type.
func convertComment(c *gogithub.IssueComment) *Comment {
	if c == nil {
		return nil
	}
	return &Comment{
		ID:        c.GetID(),
		Body:      c.GetBody(),
		Author:    c.GetUser().GetLogin(),
		CreatedAt: c.GetCreatedAt().Time,
		URL:       c.GetHTMLURL(),
	}
}

// convertPR maps a go-github PullRequest to our PullRequest type.
func convertPR(pr *gogithub.PullRequest) *PullRequest {
	if pr == nil {
		return nil
	}
	out := &PullRequest{
		Number:       pr.GetNumber(),
		Title:        pr.GetTitle(),
		Body:         pr.GetBody(),
		State:        pr.GetState(),
		Author:       pr.GetUser().GetLogin(),
		Head:         pr.GetHead().GetRef(),
		Base:         pr.GetBase().GetRef(),
		Additions:    pr.GetAdditions(),
		Deletions:    pr.GetDeletions(),
		ChangedFiles: pr.GetChangedFiles(),
		URL:          pr.GetHTMLURL(),
		CreatedAt:    pr.GetCreatedAt().Time,
		Draft:        pr.GetDraft(),
	}
	if pr.Mergeable != nil {
		m := pr.GetMergeable()
		out.Mergeable = &m
	}
	return out
}

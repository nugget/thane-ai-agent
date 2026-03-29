package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/awareness"
)

// Tools holds forge tool dependencies. Each Handle* method takes the
// raw argument map from the tool registry and returns formatted text
// for the LLM. Prefix resolution (temp:LABEL, kb:file.md, etc.) is
// handled universally by the tool registry's ContentResolver before
// handlers run — individual handlers receive already-resolved content.
type Tools struct {
	manager *Manager
	opLog   *OperationLog
	logger  *slog.Logger
}

// NewTools creates forge tools backed by the given manager. The opLog
// records successful operations for context injection; pass nil to
// disable operation tracking.
func NewTools(mgr *Manager, opLog *OperationLog, logger *slog.Logger) *Tools {
	return &Tools{
		manager: mgr,
		opLog:   opLog,
		logger:  logger,
	}
}

// recordOp logs a successful forge operation for context injection.
// No-ops when the operation log is nil.
func (t *Tools) recordOp(tool, account, repo, ref string) {
	if t.opLog == nil {
		return
	}
	t.opLog.Record(Operation{
		Tool:    tool,
		Account: account,
		Repo:    repo,
		Ref:     ref,
	})
}

// --- Argument extraction helpers ---

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func intArg(args map[string]any, key string) int {
	v, _ := args[key].(float64)
	return int(v)
}

func int64Arg(args map[string]any, key string) int64 {
	v, _ := args[key].(float64)
	return int64(v)
}

func stringSliceArg(args map[string]any, key string) []string {
	switch v := args[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	default:
		return nil
	}
}

// --- JSON response helpers ---

// marshalResponse marshals a value to compact JSON.
func marshalResponse(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(data), nil
}

// --- JSON response types ---

type issueGetResponse struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Author    string   `json:"author"`
	Comments  int      `json:"comments"`
	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Created   string   `json:"created"`
	Updated   string   `json:"updated"`
	URL       string   `json:"url"`
}

type issueActionResponse struct {
	Action string `json:"action"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

type issueCommentResponse struct {
	Action    string `json:"action"`
	Number    int    `json:"number"`
	CommentID int64  `json:"comment_id"`
	URL       string `json:"url"`
}

type issueListEntry struct {
	Number   int      `json:"number"`
	Title    string   `json:"title"`
	State    string   `json:"state"`
	Author   string   `json:"author"`
	Labels   []string `json:"labels,omitempty"`
	Comments int      `json:"comments"`
}

type issueListResponse struct {
	Count  int              `json:"count"`
	Issues []issueListEntry `json:"issues"`
}

type prGetChanges struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Files   int `json:"files"`
}

type prGetResponse struct {
	Number             int            `json:"number"`
	Title              string         `json:"title"`
	State              string         `json:"state"`
	Draft              bool           `json:"draft,omitempty"`
	Author             string         `json:"author"`
	Head               string         `json:"head"`
	Base               string         `json:"base"`
	Changes            prGetChanges   `json:"changes"`
	Comments           int            `json:"comments"`
	Labels             []string       `json:"labels,omitempty"`
	Assignees          []string       `json:"assignees,omitempty"`
	RequestedReviewers []string       `json:"requested_reviewers,omitempty"`
	Mergeable          *bool          `json:"mergeable,omitempty"`
	Created            string         `json:"created"`
	Updated            string         `json:"updated"`
	Reviews            map[string]int `json:"reviews,omitempty"`
	Checks             map[string]int `json:"checks,omitempty"`
	URL                string         `json:"url"`
}

type prListEntry struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Author string `json:"author"`
	Head   string `json:"head"`
	Base   string `json:"base"`
}

type prListResponse struct {
	Count int           `json:"count"`
	PRs   []prListEntry `json:"prs"`
}

type prMergeResponse struct {
	Action  string `json:"action"`
	Number  int    `json:"number"`
	SHA     string `json:"sha"`
	Message string `json:"message"`
}

type prReviewResponse struct {
	Action string `json:"action"`
	Number int    `json:"number"`
	Event  string `json:"event"`
}

type prReviewCommentResponse struct {
	Action    string `json:"action"`
	Number    int    `json:"number"`
	CommentID int64  `json:"comment_id"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
}

type reactResponse struct {
	Action   string `json:"action"`
	Number   int    `json:"number"`
	Reaction string `json:"reaction"`
}

type requestReviewResponse struct {
	Action    string   `json:"action"`
	Number    int      `json:"number"`
	Reviewers []string `json:"reviewers"`
}

type prCommitEntry struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

type prCommitsResponse struct {
	Count   int             `json:"count"`
	Commits []prCommitEntry `json:"commits"`
}

type prReviewInlineComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

type prReviewEntry struct {
	ID             int64                   `json:"id"`
	Author         string                  `json:"author"`
	State          string                  `json:"state"`
	Date           string                  `json:"date,omitempty"`
	Body           string                  `json:"body,omitempty"`
	InlineComments []prReviewInlineComment `json:"inline_comments,omitempty"`
}

type prReviewsResponse struct {
	Count   int             `json:"count"`
	Reviews []prReviewEntry `json:"reviews"`
}

type prCheckEntry struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

type prChecksResponse struct {
	Count  int            `json:"count"`
	Checks []prCheckEntry `json:"checks"`
}

type prFileEntry struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
}

type prFilesResponse struct {
	Count int           `json:"count"`
	Files []prFileEntry `json:"files"`
}

type searchResultEntry struct {
	Number int    `json:"number,omitempty"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

type searchResponse struct {
	Count   int                 `json:"count"`
	Results []searchResultEntry `json:"results"`
}

// --- Common helpers ---

// resolveAccountAndRepo extracts the account and repo from args,
// resolves the repo to owner/repo format, and returns the provider
// along with the resolved account name.
func (t *Tools) resolveAccountAndRepo(args map[string]any) (ForgeProvider, string, string, error) {
	account := stringArg(args, "account")
	repo := stringArg(args, "repo")
	if repo == "" {
		return nil, "", "", fmt.Errorf("repo is required")
	}

	// Resolve empty account to primary.
	resolvedAccount := account
	if resolvedAccount == "" && len(t.manager.order) > 0 {
		resolvedAccount = t.manager.order[0]
	}

	provider, err := t.manager.Account(account)
	if err != nil {
		return nil, "", "", err
	}

	fullRepo, err := t.manager.ResolveRepo(account, repo)
	if err != nil {
		return nil, "", "", err
	}

	return provider, fullRepo, resolvedAccount, nil
}

// --- Issue handlers ---

// HandleIssueCreate creates a new issue on a forge repository.
func (t *Tools) HandleIssueCreate(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	title := stringArg(args, "title")
	if title == "" {
		return "", fmt.Errorf("title is required")
	}

	body := stringArg(args, "body")

	issue, err := provider.CreateIssue(ctx, repo, &Issue{
		Title:     title,
		Body:      body,
		Labels:    stringSliceArg(args, "labels"),
		Assignees: stringSliceArg(args, "assignees"),
	})
	if err != nil {
		return "", err
	}

	t.recordOp("forge_issue_create", acct, repo, fmt.Sprintf("#%d", issue.Number))
	return marshalResponse(issueActionResponse{
		Action: "created",
		Number: issue.Number,
		Title:  issue.Title,
		URL:    issue.URL,
	})
}

// HandleIssueUpdate updates an existing issue. Body REPLACES the
// entire issue body when provided — it does not append.
func (t *Tools) HandleIssueUpdate(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	update := &IssueUpdate{}
	if v, ok := args["title"].(string); ok && v != "" {
		update.Title = &v
	}
	if v, ok := args["body"].(string); ok {
		update.Body = &v
	}
	if v, ok := args["state"].(string); ok && v != "" {
		update.State = &v
	}
	if labels := stringSliceArg(args, "labels"); labels != nil {
		update.Labels = &labels
	}

	issue, err := provider.UpdateIssue(ctx, repo, number, update)
	if err != nil {
		return "", err
	}

	t.recordOp("forge_issue_update", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(issueActionResponse{
		Action: "updated",
		Number: issue.Number,
		Title:  issue.Title,
		URL:    issue.URL,
	})
}

// HandleIssueGet retrieves a single issue by number.
func (t *Tools) HandleIssueGet(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	issue, err := provider.GetIssue(ctx, repo, number)
	if err != nil {
		return "", err
	}

	now := time.Now()

	resp := issueGetResponse{
		Number:    issue.Number,
		Title:     issue.Title,
		State:     issue.State,
		Author:    issue.Author,
		Comments:  issue.CommentCount,
		Labels:    issue.Labels,
		Assignees: issue.Assignees,
		Created:   awareness.FormatDeltaOnly(issue.CreatedAt, now),
		Updated:   awareness.FormatDeltaOnly(issue.UpdatedAt, now),
		URL:       issue.URL,
	}

	result, err := marshalResponse(resp)
	if err != nil {
		return "", err
	}

	if issue.Body != "" {
		result += "\n\n---\n" + issue.Body
	}

	t.recordOp("forge_issue_get", acct, repo, fmt.Sprintf("#%d", number))
	return result, nil
}

// HandleIssueList lists issues matching the given filters.
func (t *Tools) HandleIssueList(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	opts := &ListOptions{
		State:     stringArg(args, "state"),
		Labels:    stringArg(args, "labels"),
		Assignee:  stringArg(args, "assignee"),
		Sort:      stringArg(args, "sort"),
		Direction: stringArg(args, "direction"),
		Limit:     intArg(args, "limit"),
		Page:      intArg(args, "page"),
	}

	issues, err := provider.ListIssues(ctx, repo, opts)
	if err != nil {
		return "", err
	}

	if len(issues) == 0 {
		t.recordOp("forge_issue_list", acct, repo, "")
		return "No issues found.", nil
	}

	entries := make([]issueListEntry, 0, len(issues))
	for _, i := range issues {
		entries = append(entries, issueListEntry{
			Number:   i.Number,
			Title:    i.Title,
			State:    i.State,
			Author:   i.Author,
			Labels:   i.Labels,
			Comments: i.CommentCount,
		})
	}

	t.recordOp("forge_issue_list", acct, repo, "")
	return marshalResponse(issueListResponse{
		Count:  len(entries),
		Issues: entries,
	})
}

// HandleIssueComment posts a comment on an issue or pull request.
func (t *Tools) HandleIssueComment(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	body := stringArg(args, "body")
	if body == "" {
		return "", fmt.Errorf("body is required")
	}
	comment, err := provider.AddComment(ctx, repo, number, body)
	if err != nil {
		return "", err
	}

	t.recordOp("forge_issue_comment", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(issueCommentResponse{
		Action:    "comment_added",
		Number:    number,
		CommentID: comment.ID,
		URL:       comment.URL,
	})
}

// --- PR handlers ---

// HandlePRList lists pull requests matching the given filters.
func (t *Tools) HandlePRList(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	opts := &ListOptions{
		State:     stringArg(args, "state"),
		Base:      stringArg(args, "base"),
		Head:      stringArg(args, "head"),
		Sort:      stringArg(args, "sort"),
		Direction: stringArg(args, "direction"),
		Limit:     intArg(args, "limit"),
		Page:      intArg(args, "page"),
	}

	prs, err := provider.ListPRs(ctx, repo, opts)
	if err != nil {
		return "", err
	}

	if len(prs) == 0 {
		t.recordOp("forge_pr_list", acct, repo, "")
		return "No pull requests found.", nil
	}

	entries := make([]prListEntry, 0, len(prs))
	for _, p := range prs {
		entries = append(entries, prListEntry{
			Number: p.Number,
			Title:  p.Title,
			State:  p.State,
			Author: p.Author,
			Head:   p.Head,
			Base:   p.Base,
		})
	}

	t.recordOp("forge_pr_list", acct, repo, "")
	return marshalResponse(prListResponse{
		Count: len(entries),
		PRs:   entries,
	})
}

// HandlePRGet retrieves a single pull request by number.
func (t *Tools) HandlePRGet(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	pr, err := provider.GetPR(ctx, repo, number)
	if err != nil {
		return "", err
	}

	now := time.Now()

	resp := prGetResponse{
		Number: pr.Number,
		Title:  pr.Title,
		State:  pr.State,
		Draft:  pr.Draft,
		Author: pr.Author,
		Head:   pr.Head,
		Base:   pr.Base,
		Changes: prGetChanges{
			Added:   pr.Additions,
			Removed: pr.Deletions,
			Files:   pr.ChangedFiles,
		},
		Comments:           pr.CommentCount,
		Labels:             pr.Labels,
		Assignees:          pr.Assignees,
		RequestedReviewers: pr.RequestedReviewers,
		Mergeable:          pr.Mergeable,
		Created:            awareness.FormatDeltaOnly(pr.CreatedAt, now),
		Updated:            awareness.FormatDeltaOnly(pr.UpdatedAt, now),
		URL:                pr.URL,
	}

	// Inline review summary (extra API call, but saves a follow-up tool call).
	if reviews, err := provider.ListPRReviews(ctx, repo, number); err == nil && len(reviews) > 0 {
		counts := map[string]int{}
		for _, r := range reviews {
			counts[r.State]++
		}
		resp.Reviews = counts
	}

	// Inline check summary (extra API call, but saves a follow-up tool call).
	if checks, err := provider.ListChecks(ctx, repo, number); err == nil && len(checks) > 0 {
		summary := map[string]int{}
		for _, c := range checks {
			switch {
			case c.Conclusion == "success" || c.Conclusion == "skipped" || c.Conclusion == "neutral":
				summary["passed"]++
			case c.Status != "completed":
				summary["pending"]++
			default:
				summary["failed"]++
			}
		}
		resp.Checks = summary
	}

	result, err := marshalResponse(resp)
	if err != nil {
		return "", err
	}

	if pr.Body != "" {
		result += "\n\n---\n" + pr.Body
	}

	t.recordOp("forge_pr_get", acct, repo, fmt.Sprintf("#%d", number))
	return result, nil
}

// HandlePRDiff returns the unified diff for a pull request, truncated
// at max_lines (default 2000).
func (t *Tools) HandlePRDiff(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	maxLines := intArg(args, "max_lines")
	if maxLines <= 0 {
		maxLines = 2000
	}

	diff, err := provider.GetPRDiff(ctx, repo, number)
	if err != nil {
		return "", err
	}

	lines := strings.Split(diff, "\n")
	if len(lines) > maxLines {
		truncated := strings.Join(lines[:maxLines], "\n")
		t.recordOp("forge_pr_diff", acct, repo, fmt.Sprintf("#%d", number))
		return fmt.Sprintf("%s\n\n[diff truncated, %d more lines]", truncated, len(lines)-maxLines), nil
	}

	t.recordOp("forge_pr_diff", acct, repo, fmt.Sprintf("#%d", number))
	return diff, nil
}

// HandlePRFiles returns the files changed in a pull request.
func (t *Tools) HandlePRFiles(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	files, err := provider.GetPRFiles(ctx, repo, number)
	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		t.recordOp("forge_pr_files", acct, repo, fmt.Sprintf("#%d", number))
		return "No changed files.", nil
	}

	entries := make([]prFileEntry, 0, len(files))
	for _, f := range files {
		entries = append(entries, prFileEntry{
			Filename: f.Filename,
			Status:   f.Status,
			Added:    f.Additions,
			Removed:  f.Deletions,
		})
	}

	t.recordOp("forge_pr_files", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(prFilesResponse{
		Count: len(entries),
		Files: entries,
	})
}

// HandlePRCommits returns commits in a pull request.
func (t *Tools) HandlePRCommits(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	commits, err := provider.ListPRCommits(ctx, repo, number)
	if err != nil {
		return "", err
	}

	if len(commits) == 0 {
		t.recordOp("forge_pr_commits", acct, repo, fmt.Sprintf("#%d", number))
		return "No commits.", nil
	}

	now := time.Now()
	entries := make([]prCommitEntry, 0, len(commits))
	for _, c := range commits {
		// First line of commit message only.
		msg := c.Message
		if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
			msg = msg[:idx]
		}
		entries = append(entries, prCommitEntry{
			SHA:     c.SHA,
			Message: msg,
			Author:  c.Author,
			Date:    awareness.FormatDeltaOnly(c.Date, now),
		})
	}

	t.recordOp("forge_pr_commits", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(prCommitsResponse{
		Count:   len(entries),
		Commits: entries,
	})
}

// HandlePRReviews returns reviews for a pull request with inline comments.
func (t *Tools) HandlePRReviews(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	reviews, err := provider.ListPRReviews(ctx, repo, number)
	if err != nil {
		return "", err
	}

	if len(reviews) == 0 {
		t.recordOp("forge_pr_reviews", acct, repo, fmt.Sprintf("#%d", number))
		return "No reviews.", nil
	}

	now := time.Now()
	entries := make([]prReviewEntry, 0, len(reviews))
	for _, r := range reviews {
		entry := prReviewEntry{
			ID:     r.ID,
			Author: r.Author,
			State:  r.State,
			Body:   r.Body,
		}
		if !r.SubmittedAt.IsZero() {
			entry.Date = awareness.FormatDeltaOnly(r.SubmittedAt, now)
		}
		for _, c := range r.InlineComments {
			entry.InlineComments = append(entry.InlineComments, prReviewInlineComment{
				Path: c.Path,
				Line: c.Line,
				Body: c.Body,
			})
		}
		entries = append(entries, entry)
	}

	t.recordOp("forge_pr_reviews", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(prReviewsResponse{
		Count:   len(entries),
		Reviews: entries,
	})
}

// HandlePRReview submits a review on a pull request.
func (t *Tools) HandlePRReview(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	event := stringArg(args, "event")
	if event == "" {
		return "", fmt.Errorf("event is required (APPROVE, COMMENT, or REQUEST_CHANGES)")
	}

	body := stringArg(args, "body")
	if body == "" {
		return "", fmt.Errorf("body is required")
	}
	review, err := provider.SubmitReview(ctx, repo, number, &ReviewSubmission{
		Event: event,
		Body:  body,
	})
	if err != nil {
		return "", err
	}

	t.recordOp("forge_pr_review", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(prReviewResponse{
		Action: "review_submitted",
		Number: number,
		Event:  review.State,
	})
}

// HandlePRReviewComment posts an inline comment on a pull request diff.
func (t *Tools) HandlePRReviewComment(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	body := stringArg(args, "body")
	if body == "" {
		return "", fmt.Errorf("body is required")
	}
	path := stringArg(args, "path")
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	line := intArg(args, "line")
	if line == 0 {
		return "", fmt.Errorf("line is required")
	}

	comment, err := provider.AddReviewComment(ctx, repo, number, &ReviewComment{
		Body: body,
		Path: path,
		Line: line,
		Side: stringArg(args, "side"),
	})
	if err != nil {
		return "", err
	}

	t.recordOp("forge_pr_review_comment", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(prReviewCommentResponse{
		Action:    "review_comment_added",
		Number:    number,
		CommentID: comment.ID,
		Path:      comment.Path,
		Line:      comment.Line,
	})
}

// HandlePRChecks returns CI check runs for a pull request.
func (t *Tools) HandlePRChecks(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	checks, err := provider.ListChecks(ctx, repo, number)
	if err != nil {
		return "", err
	}

	if len(checks) == 0 {
		t.recordOp("forge_pr_checks", acct, repo, fmt.Sprintf("#%d", number))
		return "No check runs found.", nil
	}

	entries := make([]prCheckEntry, 0, len(checks))
	for _, c := range checks {
		entries = append(entries, prCheckEntry{
			Name:       c.Name,
			Status:     c.Status,
			Conclusion: c.Conclusion,
			URL:        c.DetailsURL,
		})
	}

	t.recordOp("forge_pr_checks", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(prChecksResponse{
		Count:  len(entries),
		Checks: entries,
	})
}

// HandlePRMerge merges a pull request.
func (t *Tools) HandlePRMerge(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	opts := &MergeOptions{
		Method:        stringArg(args, "method"),
		CommitTitle:   stringArg(args, "commit_title"),
		CommitMessage: stringArg(args, "commit_message"),
	}

	result, err := provider.MergePR(ctx, repo, number, opts)
	if err != nil {
		return "", err
	}

	t.recordOp("forge_pr_merge", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(prMergeResponse{
		Action:  "merged",
		Number:  number,
		SHA:     result.SHA,
		Message: result.Message,
	})
}

// --- Reaction handler ---

// HandleReact adds an emoji reaction to an issue, PR, or comment.
func (t *Tools) HandleReact(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	emoji := stringArg(args, "emoji")
	if emoji == "" {
		return "", fmt.Errorf("emoji is required (+1, -1, laugh, confused, heart, hooray, rocket, eyes)")
	}

	commentID := int64Arg(args, "comment_id")

	if err := provider.AddReaction(ctx, repo, number, commentID, emoji); err != nil {
		return "", err
	}

	t.recordOp("forge_react", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(reactResponse{
		Action:   "reaction_added",
		Number:   number,
		Reaction: emoji,
	})
}

// --- Review request handler ---

// HandleRequestReview requests reviews from specified users.
func (t *Tools) HandleRequestReview(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	reviewers := stringSliceArg(args, "reviewers")
	if len(reviewers) == 0 {
		return "", fmt.Errorf("reviewers is required")
	}

	if err := provider.RequestReview(ctx, repo, number, reviewers); err != nil {
		return "", err
	}

	t.recordOp("forge_pr_request_review", acct, repo, fmt.Sprintf("#%d", number))
	return marshalResponse(requestReviewResponse{
		Action:    "review_requested",
		Number:    number,
		Reviewers: reviewers,
	})
}

// --- Search handler ---

// HandleSearch performs a forge-native search.
func (t *Tools) HandleSearch(ctx context.Context, args map[string]any) (string, error) {
	account := stringArg(args, "account")
	resolvedAcct := account
	if resolvedAcct == "" && len(t.manager.order) > 0 {
		resolvedAcct = t.manager.order[0]
	}
	provider, err := t.manager.Account(account)
	if err != nil {
		return "", err
	}

	query := stringArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	kindStr := stringArg(args, "kind")
	if kindStr == "" {
		return "", fmt.Errorf("kind is required (issues, code, commits)")
	}

	limit := intArg(args, "limit")

	results, err := provider.Search(ctx, query, SearchKind(kindStr), limit)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		t.recordOp("forge_search", resolvedAcct, "", kindStr+": "+query)
		return "No results found.", nil
	}

	entries := make([]searchResultEntry, 0, len(results))
	for _, r := range results {
		entries = append(entries, searchResultEntry{
			Number: r.Number,
			Title:  r.Title,
			URL:    r.URL,
		})
	}

	t.recordOp("forge_search", resolvedAcct, "", kindStr+": "+query)
	return marshalResponse(searchResponse{
		Count:   len(entries),
		Results: entries,
	})
}

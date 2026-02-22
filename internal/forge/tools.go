package forge

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// TempFileResolver resolves temp:LABEL references to filesystem paths.
// The tools package's TempFileStore satisfies this interface.
type TempFileResolver interface {
	Resolve(convID, label string) string
}

// ConversationIDFunc extracts the conversation ID from the context.
// Injected at wiring time to avoid importing the tools package.
type ConversationIDFunc func(ctx context.Context) string

// Tools holds forge tool dependencies. Each Handle* method takes the
// raw argument map from the tool registry and returns formatted text
// for the LLM.
type Tools struct {
	manager        *Manager
	tempFiles      TempFileResolver
	conversationID ConversationIDFunc
	logger         *slog.Logger
}

// NewTools creates forge tools backed by the given manager and optional
// temp file resolver for temp:LABEL expansion in body parameters. The
// convIDFunc extracts the conversation ID from context (typically
// tools.ConversationIDFromContext); pass nil if temp file resolution
// is not needed.
func NewTools(mgr *Manager, tempFiles TempFileResolver, convIDFunc ConversationIDFunc, logger *slog.Logger) *Tools {
	return &Tools{
		manager:        mgr,
		tempFiles:      tempFiles,
		conversationID: convIDFunc,
		logger:         logger,
	}
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

// --- Temp label resolution ---

// resolveBody checks if value starts with "temp:" and returns the file
// contents if so. Otherwise returns value as-is.
func (t *Tools) resolveBody(ctx context.Context, value string) (string, error) {
	if !strings.HasPrefix(value, "temp:") {
		return value, nil
	}
	if t.tempFiles == nil {
		return "", fmt.Errorf("temp file resolution not available")
	}
	label := strings.TrimPrefix(value, "temp:")
	convIDFunc := t.conversationID
	if convIDFunc == nil {
		return "", fmt.Errorf("temp file resolution requires conversation ID function")
	}
	convID := convIDFunc(ctx)
	path := t.tempFiles.Resolve(convID, label)
	if path == "" {
		return "", fmt.Errorf("unknown temp label %q", label)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read temp file %q: %w", label, err)
	}
	return string(data), nil
}

// --- Common helpers ---

// resolveAccountAndRepo extracts the account and repo from args,
// resolves the repo to owner/repo format, and returns the provider.
func (t *Tools) resolveAccountAndRepo(args map[string]any) (ForgeProvider, string, error) {
	account := stringArg(args, "account")
	repo := stringArg(args, "repo")
	if repo == "" {
		return nil, "", fmt.Errorf("repo is required")
	}

	provider, err := t.manager.Account(account)
	if err != nil {
		return nil, "", err
	}

	fullRepo, err := t.manager.ResolveRepo(account, repo)
	if err != nil {
		return nil, "", err
	}

	return provider, fullRepo, nil
}

// --- Issue handlers ---

// HandleIssueCreate creates a new issue on a forge repository.
func (t *Tools) HandleIssueCreate(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	title := stringArg(args, "title")
	if title == "" {
		return "", fmt.Errorf("title is required")
	}

	body := stringArg(args, "body")
	body, err = t.resolveBody(ctx, body)
	if err != nil {
		return "", err
	}

	issue, err := provider.CreateIssue(ctx, repo, &Issue{
		Title:     title,
		Body:      body,
		Labels:    stringSliceArg(args, "labels"),
		Assignees: stringSliceArg(args, "assignees"),
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Created issue #%d: %s\nURL: %s", issue.Number, issue.Title, issue.URL), nil
}

// HandleIssueUpdate updates an existing issue. Body REPLACES the
// entire issue body when provided — it does not append.
func (t *Tools) HandleIssueUpdate(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
		resolved, err := t.resolveBody(ctx, v)
		if err != nil {
			return "", err
		}
		update.Body = &resolved
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

	return fmt.Sprintf("Updated issue #%d: %s\nURL: %s", issue.Number, issue.Title, issue.URL), nil
}

// HandleIssueGet retrieves a single issue by number.
func (t *Tools) HandleIssueGet(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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

	var sb strings.Builder
	fmt.Fprintf(&sb, "Issue #%d: %s\n", issue.Number, issue.Title)
	fmt.Fprintf(&sb, "State: %s | Author: %s\n", issue.State, issue.Author)
	if len(issue.Labels) > 0 {
		fmt.Fprintf(&sb, "Labels: %s\n", strings.Join(issue.Labels, ", "))
	}
	if len(issue.Assignees) > 0 {
		fmt.Fprintf(&sb, "Assignees: %s\n", strings.Join(issue.Assignees, ", "))
	}
	fmt.Fprintf(&sb, "Created: %s | Updated: %s\n", issue.CreatedAt.Format("2006-01-02"), issue.UpdatedAt.Format("2006-01-02"))
	fmt.Fprintf(&sb, "URL: %s\n", issue.URL)
	if issue.Body != "" {
		fmt.Fprintf(&sb, "\n---\n%s", issue.Body)
	}

	return sb.String(), nil
}

// HandleIssueList lists issues matching the given filters.
func (t *Tools) HandleIssueList(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
		return "No issues found.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d issue(s):\n\n", len(issues))
	for _, i := range issues {
		labels := ""
		if len(i.Labels) > 0 {
			labels = " [" + strings.Join(i.Labels, ", ") + "]"
		}
		fmt.Fprintf(&sb, "#%d %s (%s)%s — %s, %d comments\n",
			i.Number, i.Title, i.State, labels, i.Author, i.Comments)
	}

	return sb.String(), nil
}

// HandleIssueComment posts a comment on an issue or pull request.
func (t *Tools) HandleIssueComment(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
	body, err = t.resolveBody(ctx, body)
	if err != nil {
		return "", err
	}

	comment, err := provider.AddComment(ctx, repo, number, body)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Comment added (ID: %d)\nURL: %s", comment.ID, comment.URL), nil
}

// --- PR handlers ---

// HandlePRList lists pull requests matching the given filters.
func (t *Tools) HandlePRList(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
		return "No pull requests found.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d PR(s):\n\n", len(prs))
	for _, p := range prs {
		fmt.Fprintf(&sb, "#%d %s (%s) — %s → %s — by %s\n",
			p.Number, p.Title, p.State, p.Head, p.Base, p.Author)
	}

	return sb.String(), nil
}

// HandlePRGet retrieves a single pull request by number.
func (t *Tools) HandlePRGet(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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

	var sb strings.Builder
	fmt.Fprintf(&sb, "PR #%d: %s\n", pr.Number, pr.Title)
	fmt.Fprintf(&sb, "State: %s | Author: %s\n", pr.State, pr.Author)
	fmt.Fprintf(&sb, "Branch: %s → %s\n", pr.Head, pr.Base)
	fmt.Fprintf(&sb, "Changes: +%d -%d across %d files\n", pr.Additions, pr.Deletions, pr.ChangedFiles)
	if pr.Mergeable != nil {
		fmt.Fprintf(&sb, "Mergeable: %v\n", *pr.Mergeable)
	}
	fmt.Fprintf(&sb, "Created: %s | Updated: %s\n", pr.CreatedAt.Format("2006-01-02"), pr.UpdatedAt.Format("2006-01-02"))
	fmt.Fprintf(&sb, "URL: %s\n", pr.URL)
	if pr.Body != "" {
		fmt.Fprintf(&sb, "\n---\n%s", pr.Body)
	}

	return sb.String(), nil
}

// HandlePRDiff returns the unified diff for a pull request, truncated
// at max_lines (default 2000).
func (t *Tools) HandlePRDiff(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
		return fmt.Sprintf("%s\n\n[diff truncated, %d more lines]", truncated, len(lines)-maxLines), nil
	}

	return diff, nil
}

// HandlePRFiles returns the files changed in a pull request.
func (t *Tools) HandlePRFiles(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
		return "No changed files.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d changed file(s):\n\n", len(files))
	for _, f := range files {
		fmt.Fprintf(&sb, "%s (%s) +%d -%d\n", f.Filename, f.Status, f.Additions, f.Deletions)
		if f.Patch != "" {
			fmt.Fprintf(&sb, "```\n%s\n```\n\n", f.Patch)
		}
	}

	return sb.String(), nil
}

// HandlePRCommits returns commits in a pull request.
func (t *Tools) HandlePRCommits(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
		return "No commits.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d commit(s):\n\n", len(commits))
	for _, c := range commits {
		// First line of commit message only.
		msg := c.Message
		if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
			msg = msg[:idx]
		}
		fmt.Fprintf(&sb, "%s %s — %s (%s)\n",
			c.SHA, msg, c.Author, c.Date.Format("2006-01-02"))
	}

	return sb.String(), nil
}

// HandlePRReviews returns reviews for a pull request with inline comments.
func (t *Tools) HandlePRReviews(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
		return "No reviews.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d review(s):\n\n", len(reviews))
	for _, r := range reviews {
		fmt.Fprintf(&sb, "Review #%d by %s — %s", r.ID, r.Author, r.State)
		if !r.SubmittedAt.IsZero() {
			fmt.Fprintf(&sb, " (%s)", r.SubmittedAt.Format("2006-01-02 15:04"))
		}
		sb.WriteString("\n")
		if r.Body != "" {
			fmt.Fprintf(&sb, "  %s\n", r.Body)
		}
		for _, c := range r.InlineComments {
			fmt.Fprintf(&sb, "  → %s:%d: %s\n", c.Path, c.Line, c.Body)
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// HandlePRReview submits a review on a pull request.
func (t *Tools) HandlePRReview(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
	body, err = t.resolveBody(ctx, body)
	if err != nil {
		return "", err
	}

	review, err := provider.SubmitReview(ctx, repo, number, &ReviewSubmission{
		Event: event,
		Body:  body,
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Review submitted (ID: %d, state: %s)", review.ID, review.State), nil
}

// HandlePRReviewComment posts an inline comment on a pull request diff.
func (t *Tools) HandlePRReviewComment(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
	body, err = t.resolveBody(ctx, body)
	if err != nil {
		return "", err
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

	return fmt.Sprintf("Review comment added (ID: %d) on %s:%d", comment.ID, comment.Path, comment.Line), nil
}

// HandlePRChecks returns CI check runs for a pull request.
func (t *Tools) HandlePRChecks(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
		return "No check runs found.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d check run(s):\n\n", len(checks))
	for _, c := range checks {
		fmt.Fprintf(&sb, "%s: %s", c.Name, c.Status)
		if c.Conclusion != "" {
			fmt.Fprintf(&sb, " (%s)", c.Conclusion)
		}
		if c.DetailsURL != "" {
			fmt.Fprintf(&sb, " — %s", c.DetailsURL)
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// HandlePRMerge merges a pull request.
func (t *Tools) HandlePRMerge(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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
	if opts.CommitMessage != "" {
		resolved, err := t.resolveBody(ctx, opts.CommitMessage)
		if err != nil {
			return "", err
		}
		opts.CommitMessage = resolved
	}

	result, err := provider.MergePR(ctx, repo, number, opts)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("PR #%d merged (SHA: %s): %s", number, result.SHA, result.Message), nil
}

// --- Reaction handler ---

// HandleReact adds an emoji reaction to an issue, PR, or comment.
func (t *Tools) HandleReact(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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

	if commentID > 0 {
		return fmt.Sprintf("Added :%s: reaction to comment %d on #%d", emoji, commentID, number), nil
	}
	return fmt.Sprintf("Added :%s: reaction to #%d", emoji, number), nil
}

// --- Review request handler ---

// HandleRequestReview requests reviews from specified users.
func (t *Tools) HandleRequestReview(ctx context.Context, args map[string]any) (string, error) {
	provider, repo, err := t.resolveAccountAndRepo(args)
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

	return fmt.Sprintf("Requested review from %s on PR #%d", strings.Join(reviewers, ", "), number), nil
}

// --- Search handler ---

// HandleSearch performs a forge-native search.
func (t *Tools) HandleSearch(ctx context.Context, args map[string]any) (string, error) {
	account := stringArg(args, "account")
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
		return "No results found.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d result(s):\n\n", len(results))
	for _, r := range results {
		if r.Number > 0 {
			fmt.Fprintf(&sb, "#%d ", r.Number)
		}
		fmt.Fprintf(&sb, "%s\n  %s\n", r.Title, r.URL)
		if r.Body != "" {
			fmt.Fprintf(&sb, "  %s\n", r.Body)
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

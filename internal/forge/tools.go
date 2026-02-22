package forge

import (
	"context"
	"fmt"
	"strings"
)

// TempFileResolver resolves "temp:LABEL" references to file contents.
// This matches the interface implemented by internal/tools.TempFileStore.
type TempFileResolver interface {
	// Resolve returns the absolute file path for a label within a conversation,
	// or an empty string when the label is unknown.
	Resolve(convID, label string) string
}

// Tools holds forge tool dependencies and exposes Handle* methods for each
// registered tool.
type Tools struct {
	registry  *Registry
	tempFiles TempFileResolver
}

// NewTools creates a Tools instance backed by the given registry.
// tempFiles may be nil if no temp-file resolution is needed.
func NewTools(registry *Registry, tempFiles TempFileResolver) *Tools {
	return &Tools{registry: registry, tempFiles: tempFiles}
}

// resolveParam expands a "temp:LABEL" reference using the conversation ID
// embedded in ctx. Plain values are returned unchanged.
func (t *Tools) resolveParam(ctx context.Context, value string) (string, error) {
	if !strings.HasPrefix(value, "temp:") {
		return value, nil
	}
	if t.tempFiles == nil {
		return "", fmt.Errorf("forge: temp file resolver not configured")
	}
	label := strings.TrimPrefix(value, "temp:")
	convID, _ := ctx.Value(convIDKey{}).(string)
	path := t.tempFiles.Resolve(convID, label)
	if path == "" {
		return "", fmt.Errorf("forge: temp file %q not found", label)
	}
	return path, nil
}

// convIDKey is the context key type for the conversation ID.
type convIDKey struct{}

// HandleIssueCreate creates a new issue.
func (t *Tools) HandleIssueCreate(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	title := stringArg(args, "title")
	if title == "" {
		return "", fmt.Errorf("title is required")
	}

	body, err := t.resolveParam(ctx, stringArg(args, "body"))
	if err != nil {
		return "", err
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	issue := &Issue{
		Title:     title,
		Body:      body,
		Labels:    stringSliceArg(args, "labels"),
		Assignees: stringSliceArg(args, "assignees"),
	}

	result, err := provider.CreateIssue(ctx, owner+"/"+repoName, issue)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Created issue #%d: %s\n%s", result.Number, result.Title, result.URL), nil
}

// HandleIssueUpdate updates an existing issue.
func (t *Tools) HandleIssueUpdate(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	update := &IssueUpdate{}
	if v := stringArg(args, "title"); v != "" {
		update.Title = &v
	}
	if v := stringArg(args, "body"); v != "" {
		update.Body = &v
	}
	if v := stringArg(args, "state"); v != "" {
		update.State = &v
	}
	if l := stringSliceArg(args, "labels"); l != nil {
		update.Labels = l
	}
	if a := stringSliceArg(args, "assignees"); a != nil {
		update.Assignees = a
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	result, err := provider.UpdateIssue(ctx, owner+"/"+repoName, number, update)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Updated issue #%d: %s\n%s", result.Number, result.Title, result.URL), nil
}

// HandleIssueGet fetches a single issue.
func (t *Tools) HandleIssueGet(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	issue, err := provider.GetIssue(ctx, owner+"/"+repoName, number)
	if err != nil {
		return "", err
	}
	return formatIssue(issue), nil
}

// HandleIssueList lists issues in a repository.
func (t *Tools) HandleIssueList(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	opts := &ListOptions{
		State:     stringArg(args, "state"),
		Labels:    stringArg(args, "labels"),
		Assignee:  stringArg(args, "assignee"),
		Sort:      stringArg(args, "sort"),
		Direction: stringArg(args, "direction"),
		Limit:     intArg(args, "limit"),
		Page:      intArg(args, "page"),
	}

	issues, err := provider.ListIssues(ctx, owner+"/"+repoName, opts)
	if err != nil {
		return "", err
	}
	if len(issues) == 0 {
		return "No issues found.", nil
	}
	var sb strings.Builder
	for _, i := range issues {
		fmt.Fprintf(&sb, "#%d [%s] %s\n  Labels: %s  Assignees: %s\n  %s\n\n",
			i.Number, i.State, i.Title,
			strings.Join(i.Labels, ", "),
			strings.Join(i.Assignees, ", "),
			i.URL,
		)
	}
	return sb.String(), nil
}

// HandleIssueComment posts a comment on an issue or PR.
func (t *Tools) HandleIssueComment(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}
	body, err := t.resolveParam(ctx, stringArg(args, "body"))
	if err != nil {
		return "", err
	}
	if body == "" {
		return "", fmt.Errorf("body is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	comment, err := provider.AddComment(ctx, owner+"/"+repoName, number, body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Comment posted: %s", comment.URL), nil
}

// HandlePRList lists pull requests in a repository.
func (t *Tools) HandlePRList(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	opts := &ListOptions{
		State:     stringArg(args, "state"),
		Head:      stringArg(args, "head"),
		Base:      stringArg(args, "base"),
		Sort:      stringArg(args, "sort"),
		Direction: stringArg(args, "direction"),
		Limit:     intArg(args, "limit"),
		Page:      intArg(args, "page"),
	}

	prs, err := provider.ListPRs(ctx, owner+"/"+repoName, opts)
	if err != nil {
		return "", err
	}
	if len(prs) == 0 {
		return "No pull requests found.", nil
	}
	var sb strings.Builder
	for _, pr := range prs {
		draft := ""
		if pr.Draft {
			draft = " [DRAFT]"
		}
		fmt.Fprintf(&sb, "#%d [%s]%s %s (%s → %s)\n  %s\n\n",
			pr.Number, pr.State, draft, pr.Title, pr.Head, pr.Base, pr.URL)
	}
	return sb.String(), nil
}

// HandlePRGet fetches a single pull request.
func (t *Tools) HandlePRGet(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	pr, err := provider.GetPR(ctx, owner+"/"+repoName, number)
	if err != nil {
		return "", err
	}
	return formatPR(pr), nil
}

// HandlePRFiles lists changed files in a pull request.
func (t *Tools) HandlePRFiles(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	files, err := provider.GetPRFiles(ctx, owner+"/"+repoName, number)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "No changed files.", nil
	}
	var sb strings.Builder
	for _, f := range files {
		fmt.Fprintf(&sb, "[%s] %s (+%d/-%d)\n", f.Status, f.Filename, f.Additions, f.Deletions)
	}
	return sb.String(), nil
}

// HandlePRDiff returns the raw diff for a pull request with optional truncation.
func (t *Tools) HandlePRDiff(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}
	maxLines := intArg(args, "max_lines")

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	diff, err := provider.GetPRDiff(ctx, owner+"/"+repoName, number)
	if err != nil {
		return "", err
	}

	if maxLines > 0 {
		lines := strings.Split(diff, "\n")
		if len(lines) > maxLines {
			truncated := len(lines) - maxLines
			lines = lines[:maxLines]
			diff = strings.Join(lines, "\n") + fmt.Sprintf("\n[diff truncated, %d more lines]", truncated)
		}
	}
	return diff, nil
}

// HandlePRCommits lists commits in a pull request.
func (t *Tools) HandlePRCommits(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	commits, err := provider.ListPRCommits(ctx, owner+"/"+repoName, number)
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "No commits found.", nil
	}
	var sb strings.Builder
	for _, c := range commits {
		fmt.Fprintf(&sb, "%s  %s  (%s)\n", c.SHA[:min(7, len(c.SHA))], firstLine(c.Message), c.Author)
	}
	return sb.String(), nil
}

// HandlePRReviews lists reviews on a pull request.
func (t *Tools) HandlePRReviews(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	reviews, err := provider.ListPRReviews(ctx, owner+"/"+repoName, number)
	if err != nil {
		return "", err
	}
	if len(reviews) == 0 {
		return "No reviews found.", nil
	}
	var sb strings.Builder
	for _, r := range reviews {
		fmt.Fprintf(&sb, "%s by %s (%s)\n", r.State, r.Author, r.SubmittedAt.Format("2006-01-02"))
		if r.Body != "" {
			fmt.Fprintf(&sb, "  %s\n", r.Body)
		}
		for _, c := range r.InlineComments {
			fmt.Fprintf(&sb, "  [%s:%d] %s\n", c.Path, c.Line, c.Body)
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// HandlePRReview submits a review on a pull request.
func (t *Tools) HandlePRReview(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}
	event := stringArg(args, "event")
	if event == "" {
		return "", fmt.Errorf("event is required")
	}

	body, err := t.resolveParam(ctx, stringArg(args, "body"))
	if err != nil {
		return "", err
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	review := &Review{
		Body:  body,
		State: event,
	}
	if err := provider.SubmitReview(ctx, owner+"/"+repoName, number, review); err != nil {
		return "", err
	}
	return fmt.Sprintf("Review submitted (%s) on PR #%d.", event, number), nil
}

// HandlePRReviewComment adds an inline comment to a pull request diff.
func (t *Tools) HandlePRReviewComment(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}
	path := stringArg(args, "path")
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	body := stringArg(args, "body")
	if body == "" {
		return "", fmt.Errorf("body is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	comment := &ReviewComment{
		Path:      path,
		Line:      intArg(args, "line"),
		Side:      stringArg(args, "side"),
		Body:      body,
		InReplyTo: int64Arg(args, "in_reply_to"),
	}
	if err := provider.AddReviewComment(ctx, owner+"/"+repoName, number, comment); err != nil {
		return "", err
	}
	return fmt.Sprintf("Review comment added to %s:%d on PR #%d.", path, comment.Line, number), nil
}

// HandlePRChecks lists CI check runs for a pull request.
func (t *Tools) HandlePRChecks(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	checks, err := provider.ListChecks(ctx, owner+"/"+repoName, number)
	if err != nil {
		return "", err
	}
	if len(checks) == 0 {
		return "No checks found.", nil
	}
	var sb strings.Builder
	for _, c := range checks {
		fmt.Fprintf(&sb, "[%s] %s → %s\n  %s\n", c.Status, c.Name, c.Conclusion, c.DetailsURL)
	}
	return sb.String(), nil
}

// HandlePRMerge merges a pull request.
func (t *Tools) HandlePRMerge(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
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
	if opts.Method == "" {
		opts.Method = "merge"
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	if err := provider.MergePR(ctx, owner+"/"+repoName, number, opts); err != nil {
		return "", err
	}
	return fmt.Sprintf("PR #%d merged via %s.", number, opts.Method), nil
}

// HandleReaction adds an emoji reaction to an issue/PR or comment.
func (t *Tools) HandleReaction(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}
	emoji := stringArg(args, "emoji")
	if emoji == "" {
		return "", fmt.Errorf("emoji is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	commentID := int64Arg(args, "comment_id")
	if err := provider.AddReaction(ctx, owner+"/"+repoName, number, commentID, emoji); err != nil {
		return "", err
	}
	return fmt.Sprintf("Reaction %q added.", emoji), nil
}

// HandleRequestReview requests reviews from listed users on a PR.
func (t *Tools) HandleRequestReview(ctx context.Context, args map[string]any) (string, error) {
	repo := stringArg(args, "repo")
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	number := intArg(args, "number")
	if number == 0 {
		return "", fmt.Errorf("number is required")
	}
	reviewers := stringSliceArg(args, "reviewers")
	if len(reviewers) == 0 {
		return "", fmt.Errorf("reviewers is required")
	}

	provider, cfg, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}
	owner, repoName := t.registry.ResolveRepo(cfg, repo)

	if err := provider.RequestReview(ctx, owner+"/"+repoName, number, reviewers); err != nil {
		return "", err
	}
	return fmt.Sprintf("Review requested from %s on PR #%d.", strings.Join(reviewers, ", "), number), nil
}

// HandleSearch searches the forge for issues, code, or commits.
func (t *Tools) HandleSearch(ctx context.Context, args map[string]any) (string, error) {
	query := stringArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	kind := SearchKind(stringArg(args, "kind"))
	if kind == "" {
		kind = SearchKindIssues
	}

	provider, _, err := t.registry.Account(stringArg(args, "account"))
	if err != nil {
		return "", err
	}

	results, err := provider.Search(ctx, query, kind)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "No results found.", nil
	}
	var sb strings.Builder
	for _, r := range results {
		switch r.Kind {
		case "issue":
			fmt.Fprintf(&sb, "#%d %s\n  %s\n\n", r.Number, r.Title, r.URL)
		case "code":
			fmt.Fprintf(&sb, "%s\n  %s\n\n", r.Path, r.URL)
		case "commit":
			fmt.Fprintf(&sb, "%s %s\n  %s\n\n", r.SHA[:min(7, len(r.SHA))], firstLine(r.Title), r.URL)
		}
	}
	return sb.String(), nil
}

// --- argument helpers ---

// stringArg extracts a string argument from an args map.
func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// intArg extracts an integer argument from an args map.
func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case int64:
		return int(v)
	}
	return 0
}

// int64Arg extracts an int64 argument from an args map.
func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	}
	return 0
}

// stringSliceArg extracts a []string argument from an args map.
// Returns nil when the key is absent or empty.
func stringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []string:
		if len(val) == 0 {
			return nil
		}
		return val
	case []any:
		if len(val) == 0 {
			return nil
		}
		out := make([]string, 0, len(val))
		for _, s := range val {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// --- formatting helpers ---

func formatIssue(i *Issue) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Issue #%d [%s]: %s\nAuthor: %s  Created: %s\nLabels: %s  Assignees: %s\nComments: %d\n%s\n\n%s",
		i.Number, i.State, i.Title,
		i.Author, i.CreatedAt.Format("2006-01-02"),
		strings.Join(i.Labels, ", "),
		strings.Join(i.Assignees, ", "),
		i.CommentCount,
		i.URL,
		i.Body,
	)
	return sb.String()
}

func formatPR(pr *PullRequest) string {
	mergeable := "unknown"
	if pr.Mergeable != nil {
		if *pr.Mergeable {
			mergeable = "yes"
		} else {
			mergeable = "no"
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "PR #%d [%s]: %s\nAuthor: %s  %s → %s  Draft: %v\nMergeable: %s  Review: %s\n+%d/-%d in %d files\n%s\n\n%s",
		pr.Number, pr.State, pr.Title,
		pr.Author, pr.Head, pr.Base, pr.Draft,
		mergeable, pr.ReviewState,
		pr.Additions, pr.Deletions, pr.ChangedFiles,
		pr.URL,
		pr.Body,
	)
	return sb.String()
}

// firstLine returns the first line of a multi-line string.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

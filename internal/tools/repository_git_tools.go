package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

const (
	repositoryGitTimeout     = 15 * time.Second
	repositoryGitOutputLimit = 32 * 1024
	repositoryDiffPatchLimit = 16 * 1024
)

type repositoryGitCommit struct {
	SHA         string `json:"sha"`
	ShortSHA    string `json:"short_sha"`
	AuthorName  string `json:"author_name,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
	Age         string `json:"age,omitempty"`
	Subject     string `json:"subject,omitempty"`
}

type repositoryGitLogResult struct {
	Root      string                `json:"root"`
	From      string                `json:"from,omitempty"`
	To        string                `json:"to"`
	Path      string                `json:"path,omitempty"`
	Commits   []repositoryGitCommit `json:"commits"`
	Truncated bool                  `json:"truncated,omitempty"`
}

type repositoryGitBodyResult struct {
	Root      string `json:"root"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Path      string `json:"path,omitempty"`
	Format    string `json:"format"`
	Body      string `json:"body"`
	Note      string `json:"note,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type repositoryBlameLine struct {
	Line        int    `json:"line"`
	Commit      string `json:"commit"`
	Author      string `json:"author,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
	Age         string `json:"age,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Content     string `json:"content"`
}

type repositoryGitBlameResult struct {
	Root      string                `json:"root"`
	Revision  string                `json:"revision"`
	Path      string                `json:"path"`
	Lines     []repositoryBlameLine `json:"lines"`
	Truncated bool                  `json:"truncated,omitempty"`
}

// RepositoryGitLog returns commit history from one repository root.
func (ft *FileTools) RepositoryGitLog(ctx context.Context, requestedRoot, from, to, path string, limit int) (string, error) {
	root, err := ft.resolveRepositoryRoot(ctx, requestedRoot)
	if err != nil {
		return "", err
	}
	path, err = repositoryRelativePath(path, false)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	toSHA, err := resolveRepositoryRevision(ctx, root.Path, defaultText(to, "HEAD"))
	if err != nil {
		return "", fmt.Errorf("to: %w", err)
	}
	selector := toSHA
	fromSHA := ""
	if strings.TrimSpace(from) != "" {
		fromSHA, err = resolveRepositoryRevision(ctx, root.Path, from)
		if err != nil {
			return "", fmt.Errorf("from: %w", err)
		}
		selector = fromSHA + ".." + toSHA
	}
	args := []string{"log", "--no-decorate", "-n", strconv.Itoa(limit), "--format=%H%x00%aN%x00%aE%x00%aI%x00%s%x1e", selector, "--"}
	if path != "" {
		args = append(args, path)
	}
	out, truncated, err := runRepositoryGit(ctx, root.Path, repositoryGitOutputLimit, args...)
	if err != nil {
		return "", err
	}
	now := time.Now()
	commits := make([]repositoryGitCommit, 0)
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x00", 5)
		if len(parts) != 5 {
			continue
		}
		commit := repositoryGitCommit{SHA: parts[0], ShortSHA: shortSHA(parts[0]), AuthorName: parts[1], AuthorEmail: parts[2], Subject: parts[4]}
		if authored, parseErr := time.Parse(time.RFC3339, parts[3]); parseErr == nil {
			commit.Age = promptfmt.FormatDeltaOnly(authored, now)
		}
		commits = append(commits, commit)
	}
	return marshalRepositoryGit(repositoryGitLogResult{Root: root.Name, From: fromSHA, To: toSHA, Path: path, Commits: commits, Truncated: truncated})
}

// RepositoryGitDiff returns a bounded patch or diffstat from one repository root.
func (ft *FileTools) RepositoryGitDiff(ctx context.Context, requestedRoot, from, to, path, format string) (string, error) {
	root, err := ft.resolveRepositoryRoot(ctx, requestedRoot)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(from) == "" {
		return "", fmt.Errorf("from is required; pass the prior last_synced_sha or another commit")
	}
	path, err = repositoryRelativePath(path, false)
	if err != nil {
		return "", err
	}
	fromSHA, err := resolveRepositoryRevision(ctx, root.Path, from)
	if err != nil {
		return "", fmt.Errorf("from: %w", err)
	}
	toSHA, err := resolveRepositoryRevision(ctx, root.Path, defaultText(to, "HEAD"))
	if err != nil {
		return "", fmt.Errorf("to: %w", err)
	}
	if format == "" {
		format = "patch"
	}
	if format != "patch" && format != "stat" {
		return "", fmt.Errorf("format must be patch or stat")
	}
	body, truncated, err := repositoryDiff(ctx, root.Path, fromSHA, toSHA, path, format, repositoryGitOutputLimit)
	if err != nil {
		return "", err
	}
	note := ""
	if format == "patch" && (truncated || len(body) > repositoryDiffPatchLimit) {
		body, truncated, err = repositoryDiff(ctx, root.Path, fromSHA, toSHA, path, "stat", repositoryGitOutputLimit)
		if err != nil {
			return "", fmt.Errorf("patch exceeded %d bytes and diffstat fallback failed: %w", repositoryDiffPatchLimit, err)
		}
		format = "stat"
		note = fmt.Sprintf("patch exceeded %d bytes; showing diffstat — narrow the range or path for the full patch", repositoryDiffPatchLimit)
	}
	return marshalRepositoryGit(repositoryGitBodyResult{Root: root.Name, From: fromSHA, To: toSHA, Path: path, Format: format, Body: body, Note: note, Truncated: truncated})
}

// RepositoryGitShow returns one bounded commit display from a repository root.
func (ft *FileTools) RepositoryGitShow(ctx context.Context, requestedRoot, revision, path string) (string, error) {
	root, err := ft.resolveRepositoryRoot(ctx, requestedRoot)
	if err != nil {
		return "", err
	}
	path, err = repositoryRelativePath(path, false)
	if err != nil {
		return "", err
	}
	sha, err := resolveRepositoryRevision(ctx, root.Path, defaultText(revision, "HEAD"))
	if err != nil {
		return "", err
	}
	showFormat, err := repositoryGitShowFormat(ctx, root.Path, sha, time.Now())
	if err != nil {
		return "", err
	}
	args := []string{"show", "--no-ext-diff", "--no-textconv", "--no-color", "--format=" + showFormat, sha, "--"}
	if path != "" {
		args = append(args, path)
	}
	body, truncated, err := runRepositoryGit(ctx, root.Path, repositoryGitOutputLimit, args...)
	if err != nil {
		return "", err
	}
	note := ""
	format := "patch"
	if truncated {
		args = []string{"show", "--no-ext-diff", "--no-textconv", "--no-color", "--format=" + showFormat, "--stat", sha, "--"}
		if path != "" {
			args = append(args, path)
		}
		body, truncated, err = runRepositoryGit(ctx, root.Path, repositoryGitOutputLimit, args...)
		if err != nil {
			return "", fmt.Errorf("commit patch exceeded output limit and diffstat fallback failed: %w", err)
		}
		format = "stat"
		note = "commit patch exceeded the output limit; showing commit metadata and diffstat"
	}
	return marshalRepositoryGit(repositoryGitBodyResult{Root: root.Name, Revision: sha, Path: path, Format: format, Body: body, Note: note, Truncated: truncated})
}

// RepositoryGitBlame returns structured line attribution for one file.
func (ft *FileTools) RepositoryGitBlame(ctx context.Context, requestedRoot, revision, path string, startLine, endLine int) (string, error) {
	root, err := ft.resolveRepositoryRoot(ctx, requestedRoot)
	if err != nil {
		return "", err
	}
	path, err = repositoryRelativePath(path, true)
	if err != nil {
		return "", err
	}
	sha, err := resolveRepositoryRevision(ctx, root.Path, defaultText(revision, "HEAD"))
	if err != nil {
		return "", err
	}
	args := []string{"blame", "--line-porcelain"}
	if startLine > 0 || endLine > 0 {
		if startLine <= 0 {
			startLine = 1
		}
		if endLine <= 0 {
			endLine = startLine
		}
		if endLine < startLine {
			return "", fmt.Errorf("end_line must be greater than or equal to start_line")
		}
		args = append(args, "-L", fmt.Sprintf("%d,%d", startLine, endLine))
	}
	args = append(args, sha, "--", path)
	out, truncated, err := runRepositoryGit(ctx, root.Path, repositoryGitOutputLimit, args...)
	if err != nil {
		return "", err
	}
	return marshalRepositoryGit(repositoryGitBlameResult{Root: root.Name, Revision: sha, Path: path, Lines: parseRepositoryBlame(out, time.Now()), Truncated: truncated})
}

func (ft *FileTools) resolveRepositoryRoot(ctx context.Context, requested string) (paths.Root, error) {
	requested = strings.TrimSuffix(strings.TrimSpace(requested), ":")
	bound := looppkg.BindingFromContext(ctx, looppkg.BindingRepositoryRoot)
	if bound != "" {
		if requested != "" && requested != bound {
			return paths.Root{}, fmt.Errorf("repository root %q is not available here: this loop is bound to root %q; retry with root=%q or omit the argument", requested, bound, bound)
		}
		requested = bound
	}
	if requested == "" {
		known := ft.resolver.RepositoryRoots()
		names := make([]string, 0, len(known))
		for _, root := range known {
			names = append(names, root.Name)
		}
		return paths.Root{}, fmt.Errorf("root is required for an unbound caller; known repository roots: %s", strings.Join(names, ", "))
	}
	root, ok := ft.resolver.Root(requested)
	if !ok || root.Kind != paths.RootKindRepository {
		known := ft.resolver.RepositoryRoots()
		names := make([]string, 0, len(known))
		for _, candidate := range known {
			names = append(names, candidate.Name)
		}
		return paths.Root{}, fmt.Errorf("unknown repository root %q; known repository roots: %s", requested, strings.Join(names, ", "))
	}
	return root, nil
}

func resolveRepositoryRevision(ctx context.Context, repoPath, selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", fmt.Errorf("revision is required")
	}
	out, _, err := runRepositoryGit(ctx, repoPath, 1024, "rev-parse", "--verify", "--quiet", "--end-of-options", selector+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("revision %q does not name a commit: %w", selector, err)
	}
	sha := strings.TrimSpace(out)
	if len(sha) != 40 && len(sha) != 64 {
		return "", fmt.Errorf("revision %q resolved to an invalid commit id", selector)
	}
	return sha, nil
}

func repositoryGitShowFormat(ctx context.Context, repoPath, sha string, now time.Time) (string, error) {
	raw, truncated, err := runRepositoryGit(ctx, repoPath, 64, "show", "-s", "--format=%at", sha, "--")
	if err != nil {
		return "", fmt.Errorf("read commit authored time: %w", err)
	}
	if truncated {
		return "", fmt.Errorf("read commit authored time: output exceeded limit")
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return "", fmt.Errorf("read commit authored time: %w", err)
	}
	age := promptfmt.FormatDeltaOnly(time.Unix(seconds, 0), now)
	return "commit %H%nAuthor: %an <%ae>%nAuthored: " + age + "%n%n    %s", nil
}

func repositoryRelativePath(path string, required bool) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		if required {
			return "", fmt.Errorf("path is required")
		}
		return "", nil
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("path must be relative to the repository root")
	}
	clean := filepath.Clean(path)
	if clean == "." {
		if required {
			return "", fmt.Errorf("path must name a file inside the repository root")
		}
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the repository root")
	}
	return filepath.ToSlash(clean), nil
}

func repositoryDiff(ctx context.Context, repoPath, from, to, path, format string, limit int) (string, bool, error) {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}
	if format == "stat" {
		args = append(args, "--stat")
	}
	args = append(args, from, to, "--")
	if path != "" {
		args = append(args, path)
	}
	return runRepositoryGit(ctx, repoPath, limit, args...)
}

type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := w.max - w.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			w.buf.Write(p[:remaining])
		} else {
			w.buf.Write(p)
		}
	}
	if len(p) > remaining {
		w.truncated = true
	}
	return written, nil
}

func runRepositoryGit(ctx context.Context, repoPath string, limit int, args ...string) (string, bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, repositoryGitTimeout)
	defer cancel()
	gitArgs := append([]string{"-C", repoPath, "-c", "core.pager=cat", "-c", "color.ui=false"}, args...)
	cmd := exec.CommandContext(commandCtx, "git", gitArgs...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_PAGER=cat")
	stdout := &cappedBuffer{max: limit}
	stderr := &cappedBuffer{max: 4096}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if commandCtx.Err() != nil {
			return "", false, fmt.Errorf("git command timed out or was canceled: %w", commandCtx.Err())
		}
		detail := strings.TrimSpace(stderr.buf.String())
		if detail == "" {
			detail = err.Error()
		}
		detail = strings.ReplaceAll(detail, repoPath, "[repository root]")
		return "", false, fmt.Errorf("git command failed: %s", detail)
	}
	return stdout.buf.String(), stdout.truncated, nil
}

func parseRepositoryBlame(out string, now time.Time) []repositoryBlameLine {
	lines := strings.Split(out, "\n")
	result := make([]repositoryBlameLine, 0)
	var current repositoryBlameLine
	var authoredAt time.Time
	for _, line := range lines {
		if strings.HasPrefix(line, "\t") {
			current.Content = strings.TrimPrefix(line, "\t")
			if !authoredAt.IsZero() {
				current.Age = promptfmt.FormatDeltaOnly(authoredAt, now)
			}
			result = append(result, current)
			current = repositoryBlameLine{}
			authoredAt = time.Time{}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && (len(fields[0]) == 40 || len(fields[0]) == 64) {
			current.Commit = fields[0]
			current.Line, _ = strconv.Atoi(fields[2])
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "author":
			current.Author = value
		case "author-mail":
			current.AuthorEmail = strings.Trim(value, "<>")
		case "author-time":
			if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
				authoredAt = time.Unix(unix, 0)
			}
		case "summary":
			current.Summary = value
		}
	}
	return result
}

func marshalRepositoryGit(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal repository git result: %w", err)
	}
	return string(data), nil
}

func textArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func numberArg(args map[string]any, key string) int {
	value, _ := args[key].(float64)
	return int(value)
}

func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

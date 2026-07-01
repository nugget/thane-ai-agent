package provenance

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Reader exposes read-only revision history, diff, and point-in-time recall
// over a git-backed store, scoped to a single file. Both [*Store] and
// [*Verifier] satisfy it, so a verify-only root (which has a Verifier but no
// Store) can still be read.
//
// filename is always repo-relative; callers that map a repository onto a
// subtree translate the prefix before calling.
type Reader interface {
	// ResolveRevision maps a selector onto a concrete revision of filename.
	// Accepted selectors: "" / "HEAD" / "latest" (newest commit touching the
	// file), an RFC3339 timestamp (newest commit at or before it), or a
	// commit-ish hash (must exist and the file must exist at it). Relative
	// deltas are resolved to timestamps by the caller, not here.
	ResolveRevision(ctx context.Context, filename, selector string) (Revision, error)

	// Blob returns the file's content as of rev.
	Blob(ctx context.Context, rev, filename string) (string, error)

	// Diff returns the change to filename between two revisions.
	Diff(ctx context.Context, from, to, filename string, format DiffFormat) (RevisionDiff, error)

	// Revisions returns a page of the file's commit history, newest first.
	Revisions(ctx context.Context, filename string, opts RevisionOptions) (RevisionPage, error)
}

// Revision identifies one commit in a file's history.
type Revision struct {
	// Commit is the full commit hash.
	Commit string
	// Short is a shortened hash for display.
	Short string
	// Index is the number of commits that touched this file and are strictly
	// newer than this revision (0 for the newest). It is a reasoning aid, not
	// an addressing token.
	Index int
	// Timestamp is the commit's author time.
	Timestamp time.Time
	// Message is the commit subject.
	Message string
}

// RevisionOptions bounds a [Reader.Revisions] page.
type RevisionOptions struct {
	// Limit caps the page size. Non-positive uses the default; larger than
	// the max is clamped.
	Limit int
	// Before, when set to a commit hash, returns only revisions strictly
	// older than it — the pagination cursor. Empty starts at HEAD.
	Before string
}

// RevisionPage is one page of file history.
type RevisionPage struct {
	// Revisions are ordered newest first.
	Revisions []Revision
	// Total is the file's full revision count.
	Total int
	// NextBefore is the cursor for the next (older) page, or "" when the page
	// reached the end of history.
	NextBefore string
}

// DiffFormat selects the body a [Reader.Diff] returns.
type DiffFormat string

const (
	// DiffPatch returns a unified diff.
	DiffPatch DiffFormat = "patch"
	// DiffStat returns a diffstat summary.
	DiffStat DiffFormat = "stat"
)

// RevisionDiff is the change to one file between two revisions.
type RevisionDiff struct {
	Format  DiffFormat
	Added   int
	Removed int
	// Body is the unified diff (DiffPatch) or the diffstat (DiffStat).
	Body string
}

const (
	defaultRevisionLimit = 20
	maxRevisionLimit     = 100
	shortHashLen         = 12
)

// --- *Store satisfies Reader (locks the write mutex to stay consistent with
// concurrent commits) ---

var _ Reader = (*Store)(nil)

func (s *Store) ResolveRevision(ctx context.Context, filename, selector string) (Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return resolveRevision(ctx, s.path, filename, selector)
}

func (s *Store) Blob(ctx context.Context, rev, filename string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readBlob(ctx, s.path, rev, filename)
}

func (s *Store) Diff(ctx context.Context, from, to, filename string, format DiffFormat) (RevisionDiff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readDiff(ctx, s.path, from, to, filename, format)
}

func (s *Store) Revisions(ctx context.Context, filename string, opts RevisionOptions) (RevisionPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readRevisions(ctx, s.path, filename, opts)
}

// --- *Verifier satisfies Reader (read-only, so a required root without a
// Store can still be inspected) ---

var _ Reader = (*Verifier)(nil)

func (v *Verifier) ResolveRevision(ctx context.Context, filename, selector string) (Revision, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return resolveRevision(ctx, v.path, filename, selector)
}

func (v *Verifier) Blob(ctx context.Context, rev, filename string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return readBlob(ctx, v.path, rev, filename)
}

func (v *Verifier) Diff(ctx context.Context, from, to, filename string, format DiffFormat) (RevisionDiff, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return readDiff(ctx, v.path, from, to, filename, format)
}

func (v *Verifier) Revisions(ctx context.Context, filename string, opts RevisionOptions) (RevisionPage, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return readRevisions(ctx, v.path, filename, opts)
}

// --- shared implementations ---

func resolveRevision(ctx context.Context, repoPath, filename, selector string) (Revision, error) {
	sel := strings.TrimSpace(selector)
	switch strings.ToLower(sel) {
	case "", "head", "latest":
		commit, err := runGitText(ctx, repoPath, "rev-list", "-1", "HEAD", "--", filename)
		if err != nil {
			return Revision{}, fmt.Errorf("resolve revision: %w", err)
		}
		return describeRevision(ctx, repoPath, filename, strings.TrimSpace(commit), sel)
	}

	if t, err := time.Parse(time.RFC3339, sel); err == nil {
		commit, err := runGitText(ctx, repoPath, "rev-list", "-1",
			"--before="+t.UTC().Format(time.RFC3339), "HEAD", "--", filename)
		if err != nil {
			return Revision{}, fmt.Errorf("resolve revision at %s: %w", sel, err)
		}
		return describeRevision(ctx, repoPath, filename, strings.TrimSpace(commit), sel)
	}

	// Otherwise treat the selector as a commit-ish hash.
	full, err := runGitText(ctx, repoPath, "rev-parse", "--verify", "--quiet", sel+"^{commit}")
	if err != nil {
		return Revision{}, fmt.Errorf("revision %q is not a known commit, timestamp, or HEAD", selector)
	}
	commit := strings.TrimSpace(full)
	// Per-file guardrail: the file must exist at that commit, so a real hash
	// that has nothing to do with this document is rejected rather than
	// silently returning an empty diff or blob.
	if err := runGitCheck(ctx, repoPath, "cat-file", "-e", commit+":"+filename); err != nil {
		return Revision{}, fmt.Errorf("file %s does not exist at revision %s", filename, shorten(commit))
	}
	return describeRevision(ctx, repoPath, filename, commit, sel)
}

// describeRevision fills a Revision for a resolved commit, or errors when no
// commit matched the selector (empty commit).
func describeRevision(ctx context.Context, repoPath, filename, commit, selector string) (Revision, error) {
	if commit == "" {
		return Revision{}, fmt.Errorf("no revision of %s at or before %q", filename, selector)
	}
	meta, err := runGitText(ctx, repoPath, "log", "-1", "--format=%H%x00%aI%x00%s", commit)
	if err != nil {
		return Revision{}, fmt.Errorf("describe revision %s: %w", shorten(commit), err)
	}
	rev, err := parseRevisionLine(strings.TrimSpace(meta))
	if err != nil {
		return Revision{}, err
	}
	rev.Index, err = revisionIndex(ctx, repoPath, filename, rev.Commit)
	if err != nil {
		return Revision{}, err
	}
	return rev, nil
}

// revisionIndex counts the file-touching commits strictly newer than commit.
func revisionIndex(ctx context.Context, repoPath, filename, commit string) (int, error) {
	out, err := runGitText(ctx, repoPath, "rev-list", "--count", commit+"..HEAD", "--", filename)
	if err != nil {
		return 0, fmt.Errorf("count revisions: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse revision index %q: %w", out, err)
	}
	return n, nil
}

func readBlob(ctx context.Context, repoPath, rev, filename string) (string, error) {
	rev = strings.TrimSpace(rev)
	if rev == "" {
		rev = "HEAD"
	}
	// Raw content — no trimming, since a document's own leading/trailing
	// whitespace is meaningful.
	out, err := runGitText(ctx, repoPath, "show", rev+":"+filename)
	if err != nil {
		return "", fmt.Errorf("read %s at %s: %w", filename, shorten(rev), err)
	}
	return out, nil
}

func readDiff(ctx context.Context, repoPath, from, to, filename string, format DiffFormat) (RevisionDiff, error) {
	if format != DiffPatch && format != DiffStat {
		format = DiffPatch
	}
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)

	added, removed, err := diffNumstat(ctx, repoPath, from, to, filename)
	if err != nil {
		return RevisionDiff{}, err
	}

	bodyArgs := []string{"diff", "--no-color"}
	if format == DiffStat {
		bodyArgs = append(bodyArgs, "--stat")
	}
	bodyArgs = append(bodyArgs, from, to, "--", filename)
	body, err := runGitText(ctx, repoPath, bodyArgs...)
	if err != nil {
		return RevisionDiff{}, fmt.Errorf("diff %s %s..%s: %w", filename, shorten(from), shorten(to), err)
	}
	return RevisionDiff{Format: format, Added: added, Removed: removed, Body: body}, nil
}

// diffNumstat returns the added/removed line counts for filename between two
// revisions. Binary changes (numstat "-") report as zero.
func diffNumstat(ctx context.Context, repoPath, from, to, filename string) (int, int, error) {
	out, err := runGitText(ctx, repoPath, "diff", "--numstat", from, to, "--", filename)
	if err != nil {
		return 0, 0, fmt.Errorf("diffstat %s: %w", filename, err)
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return 0, 0, nil
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, 0, nil
	}
	added, _ := strconv.Atoi(fields[0])   // "-" (binary) → 0
	removed, _ := strconv.Atoi(fields[1]) // "-" (binary) → 0
	return added, removed, nil
}

func readRevisions(ctx context.Context, repoPath, filename string, opts RevisionOptions) (RevisionPage, error) {
	total := 0
	if out, err := runGitText(ctx, repoPath, "rev-list", "--count", "HEAD", "--", filename); err == nil {
		total, _ = strconv.Atoi(strings.TrimSpace(out))
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = defaultRevisionLimit
	}
	if limit > maxRevisionLimit {
		limit = maxRevisionLimit
	}

	start := "HEAD"
	if before := strings.TrimSpace(opts.Before); before != "" {
		// Exclude the cursor commit and everything newer; its first parent
		// and older is the next page.
		start = before + "^"
	}

	// Fetch one extra to detect whether an older page exists.
	meta, err := runGitText(ctx, repoPath, "log",
		"--format=%H%x00%aI%x00%s", fmt.Sprintf("-n%d", limit+1), start, "--", filename)
	if err != nil {
		// An empty repo or an out-of-range cursor yields no history.
		return RevisionPage{Total: total}, nil
	}

	var revs []Revision
	for _, line := range strings.Split(strings.TrimSpace(meta), "\n") {
		if line == "" {
			continue
		}
		rev, err := parseRevisionLine(line)
		if err != nil {
			return RevisionPage{}, err
		}
		revs = append(revs, rev)
	}
	if len(revs) == 0 {
		return RevisionPage{Total: total}, nil
	}

	// The first entry's index anchors the page; the rest follow sequentially.
	baseIndex, err := revisionIndex(ctx, repoPath, filename, revs[0].Commit)
	if err != nil {
		return RevisionPage{}, err
	}

	page := RevisionPage{Total: total}
	for i := range revs {
		if i >= limit {
			// The extra entry only signals a next page.
			page.NextBefore = revs[limit-1].Commit
			break
		}
		revs[i].Index = baseIndex + i
		page.Revisions = append(page.Revisions, revs[i])
	}
	return page, nil
}

// parseRevisionLine parses a "hash\x00RFC3339\x00subject" log line.
func parseRevisionLine(line string) (Revision, error) {
	parts := strings.SplitN(line, "\x00", 3)
	if len(parts) != 3 {
		return Revision{}, fmt.Errorf("malformed revision line %q", line)
	}
	t, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return Revision{}, fmt.Errorf("parse revision timestamp %q: %w", parts[1], err)
	}
	return Revision{
		Commit:    parts[0],
		Short:     shorten(parts[0]),
		Timestamp: t,
		Message:   parts[2],
	}, nil
}

func shorten(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > shortHashLen {
		return hash[:shortHashLen]
	}
	return hash
}

// runGitText runs a read-only git command in repoPath and returns its raw
// stdout. Stderr is kept separate and surfaced only in the error, so it never
// contaminates blob or diff output.
func runGitText(ctx context.Context, repoPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s: %w", msg, err)
		}
		return "", err
	}
	return stdout.String(), nil
}

// runGitCheck runs a git command only for its exit status.
func runGitCheck(ctx context.Context, repoPath string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)
	return cmd.Run()
}

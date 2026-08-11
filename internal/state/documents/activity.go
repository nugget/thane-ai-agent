package documents

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ActivityQuery bounds one root's revision-churn report.
type ActivityQuery struct {
	// Root names the document root to report on. Required; the root must
	// keep revision history (be git-backed).
	Root string
	// Since is the window start. Required.
	Since time.Time
	// Limit caps the documents in the report (default 20, max 100).
	// Flagged documents sort first, so the cap cannot hide a runaway.
	Limit int
	// RevisionThreshold flags a document with at least this many
	// in-window revisions (default 8).
	RevisionThreshold int
}

const (
	defaultActivityLimit     = 20
	maxActivityLimit         = 100
	defaultRevisionThreshold = 8
	// activityHistoryPage bounds the per-document history read. A
	// document with more in-window revisions than this reports the page
	// size as its count — far past any sane threshold, so the flag has
	// long since tripped.
	activityHistoryPage = 100
)

// ActivityAuthor attributes a share of one document's in-window
// revisions. Author is the writing loop's ID from the commit trailers,
// or "manual" for revisions with no authorship block — which were, by
// definition, not written by a loop.
type ActivityAuthor struct {
	Author    string `json:"author"`
	Model     string `json:"model,omitempty"`
	Revisions int    `json:"revisions"`
}

// DocumentActivity is one document's churn over the window.
type DocumentActivity struct {
	Ref            string           `json:"ref"`
	Revisions      int              `json:"revisions"`
	LinesAdded     int              `json:"lines_added"`
	LinesRemoved   int              `json:"lines_removed"`
	NetLineDelta   int              `json:"net_line_delta"`
	SizeBytes      int64            `json:"size_bytes"`
	WordCount      int              `json:"word_count"`
	LastRevisionAt time.Time        `json:"-"`
	Authors        []ActivityAuthor `json:"authors,omitempty"`
	Flagged        bool             `json:"flagged,omitempty"`
	FlagReason     string           `json:"flag_reason,omitempty"`
}

// ActivityReport is one root's churn summary: which documents changed
// in the window, how much, and by whose hand — flagged runaways first.
type ActivityReport struct {
	Root      string             `json:"root"`
	Threshold int                `json:"revision_threshold"`
	Documents []DocumentActivity `json:"documents"`
	// Total counts documents with in-window revisions before the limit
	// clipped; Truncated marks the clip explicitly.
	Total     int  `json:"total"`
	Truncated bool `json:"truncated,omitempty"`
}

// Activity reports revision churn across one root's documents since the
// window start. It reads the index first (a document whose file mtime
// predates the window is skipped before any git subprocess runs), pulls
// each active document's in-window history, and computes the window's
// net line delta with a single spanning diff per document — from the
// revision in force at window start to HEAD — rather than a diff per
// revision.
func (s *Store) Activity(ctx context.Context, q ActivityQuery) (*ActivityReport, error) {
	root := normalizeRootName(q.Root)
	if root == "" {
		return nil, fmt.Errorf("activity requires a root; known roots: %s", strings.Join(s.allRoots(), ", "))
	}
	if _, known := s.roots[root]; !known {
		return nil, fmt.Errorf("unknown root %q; known roots: %s", root, strings.Join(s.allRoots(), ", "))
	}
	reviser := s.rootReviser(root)
	if reviser == nil {
		return nil, fmt.Errorf("root %q keeps no revision history; revision-backed roots: %s",
			root, strings.Join(s.reviserRoots(), ", "))
	}
	if q.Since.IsZero() {
		return nil, fmt.Errorf("activity requires a window start (since)")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}
	threshold := q.RevisionThreshold
	if threshold <= 0 {
		threshold = defaultRevisionThreshold
	}

	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}

	// Candidate pre-filter on the index: file mtime inside the window
	// (with one second of slack — modified_at is RFC3339Nano text, whose
	// lexicographic order is only second-exact across precision changes).
	// This is what bounds the git subprocess count.
	cutoff := q.Since.Add(-time.Second).UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `
		SELECT rel_path, size_bytes, word_count
		FROM indexed_documents
		WHERE root = ? AND modified_at >= ?
		ORDER BY rel_path ASC
	`, root, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list active documents for root %q: %w", root, err)
	}
	defer rows.Close()

	type candidate struct {
		relPath   string
		sizeBytes int64
		wordCount int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.relPath, &c.sizeBytes, &c.wordCount); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	report := &ActivityReport{Root: root, Threshold: threshold}
	for _, c := range candidates {
		activity, err := s.documentActivity(ctx, reviser, root, c.relPath, q.Since, threshold)
		if err != nil {
			return nil, err
		}
		if activity == nil {
			continue // no in-window revisions (mtime was touch-only or index slack)
		}
		activity.SizeBytes = c.sizeBytes
		activity.WordCount = c.wordCount
		report.Documents = append(report.Documents, *activity)
	}

	// Flagged first, then busiest, then stable name order.
	sort.SliceStable(report.Documents, func(i, j int) bool {
		a, b := report.Documents[i], report.Documents[j]
		if a.Flagged != b.Flagged {
			return a.Flagged
		}
		if a.Revisions != b.Revisions {
			return a.Revisions > b.Revisions
		}
		return a.Ref < b.Ref
	})
	report.Total = len(report.Documents)
	if len(report.Documents) > limit {
		report.Documents = report.Documents[:limit]
		report.Truncated = true
	}
	return report, nil
}

// documentActivity computes one document's in-window churn, or nil when
// the window holds no revisions.
func (s *Store) documentActivity(ctx context.Context, reviser RootReviser, root, relPath string, since time.Time, threshold int) (*DocumentActivity, error) {
	listing, err := reviser.History(ctx, relPath, RevisionQuery{Limit: activityHistoryPage})
	if err != nil {
		return nil, fmt.Errorf("history for %s:%s: %w", root, relPath, err)
	}
	var inWindow []RevisionRef
	for _, rev := range listing.Revisions {
		if rev.Timestamp.Before(since) {
			break // newest-first: everything past here predates the window
		}
		inWindow = append(inWindow, rev)
	}
	if len(inWindow) == 0 {
		return nil, nil
	}

	activity := &DocumentActivity{
		Ref:            root + ":" + relPath,
		Revisions:      len(inWindow),
		LastRevisionAt: inWindow[0].Timestamp,
		Authors:        aggregateActivityAuthors(inWindow),
	}

	// One spanning diff: from the revision in force at window start to
	// HEAD. A document born inside the window has no such base; fall
	// back to its oldest in-window revision, which undercounts by the
	// birth commit's own additions — a known, bounded skew.
	base := ""
	if len(inWindow) < len(listing.Revisions) {
		base = listing.Revisions[len(inWindow)].Commit
	} else if len(inWindow) > 1 {
		base = inWindow[len(inWindow)-1].Commit
	}
	if base != "" {
		diff, err := reviser.Diff(ctx, relPath, base, "HEAD", "stat")
		if err != nil {
			return nil, fmt.Errorf("diff for %s:%s: %w", root, relPath, err)
		}
		activity.LinesAdded = diff.Added
		activity.LinesRemoved = diff.Removed
		activity.NetLineDelta = diff.Added - diff.Removed
	}

	if activity.Revisions >= threshold {
		activity.Flagged = true
		activity.FlagReason = fmt.Sprintf("%d revisions in the window meets the runaway threshold %d", activity.Revisions, threshold)
	}
	return activity, nil
}

// maxActivityAuthors caps the per-document attribution list.
const maxActivityAuthors = 5

func aggregateActivityAuthors(revs []RevisionRef) []ActivityAuthor {
	counts := make(map[string]*ActivityAuthor)
	var order []string
	for _, rev := range revs {
		author := rev.Trailers[TrailerLoopID]
		if author == "" {
			author = "manual"
		}
		entry, seen := counts[author]
		if !seen {
			entry = &ActivityAuthor{Author: author}
			counts[author] = entry
			order = append(order, author)
		}
		entry.Revisions++
		if entry.Model == "" {
			entry.Model = rev.Trailers[TrailerModel]
		}
	}
	authors := make([]ActivityAuthor, 0, len(order))
	for _, key := range order {
		authors = append(authors, *counts[key])
	}
	sort.SliceStable(authors, func(i, j int) bool {
		if authors[i].Revisions != authors[j].Revisions {
			return authors[i].Revisions > authors[j].Revisions
		}
		return authors[i].Author < authors[j].Author
	})
	if len(authors) > maxActivityAuthors {
		authors = authors[:maxActivityAuthors]
	}
	return authors
}

// reviserRoots lists the roots that keep revision history, for the
// teaching error when a caller names one that does not.
func (s *Store) reviserRoots() []string {
	var roots []string
	for root := range s.rootRevisers {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

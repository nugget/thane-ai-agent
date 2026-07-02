package documents

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

const (
	defaultHistoryLimit = 20
	maxHistoryLimit     = 100
	// diffPatchBudget bounds an inline unified diff. A larger patch degrades
	// to a diffstat rather than being clipped mid-hunk into unparseable text.
	diffPatchBudget = 14 * 1024
)

// HistoryArgs requests the revision log for one document.
type HistoryArgs struct {
	Ref    string `json:"ref"`
	Limit  int    `json:"limit,omitempty"`
	Before string `json:"before,omitempty"`
}

// DiffArgs requests the change to one document between two revisions.
type DiffArgs struct {
	Ref    string `json:"ref"`
	From   string `json:"from"`
	To     string `json:"to,omitempty"`
	Format string `json:"format,omitempty"`
}

// AtArgs recalls one document's content as of a revision.
type AtArgs struct {
	Ref string `json:"ref"`
	Rev string `json:"rev,omitempty"`
}

// resolveReviser maps a ref to its root's reviser and the root-relative path,
// distinguishing an unknown root from a known-but-not-git-backed one so the
// model can recover.
func (s *Store) resolveReviser(ref string) (RootReviser, string, error) {
	root, relPath, err := parseRef(ref)
	if err != nil {
		return nil, "", err
	}
	if _, ok := s.roots[normalizeRootName(root)]; !ok {
		return nil, "", fmt.Errorf("unknown root %q; call doc_roots to list roots", root)
	}
	reviser := s.rootReviser(root)
	if reviser == nil {
		return nil, "", fmt.Errorf("root %q has no revision history (it is not git-backed); doc_roots reports revisions=true for roots that do", root)
	}
	return reviser, relPath, nil
}

func (s *Store) revisionHistory(ctx context.Context, ref string, opts RevisionQuery) (RevisionListing, error) {
	reviser, relPath, err := s.resolveReviser(ref)
	if err != nil {
		return RevisionListing{}, err
	}
	return reviser.History(ctx, relPath, opts)
}

func (s *Store) revisionDiff(ctx context.Context, ref, from, to, format string) (RevisionDiff, error) {
	reviser, relPath, err := s.resolveReviser(ref)
	if err != nil {
		return RevisionDiff{}, err
	}
	return reviser.Diff(ctx, relPath, from, to, format)
}

func (s *Store) revisionContent(ctx context.Context, ref, selector string) (RevisionContent, error) {
	reviser, relPath, err := s.resolveReviser(ref)
	if err != nil {
		return RevisionContent{}, err
	}
	return reviser.Content(ctx, relPath, selector)
}

// History returns the revision log for one document, newest first.
func (t *Tools) History(ctx context.Context, args HistoryArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if strings.TrimSpace(args.Ref) == "" {
		return "", fmt.Errorf("ref is required")
	}
	listing, err := t.store.revisionHistory(ctx, args.Ref, RevisionQuery{
		Limit:       clampPositiveLimit(args.Limit, defaultHistoryLimit, maxHistoryLimit),
		Before:      strings.TrimSpace(args.Before),
		WithSigners: true,
	})
	if err != nil {
		return "", err
	}
	now := nowUTC()
	out := modelHistory{
		Ref:           args.Ref,
		RevisionCount: listing.Total,
		Revisions:     make([]modelRevision, 0, len(listing.Revisions)),
		NextBefore:    listing.NextBefore,
	}
	for _, rev := range listing.Revisions {
		out.Revisions = append(out.Revisions, toModelRevision(rev, now))
	}
	return marshalToolResult(out)
}

// Diff returns the change to one document between two revisions.
func (t *Tools) Diff(ctx context.Context, args DiffArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if strings.TrimSpace(args.Ref) == "" {
		return "", fmt.Errorf("ref is required")
	}
	if strings.TrimSpace(args.From) == "" {
		return "", fmt.Errorf("from is required; pass a rev from doc_history, a timestamp, or a delta like -7d")
	}
	from := t.normalizeSelector(args.From)
	to := t.normalizeSelector(defaultString(args.To, "HEAD"))
	format := "patch"
	if strings.TrimSpace(args.Format) == "stat" {
		format = "stat"
	}
	diff, err := t.store.revisionDiff(ctx, args.Ref, from, to, format)
	if err != nil {
		return "", err
	}
	// A unified patch over the byte budget degrades to a diffstat so a clipped,
	// unparseable patch never reaches the model. The fallback is mandatory: if
	// the diffstat itself fails, error rather than emit the over-budget patch
	// (which marshalToolResult would clip into a meaningless preview).
	note := ""
	if format == "patch" && len(diff.Body) > diffPatchBudget {
		statDiff, sErr := t.store.revisionDiff(ctx, args.Ref, from, to, "stat")
		if sErr != nil {
			return "", fmt.Errorf("diff for %s exceeded %d bytes and the diffstat fallback failed: %w", args.Ref, diffPatchBudget, sErr)
		}
		diff = statDiff
		note = fmt.Sprintf("patch exceeded %d bytes; showing diffstat — narrow the range for the full patch", diffPatchBudget)
	}
	now := nowUTC()
	return marshalToolResult(modelDiff{
		Ref:     args.Ref,
		Base:    toModelRevision(diff.Base, now),
		Target:  toModelRevision(diff.Target, now),
		Format:  diff.Format,
		Added:   diff.Added,
		Removed: diff.Removed,
		Patch:   diff.Body,
		Note:    note,
	})
}

// At recalls one document's content as of a revision.
func (t *Tools) At(ctx context.Context, args AtArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	if strings.TrimSpace(args.Ref) == "" {
		return "", fmt.Errorf("ref is required")
	}
	selector := t.normalizeSelector(defaultString(args.Rev, "HEAD"))
	content, err := t.store.revisionContent(ctx, args.Ref, selector)
	if err != nil {
		return "", err
	}
	_, relPath, _ := parseRef(args.Ref)
	meta, body := splitFrontmatter(content.Content)
	parsed := parseMarkdownDocumentParts(relPath, meta, body)
	now := nowUTC()
	rev := content.Revision
	out := modelRevisionAt{
		Ref:          args.Ref,
		Rev:          rev.Short,
		Index:        rev.Index,
		Timestamp:    rev.Timestamp.UTC().Format(time.RFC3339),
		Age:          promptfmt.FormatDeltaOnly(rev.Timestamp, now),
		VerifyStatus: revisionVerifyStatus(rev.Signer),
		Signer:       toModelRevisionSigner(rev.Signer),
		Title:        parsed.Title,
		Frontmatter:  modelFrontmatter(parsed.Frontmatter, now),
		Outline:      parsed.Sections,
	}
	// Honesty seam: an untrusted recall ships under "unverified_body", never
	// "body", so the model can't pattern-match it as a normal doc_read payload.
	if out.VerifyStatus == "trusted" {
		out.Body = body
	} else {
		out.UnverifiedBody = body
	}
	return marshalToolResult(out)
}

type modelRevision struct {
	Rev       string               `json:"rev"`
	Index     int                  `json:"index"`
	Timestamp string               `json:"timestamp"`
	Age       string               `json:"age,omitempty"`
	Message   string               `json:"message,omitempty"`
	Signer    *modelRevisionSigner `json:"signer,omitempty"`
}

type modelRevisionSigner struct {
	Verified  bool   `json:"verified"`
	Principal string `json:"principal,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type modelHistory struct {
	Ref           string          `json:"ref"`
	RevisionCount int             `json:"revision_count"`
	Revisions     []modelRevision `json:"revisions"`
	NextBefore    string          `json:"next_before,omitempty"`
}

type modelDiff struct {
	Ref     string        `json:"ref"`
	Base    modelRevision `json:"base_rev"`
	Target  modelRevision `json:"target_rev"`
	Format  string        `json:"format"`
	Added   int           `json:"added"`
	Removed int           `json:"removed"`
	Patch   string        `json:"patch,omitempty"`
	Note    string        `json:"note,omitempty"`
}

type modelRevisionAt struct {
	Ref            string               `json:"ref"`
	Rev            string               `json:"rev"`
	Index          int                  `json:"index"`
	Timestamp      string               `json:"timestamp"`
	Age            string               `json:"age,omitempty"`
	VerifyStatus   string               `json:"verify_status"`
	Signer         *modelRevisionSigner `json:"signer,omitempty"`
	Title          string               `json:"title,omitempty"`
	Frontmatter    map[string][]string  `json:"frontmatter,omitempty"`
	Outline        []Section            `json:"outline,omitempty"`
	Body           string               `json:"body,omitempty"`
	UnverifiedBody string               `json:"unverified_body,omitempty"`
}

func toModelRevision(r RevisionRef, now time.Time) modelRevision {
	return modelRevision{
		Rev:       r.Short,
		Index:     r.Index,
		Timestamp: r.Timestamp.UTC().Format(time.RFC3339),
		Age:       promptfmt.FormatDeltaOnly(r.Timestamp, now),
		Message:   r.Message,
		Signer:    toModelRevisionSigner(r.Signer),
	}
}

func toModelRevisionSigner(s *RevisionSigner) *modelRevisionSigner {
	if s == nil {
		return nil
	}
	return &modelRevisionSigner{
		Verified:  s.Verified,
		Principal: s.Principal,
		Kind:      s.Kind,
		Reason:    s.Reason,
	}
}

func revisionVerifyStatus(s *RevisionSigner) string {
	switch {
	case s == nil:
		return "unknown"
	case s.Verified:
		return "trusted"
	default:
		return "unverified"
	}
}

// normalizeSelector converts a delta ("-7d") or absolute timestamp into the
// RFC3339 form the reviser understands, leaving "HEAD"/"latest" and commit
// hashes untouched. Deltas are recognized by a leading +/-, timestamps by a
// 'T' or ':' — neither can occur in a hex commit hash — so a hash is never
// mistaken for a time.
func (t *Tools) normalizeSelector(sel string) string {
	s := strings.TrimSpace(sel)
	switch strings.ToLower(s) {
	case "", "head", "latest":
		return s
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") || strings.ContainsAny(s, "T:") {
		if parsed, err := promptfmt.ParseTimeOrDelta(s, nowUTC()); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return s
}

func defaultString(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

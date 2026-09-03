package documents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	revisionAbsent          = "absent"
	maxRevisionReceipts     = 2048
	revisionReceiptTTL      = 24 * time.Hour
	maxConflictDiffBytes    = 10 * 1024
	maxConflictExcerptBytes = 4 * 1024
)

type revisionReceipt struct {
	revision string
	readAt   time.Time
}

type mutationConflict struct {
	action           string
	ref              string
	message          string
	changedSinceRead string
	linesAdded       int
	linesRemoved     int
	diffTruncated    bool
	currentExcerpt   string
	currentTruncated bool
	receiptRevision  string
}

type modelMutationConflict struct {
	Action           string `json:"action"`
	Applied          bool   `json:"applied"`
	Ref              string `json:"ref"`
	Message          string `json:"message"`
	ChangedSinceRead string `json:"changed_since_read,omitempty"`
	LinesAdded       int    `json:"lines_added,omitempty"`
	LinesRemoved     int    `json:"lines_removed,omitempty"`
	DiffTruncated    bool   `json:"diff_truncated,omitempty"`
	CurrentExcerpt   string `json:"current_excerpt,omitempty"`
	CurrentTruncated bool   `json:"current_excerpt_truncated,omitempty"`
}

// MutationRejectedError is the error form of a refused mutation. The
// document is unchanged, and Result carries the same model-facing
// applied:false payload the doc_* tools return inline: the rejection
// message plus, for a stale receipt, the bounded intervening diff or
// current excerpt. The doc_* tools deliver that payload as a successful
// result because a conversational model reads it either way. A loop's
// generated output tool asks for this error instead
// ([WriteArgs.RejectionIsError]): there a result that reads as success
// while nothing was committed is indistinguishable from a healthy
// publish — to the loop's failure handling, to the event log's ok flag,
// and to a model that does not independently check doc_history — and a
// model that senses the write did not land retries the identical call.
type MutationRejectedError struct {
	Action  string
	Ref     string
	Message string
	// Result is the marshaled rejection payload, byte-identical to what
	// the inline contract returns as a successful result.
	Result string
}

// Error returns the rejection message followed by the payload, so a model
// reading the tool error gets both the next move and the reconciliation
// data in one place.
func (e *MutationRejectedError) Error() string {
	if strings.TrimSpace(e.Result) == "" {
		return e.Message
	}
	return e.Message + "\n" + e.Result
}

func (s *Store) automaticExpectedRevision(root string, existed bool, record *DocumentRecord, expected string) string {
	if expected = strings.TrimSpace(expected); expected != "" {
		return expected
	}
	if s.rootWriter(root) == nil || s.rootReviser(root) == nil {
		return ""
	}
	if !existed {
		return revisionAbsent
	}
	if record != nil && strings.TrimSpace(record.Revision) != "" {
		return record.Revision
	}
	// The path exists but has no committed file revision. Treating it as an
	// unconditional update would overwrite an operator's untracked file; the
	// conditional writer maps this creation precondition to a dirty conflict.
	return revisionAbsent
}

func (t *Tools) rememberDocumentReceipt(scope string, doc *DocumentRecord) {
	if t == nil || t.store == nil || doc == nil || t.store.rootWriter(doc.Root) == nil {
		return
	}
	t.rememberRevisionReceipt(scope, doc.Ref, doc.Revision)
}

// RememberRead records doc's current revision as scope's hidden read
// receipt, for a caller that showed the model the document by a path other
// than the read tools. The loop output context is that caller: it renders a
// loop's own documents into each wake's prompt, which is the read the loop
// actually performs before rewriting them — asking for a second one through
// doc_read costs every wake a refusal, a re-read, and a retry, for nothing
// the model has not already seen. A receipt asserts that this scope has seen
// this whole revision, so record one only when the whole document was shown;
// a truncated rendering must leave the read-first requirement standing.
// Non-revision roots keep no receipts, so the call is a no-op for them.
func (t *Tools) RememberRead(scope string, doc *DocumentRecord) {
	t.rememberDocumentReceipt(scope, doc)
}

// rememberAbsentReceipt records that the caller itself removed ref from
// a revision-backed root, so its next write there is a conditional create
// rather than a replacement of a revision that no longer exists. Without
// this the pre-deletion receipt stayed pinned: a loop that created a
// document, deleted it, and created it again at the same ref was refused
// with "deleted after this loop last read it" — for a deletion it had
// performed. Roots without a writer keep no receipts and record nothing.
func (t *Tools) rememberAbsentReceipt(scope, ref string) {
	if t == nil || t.store == nil {
		return
	}
	root, _, err := parseRef(ref)
	if err != nil || t.store.rootWriter(root) == nil {
		return
	}
	t.rememberRevisionReceipt(scope, ref, revisionAbsent)
}

func (t *Tools) rememberRevisionReceipt(scope, ref, revision string) {
	scope = strings.TrimSpace(scope)
	ref = strings.TrimSpace(ref)
	revision = strings.TrimSpace(revision)
	if t == nil || scope == "" || ref == "" || revision == "" {
		return
	}
	now := time.Now()
	t.receiptMu.Lock()
	defer t.receiptMu.Unlock()
	if t.receipts == nil {
		t.receipts = make(map[string]revisionReceipt)
	}
	for key, receipt := range t.receipts {
		if now.Sub(receipt.readAt) > revisionReceiptTTL {
			delete(t.receipts, key)
		}
	}
	if len(t.receipts) >= maxRevisionReceipts {
		oldestKey := ""
		var oldest time.Time
		for key, receipt := range t.receipts {
			if oldestKey == "" || receipt.readAt.Before(oldest) {
				oldestKey = key
				oldest = receipt.readAt
			}
		}
		delete(t.receipts, oldestKey)
	}
	t.receipts[revisionReceiptKey(scope, ref)] = revisionReceipt{revision: revision, readAt: now}
}

func (t *Tools) revisionReceipt(scope, ref string) (string, bool) {
	scope = strings.TrimSpace(scope)
	ref = strings.TrimSpace(ref)
	if t == nil || scope == "" || ref == "" {
		return "", false
	}
	now := time.Now()
	key := revisionReceiptKey(scope, ref)
	t.receiptMu.Lock()
	defer t.receiptMu.Unlock()
	receipt, ok := t.receipts[key]
	if !ok {
		return "", false
	}
	if now.Sub(receipt.readAt) > revisionReceiptTTL {
		delete(t.receipts, key)
		return "", false
	}
	return receipt.revision, true
}

func revisionReceiptKey(scope, ref string) string {
	return scope + "\x00" + ref
}

func (t *Tools) prepareWriteReceipt(args *WriteArgs) {
	if args == nil || strings.TrimSpace(args.ExpectedRevision) != "" {
		return
	}
	revision, ok := t.revisionReceipt(args.ReceiptScope, args.Ref)
	if ok {
		args.ExpectedRevision = revision
		return
	}
	args.RequirePriorRead = strings.TrimSpace(args.ReceiptScope) != "" && args.Body != nil
}

func (t *Tools) prepareEditReceipt(args *EditArgs) {
	if args == nil || strings.TrimSpace(args.ExpectedRevision) != "" {
		return
	}
	if revision, ok := t.revisionReceipt(args.ReceiptScope, args.Ref); ok {
		args.ExpectedRevision = revision
	}
}

func (t *Tools) prepareJournalReceipt(args *JournalUpdateArgs) {
	if args == nil || strings.TrimSpace(args.ExpectedRevision) != "" {
		return
	}
	if revision, ok := t.revisionReceipt(args.ReceiptScope, args.Ref); ok {
		args.ExpectedRevision = revision
	}
}

// marshalMutationResult turns a store mutation outcome into its
// model-facing shape. A success records the new revision as the caller's
// receipt. A refusal — no read on record for a whole-document
// replacement, or a receipt the document has moved past — comes back as a
// *MutationRejectedError carrying the inline payload; deliverMutation
// then decides, per caller, whether that surfaces as the error or as the
// payload. Every other error passes through untouched.
func (t *Tools) marshalMutationResult(ctx context.Context, action, ref, scope, expected string, result *MutationResult, err error) (string, error) {
	if err == nil {
		if result != nil {
			t.rememberRevisionReceipt(scope, ref, result.Revision)
		}
		return marshalToolResult(toModelMutationResult(result, nowUTC()))
	}

	var readRequired *PriorReadRequiredError
	if errors.As(err, &readRequired) {
		return rejectMutation(&modelMutationConflict{
			Action:  action,
			Applied: false,
			Ref:     ref,
			Message: priorReadRequiredMessage(ref),
		})
	}

	var revisionConflict *RootRevisionConflictError
	if !errors.As(err, &revisionConflict) {
		return "", err
	}
	if strings.TrimSpace(expected) == "" {
		expected = revisionConflict.Expected
	}
	conflict := t.store.describeMutationConflict(ctx, action, ref, expected, revisionConflict)
	t.rememberRevisionReceipt(scope, ref, conflict.receiptRevision)
	return rejectMutation(&modelMutationConflict{
		Action:           conflict.action,
		Applied:          false,
		Ref:              conflict.ref,
		Message:          conflict.message,
		ChangedSinceRead: conflict.changedSinceRead,
		LinesAdded:       conflict.linesAdded,
		LinesRemoved:     conflict.linesRemoved,
		DiffTruncated:    conflict.diffTruncated,
		CurrentExcerpt:   conflict.currentExcerpt,
		CurrentTruncated: conflict.currentTruncated,
	})
}

// priorReadRequiredMessage names the precondition that failed and the one
// call that satisfies it. Its predecessor advised retrying "without any
// revision parameter" — a parameter no model-facing tool has carried
// since revision preconditions went hidden — so a caller that had done
// exactly what the message asked (read, then retry body-only) was told to
// do it again, verbatim, forever.
func priorReadRequiredMessage(ref string) string {
	return fmt.Sprintf("No change was made: Thane has no record of this loop reading %s, and a whole-document replacement is only applied against a version its caller has read — that is what protects intervening edits. Read %s with doc_read, then retry the same call.", ref, ref)
}

// rejectMutation marshals a refusal into the typed error that carries it.
func rejectMutation(conflict *modelMutationConflict) (string, error) {
	payload, err := marshalToolResult(conflict)
	if err != nil {
		return "", err
	}
	return "", &MutationRejectedError{Action: conflict.Action, Ref: conflict.Ref, Message: conflict.Message, Result: payload}
}

// deliverMutation shapes a mutation outcome for its caller. A refusal
// stays a *MutationRejectedError for callers that asked for one and
// becomes the inline applied:false payload, with no error, for everyone
// else. Any other outcome passes through unchanged.
func deliverMutation(payload string, err error, rejectionIsError bool) (string, error) {
	var rejected *MutationRejectedError
	if err != nil && !rejectionIsError && errors.As(err, &rejected) {
		return rejected.Result, nil
	}
	return payload, err
}

func (s *Store) describeMutationConflict(ctx context.Context, action, ref, expected string, conflict *RootRevisionConflictError) mutationConflict {
	result := mutationConflict{
		action: action,
		ref:    ref,
		message: "No change was made because this document changed after this loop last read it. " +
			"Review the intervening change, reconcile it with the intended update, and retry; Thane will compare the retry with the current document automatically.",
	}
	root, relPath, err := parseRef(ref)
	if err != nil {
		return result
	}
	reviser := s.rootReviser(root)
	if reviser == nil {
		return result
	}
	snapshot, snapshotErr := reviser.Snapshot(ctx, relPath)
	if snapshotErr != nil {
		actual := ""
		if conflict != nil {
			actual = strings.TrimSpace(conflict.Actual)
		}
		switch actual {
		case revisionAbsent:
			result.message = "No change was made because this document was deleted after this loop last read it. Reconcile that deletion with the intended update, then retry."
			result.receiptRevision = revisionAbsent
		case "", "worktree_dirty":
			result.message = "No change was made because the document changed, but Thane could not load its current content. Read the document again before retrying so the intended update can be reconciled safely."
		default:
			result.message = "No change was made because the document changed. Thane could not load its current content to show the intervening diff, so read the document again before retrying; the comparison base has advanced to the observed revision."
			result.receiptRevision = actual
		}
		return result
	}
	result.receiptRevision = snapshot.Revision.Short
	if result.receiptRevision == "" && conflict != nil && conflict.Actual == revisionAbsent {
		result.receiptRevision = revisionAbsent
	}

	actual := ""
	if conflict != nil {
		actual = strings.TrimSpace(conflict.Actual)
	}
	if actual == "worktree_dirty" {
		result.message = "No change was made because the document has uncommitted operator edits. The current worktree excerpt is included; preserve or commit those edits before retrying."
	}

	expected = strings.TrimSpace(expected)
	if expected != "" && expected != revisionAbsent && actual != revisionAbsent && actual != "worktree_dirty" && snapshot.Revision.Commit != "" {
		if diff, diffErr := reviser.Diff(ctx, relPath, expected, snapshot.Revision.Commit, "patch"); diffErr == nil {
			result.linesAdded = diff.Added
			result.linesRemoved = diff.Removed
			result.changedSinceRead, result.diffTruncated = truncateRevisionConflictText(diff.Body, maxConflictDiffBytes)
		}
	}
	if result.changedSinceRead == "" || result.diffTruncated || actual == "worktree_dirty" {
		result.currentExcerpt, result.currentTruncated = truncateRevisionConflictText(snapshot.Content, maxConflictExcerptBytes)
	}
	return result
}

func truncateRevisionConflictText(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return strings.TrimSpace(value[:cut]) + "\n…", true
}

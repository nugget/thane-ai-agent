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

func (t *Tools) marshalMutationResult(ctx context.Context, action, ref, scope, expected string, result *MutationResult, err error) (string, error) {
	if err == nil {
		if result != nil {
			t.rememberRevisionReceipt(scope, ref, result.Revision)
		}
		return marshalToolResult(toModelMutationResult(result, nowUTC()))
	}

	var readRequired *PriorReadRequiredError
	if errors.As(err, &readRequired) {
		return marshalToolResult(&modelMutationConflict{
			Action:  action,
			Applied: false,
			Ref:     ref,
			Message: fmt.Sprintf("No change was made. Read %s first so Thane can protect the whole-document replacement against intervening edits, then retry without any revision parameter.", ref),
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
	return marshalToolResult(&modelMutationConflict{
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

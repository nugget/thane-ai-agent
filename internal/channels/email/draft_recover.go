package email

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Reconcile settles two kinds of entry the plain identity proof cannot:
// a revision a crash interrupted, and a draft whose server never
// reported its UID. Both follow the rule the live revision follows:
// Thane's version counts only when the operator has not acted on the
// draft, and anything Thane removes is a UID it has proven its own.

// recoverPending settles a revision a crash interrupted after its
// write-ahead record was written. The record's phase says how far the
// revision got: without a new UID, the new version's APPEND had not
// answered, so Thane had not touched the old version; with one, Thane
// was about to flag the old version \Deleted, or had. The new version
// is adopted only when the operator did not act on the draft in the
// meantime; otherwise it is removed again and the entry closes.
func (s *Service) recoverPending(ctx context.Context, acct ResolvedAccount, state draftFolderState, owned map[uint32]bool, e *draftEntry) error {
	removing := e.Pending.NewUID != 0
	oldVerdict := proveOwnership(*e, state)
	copyOf, newVerdict, err := s.pendingCopy(ctx, acct, state, *e)
	if err != nil {
		return fmt.Errorf("settle the interrupted revision of draft %s: %w", e.ID, err)
	}
	newThere := newVerdict == ownershipProven || newVerdict == ownershipDeleted

	if !newThere && !removing {
		// The APPEND never landed, so only the old version matters.
		if oldVerdict == ownershipProven {
			e.Pending = nil
			if err := s.saveDraft(e); err != nil {
				return err
			}
			s.logRecovery(ctx, *e, "abandoned", e.UID)
			return nil
		}
		return s.closeDraft(ctx, e, DraftStageGone, s.closedReasonFor(ctx, acct, state.Folder, *e, oldVerdict, owned))
	}
	if reason := s.operatorActedDuringRevision(ctx, acct, state, owned, *e, copyOf, oldVerdict, newVerdict); reason != "" {
		return s.backOutRevision(ctx, acct, e, copyOf, newThere, removing, reason)
	}
	return s.completeRevision(ctx, acct, e, copyOf, oldVerdict)
}

// pendingCopy finds the new version a write-ahead record names and
// proves it: by the recorded UID once the record has one, else by its
// Message-ID and the SHA-256 of its exact bytes.
func (s *Service) pendingCopy(ctx context.Context, acct ResolvedAccount, state draftFolderState, e draftEntry) (draftEntry, ownershipVerdict, error) {
	p := e.Pending
	copyOf := draftEntry{ID: e.ID, Account: e.Account, Folder: state.Folder, UIDValidity: p.NewUIDValidity, UID: p.NewUID, MessageID: p.MessageID, InReplyTo: e.InReplyTo}
	if p.NewUID != 0 {
		return copyOf, proveOwnership(copyOf, state), nil
	}
	uid, validity, err := acct.Client.findProvenCopy(ctx, state.Folder, p.MessageID, p.SHA256)
	if err != nil || uid == 0 {
		return copyOf, ownershipVanished, err
	}
	copyOf.UID, copyOf.UIDValidity = uid, validity
	found, err := acct.Client.draftFolderState(ctx, state.Folder, []uint32{uid})
	if err != nil {
		return copyOf, "", err
	}
	return copyOf, proveOwnership(copyOf, found), nil
}

// operatorActedDuringRevision returns the reason the entry closes when
// the operator acted on the draft while the revision was interrupted,
// or "" when they did not: Thane's new version was sent, discarded, or
// edited; or the old version was removed or flagged before Thane began
// removing it; or, once Thane had begun, a draft answering the same
// message appeared under a UID Thane does not hold.
func (s *Service) operatorActedDuringRevision(ctx context.Context, acct ResolvedAccount, state draftFolderState, owned map[uint32]bool, e, copyOf draftEntry, oldVerdict, newVerdict ownershipVerdict) string {
	if newVerdict != ownershipProven {
		return s.closedReasonFor(ctx, acct, state.Folder, copyOf, newVerdict, withUID(owned, e.UID))
	}
	if oldVerdict == ownershipProven {
		return ""
	}
	if e.Pending.NewUID == 0 {
		return closedTouchedDuringRevision
	}
	if s.vanishedReason(ctx, acct, state.Folder, e, withUID(owned, copyOf.UID), closedVanished) == closedOperatorTookOver {
		return closedTouchedDuringRevision
	}
	return ""
}

// backOutRevision removes Thane's new version, finishes removing the old
// one when the record says Thane had begun to, and closes the entry.
func (s *Service) backOutRevision(ctx context.Context, acct ResolvedAccount, e *draftEntry, copyOf draftEntry, newThere, removing bool, reason string) error {
	if newThere {
		if _, _, err := acct.Client.removeProvenDraft(ctx, copyOf, true); err != nil {
			return fmt.Errorf("settle the interrupted revision of draft %s: remove Thane's new version: %w", e.ID, err)
		}
	}
	if removing {
		if _, _, err := acct.Client.removeProvenDraft(ctx, *e, true); err != nil {
			return fmt.Errorf("settle the interrupted revision of draft %s: remove the old version: %w", e.ID, err)
		}
	}
	s.logRecovery(ctx, *e, "backed_out", e.UID)
	return s.closeDraft(ctx, e, DraftStageGone, reason)
}

// completeRevision finishes an interrupted revision the operator did not
// touch: it removes the old version if it is still there, recording the
// new UID first so a crash from here is settled as a removal in
// progress, and adopts the new version. A removal that does not hold
// means the operator acted just now, and the revision is backed out.
func (s *Service) completeRevision(ctx context.Context, acct ResolvedAccount, e *draftEntry, copyOf draftEntry, oldVerdict ownershipVerdict) error {
	p := e.Pending
	switch oldVerdict {
	case ownershipProven:
		if p.NewUID == 0 {
			p.NewUID, p.NewUIDValidity = copyOf.UID, copyOf.UIDValidity
			if err := s.saveDraft(e); err != nil {
				return err
			}
		}
		removed, _, err := acct.Client.removeProvenDraft(ctx, *e, false)
		if err != nil {
			return fmt.Errorf("settle the interrupted revision of draft %s: %w", e.ID, err)
		}
		if !removed {
			return s.backOutRevision(ctx, acct, e, copyOf, true, false, closedTouchedDuringRevision)
		}
	case ownershipDeleted:
		// The record says Thane was removing it, and no edit shows, so
		// the flag is Thane's own from before the crash.
		if _, _, err := acct.Client.removeProvenDraft(ctx, *e, true); err != nil {
			return fmt.Errorf("settle the interrupted revision of draft %s: %w", e.ID, err)
		}
	}
	replaced := e.UID
	e.PreviousBody, e.Body = e.Body, p.Body
	e.UID, e.UIDValidity, e.MessageID, e.SHA256 = copyOf.UID, copyOf.UIDValidity, p.MessageID, p.SHA256
	e.Revisions++
	e.addRevision(p.Revision)
	e.Pending = nil
	if err := s.saveDraft(e); err != nil {
		return err
	}
	s.logRecovery(ctx, *e, "completed", replaced)
	return nil
}

// settleUnknownUID settles an open entry whose server never reported
// its UID. The draft is looked for by its Message-ID and proven by the
// SHA-256 of the bytes Thane appended: found, the entry learns its UID
// and can be acted on; gone, with nothing carrying its Message-ID, the
// entry closes, so it cannot hold a message's one draft forever. A
// message that carries its Message-ID but cannot be proven keeps the
// entry open and unactionable.
func (s *Service) settleUnknownUID(ctx context.Context, acct ResolvedAccount, folder string, owned map[uint32]bool, e *draftEntry) error {
	if !namesFolder(e.Folder, folder) {
		return s.closeDraft(ctx, e, DraftStageGone, closedFolderChanged)
	}
	if e.SHA256 != "" {
		uid, validity, err := acct.Client.findProvenCopy(ctx, folder, e.MessageID, e.SHA256)
		if err != nil {
			return fmt.Errorf("look for draft %s in %q of account %q: %w", e.ID, folder, acct.Name, err)
		}
		if uid != 0 {
			learned := *e
			learned.UID, learned.UIDValidity = uid, validity
			state, err := acct.Client.draftFolderState(ctx, folder, []uint32{uid})
			if err != nil {
				return fmt.Errorf("look for draft %s in %q of account %q: %w", e.ID, folder, acct.Name, err)
			}
			switch verdict := proveOwnership(learned, state); verdict {
			case ownershipProven:
				*e = learned
				if err := s.saveDraft(e); err != nil {
					return err
				}
				s.logger.Info("email draft uid learned", "draft_id", e.ID, "account", e.Account, "folder", e.Folder, "uid", e.UID, "uid_validity", e.UIDValidity,
					"loop_id", tools.LoopIDFromContext(ctx), "conversation_id", tools.ConversationIDFromContext(ctx))
				return nil
			case ownershipDeleted:
				return s.closeDraft(ctx, e, DraftStageGone, s.closedReasonFor(ctx, acct, folder, learned, verdict, owned))
			}
		}
	}
	if e.MessageID != "" {
		found, err := acct.Client.draftsMatching(ctx, folder, []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: bracketed(e.MessageID)}})
		if err != nil {
			return fmt.Errorf("look for draft %s in %q of account %q: %w", e.ID, folder, acct.Name, err)
		}
		if len(found.Messages) > 0 {
			return nil
		}
	}
	return s.closeDraft(ctx, e, DraftStageGone, s.vanishedReason(ctx, acct, folder, *e, owned, closedVanished))
}

// logRecovery writes the one line a settled write-ahead record gets.
func (s *Service) logRecovery(ctx context.Context, e draftEntry, outcome string, replacedUID uint32) {
	s.logger.Info("email draft revision recovered",
		"draft_id", e.ID,
		"account", e.Account,
		"outcome", outcome,
		"folder", e.Folder,
		"replaced_uid", replacedUID,
		"uid", e.UID,
		"message_id", e.MessageID,
		"revision", e.Revisions,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
}

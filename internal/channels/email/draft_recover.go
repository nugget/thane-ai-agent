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

// Write-ahead phases (draftPending.Phase). Each is written at the point
// its comment names; the revision is done when the ledger holds the new
// version and the record is cleared.
const (
	// pendingAppending: the new version's APPEND is about to be sent.
	pendingAppending = "appending"

	// pendingAppended: the APPEND answered with NewUID. Thane may have
	// sent its STORE \Deleted on the old version since, but has not seen
	// the flag hold there, so nothing on the old version is provably
	// Thane's doing.
	pendingAppended = "appended"

	// pendingRetiring: Thane's STORE \Deleted on the old version read
	// back holding, and the old version is about to be expunged, or
	// already has been.
	pendingRetiring = "retiring"
)

// phase is how far the revision provably got. Retiring needs an explicit
// retiring record with a new UID; any other record with a new UID reads
// as appended, the phase that authorizes the least.
func (p draftPending) phase() string {
	switch {
	case p.NewUID == 0:
		return pendingAppending
	case p.Phase == pendingRetiring:
		return pendingRetiring
	}
	return pendingAppended
}

// recoverPending settles a revision a crash interrupted after its
// write-ahead record was written, by the phase the record reached.
// Before retiring, anything that happened to the old version was not
// provably Thane's doing, even a \Deleted flag its own interrupted store
// may have set; in retiring, which is recorded only after Thane saw its
// flag hold there, a \Deleted flag or an absence there is Thane's own.
// Recovery never flags the old version, and expunges it
// only in the retiring phase, and only while it is still proven by its
// UID and Message-ID and carries the flag the record attests, with no
// edit by the operator in sight. Otherwise a revision the operator did
// not touch is rolled back, removing Thane's new version and keeping the
// old one as the recorded draft; one the operator touched closes held,
// and nothing the operator marked is expunged.
func (s *Service) recoverPending(ctx context.Context, acct ResolvedAccount, state draftFolderState, owned map[uint32]bool, e *draftEntry) error {
	phase := e.Pending.phase()
	oldVerdict := proveOwnership(*e, state)
	copyOf, newVerdict, err := s.pendingCopy(ctx, acct, state, *e)
	if err != nil {
		return fmt.Errorf("settle the interrupted revision of draft %s: %w", e.ID, err)
	}

	if phase == pendingAppending && newVerdict != ownershipProven && newVerdict != ownershipDeleted {
		// The APPEND never landed, so only the old version matters.
		if oldVerdict == ownershipProven {
			e.Pending = nil
			if err := s.saveDraft(e); err != nil {
				return err
			}
			s.logRecovery(ctx, *e, "abandoned", phase, 0)
			return nil
		}
		return s.closeDraft(ctx, e, DraftStageGone, s.closedReasonFor(ctx, acct, state.Folder, *e, oldVerdict, owned))
	}
	if reason := s.operatorActedDuringRevision(ctx, acct, state, owned, *e, copyOf, phase, oldVerdict, newVerdict); reason != "" {
		return s.backOutRevision(ctx, acct, e, copyOf, phase, newVerdict, reason)
	}
	if phase == pendingRetiring && oldVerdict != ownershipProven {
		return s.completeRevision(ctx, acct, e, copyOf, oldVerdict)
	}
	return s.rollBackRevision(ctx, acct, e, copyOf, phase)
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
// edited; or, before retiring, the old version was removed or flagged at
// all; or, in retiring, a draft answering the same message appeared
// under a UID Thane does not hold.
func (s *Service) operatorActedDuringRevision(ctx context.Context, acct ResolvedAccount, state draftFolderState, owned map[uint32]bool, e, copyOf draftEntry, phase string, oldVerdict, newVerdict ownershipVerdict) string {
	if newVerdict != ownershipProven {
		return s.closedReasonFor(ctx, acct, state.Folder, copyOf, newVerdict, withUID(owned, e.UID))
	}
	if oldVerdict == ownershipProven {
		return ""
	}
	if phase != pendingRetiring {
		return closedTouchedDuringRevision
	}
	if s.vanishedReason(ctx, acct, state.Folder, e, withUID(owned, copyOf.UID), closedVanished) == closedOperatorTookOver {
		return closedTouchedDuringRevision
	}
	return ""
}

// backOutRevision removes Thane's new version when it can be proven by
// its bytes and is unflagged, and closes the entry. It never touches the
// old version: before retiring, whatever happened there is the
// operator's; in retiring, a flag there may be their client's too, once
// they have acted. A new version that cannot be removed is left in
// place, logged, and the entry closes as operator_took_over, since the
// folder then holds a second draft answering the same message.
func (s *Service) backOutRevision(ctx context.Context, acct ResolvedAccount, e *draftEntry, copyOf draftEntry, phase string, newVerdict ownershipVerdict, reason string) error {
	var removedUID uint32
	if newVerdict == ownershipProven {
		removed, err := acct.Client.removeProvenCopy(ctx, copyOf, e.Pending.SHA256)
		if err != nil {
			return fmt.Errorf("settle the interrupted revision of draft %s: remove Thane's new version: %w", e.ID, err)
		}
		if removed {
			removedUID = copyOf.UID
		} else {
			s.logNewVersionLeft(ctx, *e, copyOf.UID, phase)
			reason = closedOperatorTookOver
		}
	}
	s.logRecovery(ctx, *e, "backed_out", phase, removedUID)
	return s.closeDraft(ctx, e, DraftStageGone, reason)
}

// rollBackRevision undoes a revision the operator did not touch and that
// had not provably begun retiring the old version: Thane's new version is
// removed, proven by its UID, its Message-ID, and its bytes, and the old
// version stays the recorded draft, still open. A new version that
// cannot be removed means it changed since the folder was read, so the
// entry closes as operator_took_over and the new version is left.
func (s *Service) rollBackRevision(ctx context.Context, acct ResolvedAccount, e *draftEntry, copyOf draftEntry, phase string) error {
	removed, err := acct.Client.removeProvenCopy(ctx, copyOf, e.Pending.SHA256)
	if err != nil {
		return fmt.Errorf("settle the interrupted revision of draft %s: remove Thane's new version: %w", e.ID, err)
	}
	if !removed {
		s.logNewVersionLeft(ctx, *e, copyOf.UID, phase)
		s.logRecovery(ctx, *e, "backed_out", phase, 0)
		return s.closeDraft(ctx, e, DraftStageGone, closedOperatorTookOver)
	}
	e.Pending = nil
	if err := s.saveDraft(e); err != nil {
		return err
	}
	s.logRecovery(ctx, *e, "rolled_back", phase, copyOf.UID)
	return nil
}

// completeRevision finishes a revision interrupted while it was retiring
// the old version, when the operator did not touch the draft: the old
// version's \Deleted flag is the one the record attests, so that UID is
// expunged, and the new version is adopted. An old version found without
// the flag by then means someone cleared it, and the revision is backed
// out instead.
func (s *Service) completeRevision(ctx context.Context, acct ResolvedAccount, e *draftEntry, copyOf draftEntry, oldVerdict ownershipVerdict) error {
	p := e.Pending
	var removedUID uint32
	if oldVerdict == ownershipDeleted {
		gone, err := acct.Client.expungeRetiredDraft(ctx, *e)
		if err != nil {
			return fmt.Errorf("settle the interrupted revision of draft %s: %w", e.ID, err)
		}
		if !gone {
			return s.backOutRevision(ctx, acct, e, copyOf, pendingRetiring, ownershipProven, closedTouchedDuringRevision)
		}
		removedUID = e.UID
	}
	e.PreviousBody, e.Body = e.Body, p.Body
	e.UID, e.UIDValidity, e.MessageID, e.SHA256 = copyOf.UID, copyOf.UIDValidity, p.MessageID, p.SHA256
	e.Revisions++
	e.addRevision(p.Revision)
	e.Pending = nil
	if err := s.saveDraft(e); err != nil {
		return err
	}
	s.logRecovery(ctx, *e, "completed", pendingRetiring, removedUID)
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

// logRecovery writes the one line a settled write-ahead record gets:
// how it was settled, from which phase, and the UID Thane removed doing
// it, or 0 when it removed nothing.
func (s *Service) logRecovery(ctx context.Context, e draftEntry, outcome, phase string, removedUID uint32) {
	s.logger.Info("email draft revision recovered",
		"draft_id", e.ID,
		"account", e.Account,
		"outcome", outcome,
		"phase", phase,
		"folder", e.Folder,
		"removed_uid", removedUID,
		"uid", e.UID,
		"message_id", e.MessageID,
		"revision", e.Revisions,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
}

// logNewVersionLeft records a new version recovery could not prove by
// its bytes or remove, and so left in the drafts folder.
func (s *Service) logNewVersionLeft(ctx context.Context, e draftEntry, newUID uint32, phase string) {
	s.logger.Warn("email draft revision's new version left in place",
		"draft_id", e.ID,
		"account", e.Account,
		"phase", phase,
		"folder", e.Folder,
		"new_uid", newUID,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
}

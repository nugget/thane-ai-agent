package email

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/emersion/go-imap/v2"
)

// Reconcile brings an account's ledger up to date with its drafts
// folder. It runs lazily, whenever a draft tool or a drafted message
// needs the ledger, never on a timer: it EXAMINEs the drafts folder,
// resolved by role, and UID FETCHes only the UIDs the ledger recorded,
// so no capped listing can hide a draft from it. An entry whose proof
// fails closes as gone; a write-ahead record a crash left behind, and an
// entry whose server never reported its UID, are settled here
// (draft_recover.go).

// ownershipVerdict is the identity proof's answer.
type ownershipVerdict string

// Verdicts. Every one but ownershipProven means the draft is not, or
// cannot be shown to be, the one Thane wrote.
const (
	ownershipProven             ownershipVerdict = "proven"
	ownershipUIDUnknown         ownershipVerdict = "uid_unknown"
	ownershipFolderChanged      ownershipVerdict = closedFolderChanged
	ownershipUIDValidityChanged ownershipVerdict = closedUIDValidityChanged
	ownershipVanished           ownershipVerdict = closedVanished
	ownershipMessageIDChanged   ownershipVerdict = closedMessageIDChanged
	ownershipDeleted            ownershipVerdict = closedMarkedDeleted
)

// proveOwnership is the one identity proof every draft action runs: the
// folder the state came from is the entry's folder, its UIDVALIDITY is
// the recorded one, and it holds the recorded UID with the recorded
// Message-ID, not marked \Deleted. IMAP never changes a stored message
// under its UID, so a draft that passes holds exactly the bytes Thane
// wrote; a \Deleted flag Thane did not set is the operator's client
// discarding it, or saving an edit, before it expunges, so a flagged
// draft is never proven.
func proveOwnership(e draftEntry, state draftFolderState) ownershipVerdict {
	switch {
	case !e.uidKnown():
		return ownershipUIDUnknown
	case !namesFolder(state.Folder, e.Folder):
		return ownershipFolderChanged
	case state.UIDValidity != e.UIDValidity:
		return ownershipUIDValidityChanged
	}
	msg, ok := state.Messages[e.UID]
	if !ok {
		return ownershipVanished
	}
	if !sameMessageID(msg.MessageID, e.MessageID) {
		return ownershipMessageIDChanged
	}
	if flagIn(msg.Flags, imap.FlagDeleted) {
		return ownershipDeleted
	}
	return ownershipProven
}

// reconcileDrafts proves every open entry of an account against its
// drafts folder, closes the ones that fail, settles write-ahead records
// and unknown UIDs, and returns every entry of the account as it stands
// afterwards. Caller must hold the account's draft lock.
func (s *Service) reconcileDrafts(ctx context.Context, acct ResolvedAccount) ([]draftEntry, error) {
	entries, err := s.draftEntries(acct.Name)
	if err != nil {
		return nil, err
	}
	var open []int
	for i, e := range entries {
		if e.Stage == DraftStageOpen {
			open = append(open, i)
		}
	}
	if len(open) == 0 {
		return entries, nil
	}

	folder, err := s.sendDraftsFolder(ctx, acct)
	if err != nil {
		return nil, err
	}
	if folder == "" {
		for _, i := range open {
			if err := s.closeDraft(ctx, &entries[i], DraftStageGone, closedNoDraftsFolder); err != nil {
				return nil, err
			}
		}
		return entries, nil
	}

	uids := make([]uint32, 0, len(open))
	for _, i := range open {
		e := entries[i]
		if namesFolder(e.Folder, folder) {
			uids = append(uids, e.UID)
			if e.Pending != nil {
				uids = append(uids, e.Pending.NewUID)
			}
		}
	}
	state, err := acct.Client.draftFolderState(ctx, folder, uids)
	if err != nil {
		return nil, fmt.Errorf("check the drafts folder %q of account %q against the draft ledger: %w", folder, acct.Name, err)
	}
	owned := ownedUIDs(entries, state)
	for _, i := range open {
		e := &entries[i]
		switch {
		case e.Pending != nil:
			err = s.recoverPending(ctx, acct, state, owned, e)
		case !e.uidKnown():
			err = s.settleUnknownUID(ctx, acct, folder, owned, e)
		default:
			verdict := proveOwnership(*e, state)
			if verdict == ownershipProven {
				continue
			}
			err = s.closeDraft(ctx, e, DraftStageGone, s.closedReasonFor(ctx, acct, folder, *e, verdict, owned))
		}
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// ownedUIDs returns the UIDs Thane holds in state's folder under its
// UIDVALIDITY: every open entry's, and the new version a write-ahead
// record names.
func ownedUIDs(entries []draftEntry, state draftFolderState) map[uint32]bool {
	owned := make(map[uint32]bool)
	for _, e := range entries {
		if e.Stage != DraftStageOpen || !namesFolder(e.Folder, state.Folder) {
			continue
		}
		if e.UID != 0 && e.UIDValidity == state.UIDValidity {
			owned[e.UID] = true
		}
		if p := e.Pending; p != nil && p.NewUID != 0 && p.NewUIDValidity == state.UIDValidity {
			owned[p.NewUID] = true
		}
	}
	return owned
}

// withUID returns a copy of owned that also holds uid.
func withUID(owned map[uint32]bool, uid uint32) map[uint32]bool {
	out := maps.Clone(owned)
	if out == nil {
		out = map[uint32]bool{}
	}
	if uid != 0 {
		out[uid] = true
	}
	return out
}

// closedReasonFor names why an entry whose proof failed with verdict
// closes. A draft that vanished or is marked \Deleted closes as
// operator_took_over when the folder shows the operator edited it.
func (s *Service) closedReasonFor(ctx context.Context, acct ResolvedAccount, folder string, e draftEntry, verdict ownershipVerdict, owned map[uint32]bool) string {
	switch verdict {
	case ownershipVanished, ownershipDeleted:
		return s.vanishedReason(ctx, acct, folder, e, owned, string(verdict))
	}
	return string(verdict)
}

// vanishedReason tells a draft the operator edited from one they sent or
// discarded. A client stores an edit as a new message that still carries
// the draft's Message-ID or still answers the same original, so such a
// message under a UID that is neither the entry's nor one in exclude
// means the operator has the draft, and the reason is
// operator_took_over; otherwise it is fallback. A search that fails
// reads as fallback, the reason that claims less.
func (s *Service) vanishedReason(ctx context.Context, acct ResolvedAccount, folder string, e draftEntry, exclude map[uint32]bool, fallback string) string {
	fields := []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: bracketed(e.MessageID)}}
	if e.InReplyTo != "" {
		fields = append(fields, imap.SearchCriteriaHeaderField{Key: "In-Reply-To", Value: bracketed(e.InReplyTo)})
	}
	found, err := acct.Client.draftsMatching(ctx, folder, fields)
	if err != nil {
		s.logger.Debug("email draft replacement search failed", "draft_id", e.ID, "account", acct.Name, "error", err)
		return fallback
	}
	for _, uid := range slices.Sorted(maps.Keys(found.Messages)) {
		if uid != e.UID && !exclude[uid] {
			return closedOperatorTookOver
		}
	}
	return fallback
}

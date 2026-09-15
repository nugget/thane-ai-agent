package email

import (
	"context"
	"fmt"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/mail"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// A revision replaces one of Thane's drafts with a new version. IMAP
// cannot change a stored message, so the new version is appended and the
// old one removed, all under one hold of the client lock, in this order:
//
//  1. Require UIDPLUS, which IMAP4rev2 includes: without it the server
//     cannot say which UID the new version got, or expunge one UID alone.
//  2. Prove the draft is still Thane's (proveOwnership).
//  3. Write the write-ahead record in phase appending, naming the new
//     version's fresh Message-ID, generated in advance and never the
//     old one.
//  4. APPEND the new version with \Draft and \Seen, and record its UID
//     in phase appended.
//  5. Check the old version is still there and not marked \Deleted,
//     then UID STORE +FLAGS.SILENT (\Deleted) on it, with
//     UNCHANGEDSINCE when the server has CONDSTORE.
//  6. Read \Deleted back. If the check or the flag did not hold, the
//     operator touched the draft: remove Thane's new version instead
//     and close the entry. If it held, record phase retiring.
//  7. UID EXPUNGE the old UID alone, then read back again, because UID
//     EXPUNGE of a UID that is already gone answers OK.
//  8. Update the ledger, clearing the record, and log one line keyed to
//     the turn.
//
// Steps 5 to 7 are removeOwnLocked, split by retireOldLocked so the
// retiring record is written between the flag's read-back and the
// expunge. A crash after step 3 leaves the write-ahead record, which the
// next reconcile settles by its phase (recoverPending): only a retiring
// record lets recovery finish removing the old version, and a retiring
// record exists only once Thane saw its own flag hold there. A store that
// failed, timed out, or did not hold leaves the record at appended.

// replaceVerdict is how the IMAP part of a revision ended.
type replaceVerdict string

const (
	replaceDone      replaceVerdict = "replaced"
	replaceNoUIDPlus replaceVerdict = "no_uidplus"
	replaceNotProven replaceVerdict = "not_proven"
	replaceTouched   replaceVerdict = "touched"
)

// replaceOutcome is the IMAP part's answer. Ownership is set when the
// proof failed; UID and UIDValidity are the new version's.
type replaceOutcome struct {
	Verdict     replaceVerdict
	Ownership   ownershipVerdict
	UID         uint32
	UIDValidity uint32
}

// revisionRecord writes the write-ahead record at each phase of a
// revision: appending in step 3, appended with the new version's UID and
// UIDVALIDITY in step 4, and retiring in step 6, after the old version's
// \Deleted flag reads back and before its expunge.
type revisionRecord struct {
	appending func() error
	appended  func(uid, uidValidity uint32) error
	retiring  func() error
}

// replaceDraft runs steps 1 to 7 on e's draft, writing record at each
// phase. An error after the appending record leaves the record for
// reconcile.
func (c *Client) replaceDraft(ctx context.Context, e draftEntry, version []byte, record revisionRecord) (replaceOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnected(ctx); err != nil {
		return replaceOutcome{}, err
	}
	if !c.capsLocked(ctx).Has(imap.CapUIDPlus) {
		return replaceOutcome{Verdict: replaceNoUIDPlus}, nil
	}
	state, err := c.draftFolderStateLocked(ctx, e.Folder, false, []uint32{e.UID})
	if err != nil {
		return replaceOutcome{}, err
	}
	if verdict := proveOwnership(e, state); verdict != ownershipProven {
		return replaceOutcome{Verdict: replaceNotProven, Ownership: verdict}, nil
	}
	if err := record.appending(); err != nil {
		return replaceOutcome{}, err
	}
	stored, err := c.appendLocked(ctx, state.Folder, version, []imap.Flag{imap.FlagDraft, imap.FlagSeen})
	if err != nil {
		return replaceOutcome{}, err
	}
	if stored.UID == 0 {
		return replaceOutcome{}, fmt.Errorf("the server advertises UIDPLUS but returned no APPENDUID for the new version in %q, so its UID is unknown; the old version was left in place", state.Folder)
	}
	validity := stored.UIDValidity
	if validity == 0 {
		validity = state.UIDValidity
	}
	if err := record.appended(stored.UID, validity); err != nil {
		return replaceOutcome{}, err
	}
	unflagged, err := c.unflaggedLocked(ctx, state.Folder, e.UID)
	if err != nil {
		return replaceOutcome{}, err
	}
	removed := false
	if unflagged {
		if removed, err = c.retireOldLocked(ctx, state.Folder, e.UID, state.Messages[e.UID].ModSeq, record.retiring); err != nil {
			return replaceOutcome{}, err
		}
	}
	if !removed {
		// The new version is Thane's by the APPENDUID this session got.
		backedOut, err := c.expungeOwnLocked(ctx, state.Folder, stored.UID, 0)
		if err != nil {
			return replaceOutcome{}, err
		}
		if !backedOut {
			return replaceOutcome{}, fmt.Errorf("the operator changed the draft while it was being revised, and Thane's new version at uid %d in %q could not be removed again", stored.UID, state.Folder)
		}
		return replaceOutcome{Verdict: replaceTouched}, nil
	}
	return replaceOutcome{Verdict: replaceDone, UID: stored.UID, UIDValidity: validity}, nil
}

// retireOldLocked removes the old version once its check has passed:
// Thane's \Deleted flag and its read-back (flagOwnLocked), then the
// retiring record, then the expunge (expungeFlaggedLocked). The record
// follows the read-back, so a retiring record always means the flag on
// the old version is one Thane set and saw hold. removed is false when
// the flag did not hold or the old version outlived the expunge. Caller
// must hold c.mu and have selected folder read-write.
func (c *Client) retireOldLocked(ctx context.Context, folder string, uid uint32, modSeq uint64, retiring func() error) (removed bool, err error) {
	flagged, err := c.flagOwnLocked(ctx, folder, uid, modSeq)
	if err != nil || !flagged {
		return false, err
	}
	if err := retiring(); err != nil {
		return false, err
	}
	return c.expungeFlaggedLocked(ctx, folder, uid)
}

// draftRevised is email_draft_revise's result.
type draftRevised struct {
	Action       string   `json:"action"`
	DraftID      string   `json:"draft_id"`
	Account      string   `json:"account"`
	DraftsFolder string   `json:"drafts_folder"`
	UID          uint32   `json:"uid"`
	MessageID    string   `json:"message_id"`
	ReplacedUID  uint32   `json:"replaced_uid"`
	Revision     int      `json:"revision"`
	Subject      string   `json:"subject"`
	To           []string `json:"to"`
	Cc           []string `json:"cc"`
	Note         string   `json:"note"`
}

// reviseDraft replaces an open draft's body, keeping every header but
// Message-ID and Date. Caller must hold the account's draft lock and
// have reconciled.
func (s *Service) reviseDraft(ctx context.Context, acct ResolvedAccount, e draftEntry, body, note string) (draftRevised, error) {
	const tool = "email_draft_revise"
	if !acct.Config.CanDraft() {
		return draftRevised{}, s.refuseDraft(ctx, tool, e, draftRefusedAccess, "Draft "+e.ID+" was not revised: "+accessRefusalSentence(acct.Config))
	}
	if err := s.inspectRevision(ctx, acct, e, body); err != nil {
		return draftRevised{}, err
	}
	messageID, err := freshMessageID(e.From)
	if err != nil {
		return draftRevised{}, fmt.Errorf("revise draft %s: %w", e.ID, err)
	}
	composed, err := ComposeMessage(ComposeOptions{
		From:       e.From,
		To:         e.To,
		Cc:         e.Cc,
		Subject:    e.Subject,
		Body:       body,
		InReplyTo:  e.InReplyTo,
		References: e.References,
		MessageID:  messageID,
	})
	if err != nil {
		return draftRevised{}, fmt.Errorf("revise draft %s: compose the new version: %w", e.ID, err)
	}
	rev := draftRevision{By: draftAuthor(ctx), At: time.Now().UTC(), Note: note}
	next := e
	record := revisionRecord{
		appending: func() error {
			next.Pending = &draftPending{Phase: pendingAppending, MessageID: messageID, SHA256: contentSum(composed.Bytes), Body: body, Revision: rev}
			return s.saveDraft(&next)
		},
		appended: func(uid, uidValidity uint32) error {
			next.Pending.Phase, next.Pending.NewUID, next.Pending.NewUIDValidity = pendingAppended, uid, uidValidity
			return s.saveDraft(&next)
		},
		retiring: func() error {
			next.Pending.Phase = pendingRetiring
			return s.saveDraft(&next)
		},
	}

	out, err := acct.Client.replaceDraft(ctx, e, composed.Bytes, record)
	if err != nil {
		phase := ""
		if next.Pending != nil {
			phase = next.Pending.phase()
		}
		s.logger.Warn("email draft revision failed",
			"draft_id", e.ID, "account", e.Account, "folder", e.Folder, "uid", e.UID,
			"write_ahead", next.Pending != nil, "phase", phase, "error", err,
			"loop_id", tools.LoopIDFromContext(ctx), "conversation_id", tools.ConversationIDFromContext(ctx))
		return draftRevised{}, fmt.Errorf("revise draft %s: %w. If the new version was already stored, the next draft tool call on account %q settles it, so call email_drafts before trying again", e.ID, err, e.Account)
	}
	switch out.Verdict {
	case replaceNoUIDPlus:
		return draftRevised{}, s.refuseDraft(ctx, tool, e, draftRefusedNoUIDPlus, noUIDPlusSentence(e, "revised"))
	case replaceNotProven:
		if out.Ownership == ownershipUIDUnknown {
			return draftRevised{}, s.refuseDraft(ctx, tool, e, draftRefusedNoUIDPlus, uidUnknownSentence(e, "revised"))
		}
		reason := s.closedReasonFor(ctx, acct, e.Folder, e, out.Ownership, nil)
		if err := s.closeDraft(ctx, &next, DraftStageGone, reason); err != nil {
			return draftRevised{}, err
		}
		return draftRevised{}, s.refuseClosedDraft(ctx, tool, "revised", next)
	case replaceTouched:
		if err := s.closeDraft(ctx, &next, DraftStageGone, closedTouchedDuringRevision); err != nil {
			return draftRevised{}, err
		}
		return draftRevised{}, s.refuseClosedDraft(ctx, tool, "revised", next)
	}

	next.PreviousBody, next.Body = e.Body, body
	next.UID, next.UIDValidity, next.MessageID, next.SHA256 = out.UID, out.UIDValidity, messageID, contentSum(composed.Bytes)
	next.Revisions++
	next.addRevision(rev)
	next.Pending = nil
	if err := s.saveDraft(&next); err != nil {
		return draftRevised{}, fmt.Errorf("draft %s was revised and its new version is at uid %d in %q, but the ledger could not record that (%w); the next draft tool call on account %q settles it from the write-ahead record", e.ID, out.UID, e.Folder, err, e.Account)
	}
	s.logger.Info("email draft revised",
		"draft_id", next.ID,
		"account", next.Account,
		"folder", next.Folder,
		"replaced_uid", e.UID,
		"uid", next.UID,
		"message_id", next.MessageID,
		"revision", next.Revisions,
		"by", rev.By,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	return draftRevised{
		Action:       "revised",
		DraftID:      next.ID,
		Account:      next.Account,
		DraftsFolder: next.Folder,
		UID:          next.UID,
		MessageID:    next.MessageID,
		ReplacedUID:  e.UID,
		Revision:     next.Revisions,
		Subject:      next.Subject,
		To:           nonNilStrings(next.To),
		Cc:           nonNilStrings(next.Cc),
		Note:         "The draft's body was replaced in " + next.Folder + "; its recipients, subject, and threading are unchanged, and nothing was sent. The operator sends it by hand.",
	}, nil
}

// inspectRevision hands the new body to the outbound inspector, which
// may only refuse. A revision changes nothing but the body, so the
// decision it sees is the draft's, with route draft_revision.
func (s *Service) inspectRevision(ctx context.Context, acct ResolvedAccount, e draftEntry, body string) error {
	if s.inspector == nil {
		return nil
	}
	from, _ := parseAddress(e.From)
	to, _ := parseAddresses(e.To)
	cc, _ := parseAddresses(e.Cc)
	objection, err := s.inspector.Inspect(ctx, OutboundReview{
		Tool: "email_draft_revise",
		Decision: Decision{
			Disposition:  DispositionDrafted,
			Account:      acct.Name,
			Access:       acct.Config.AccessLevel(),
			Delivery:     acct.Config.DeliveryMode(),
			Attended:     attended(ctx),
			Route:        RouteDraftRevision,
			Recipients:   []RecipientAssessment{},
			DraftsFolder: e.Folder,
		},
		From:      from,
		To:        to,
		Cc:        cc,
		Subject:   e.Subject,
		Body:      body,
		InReplyTo: e.InReplyTo,
	})
	if err != nil {
		objection = "the outbound inspector failed: " + err.Error()
	}
	if objection == "" {
		return nil
	}
	return s.refuseDraft(ctx, "email_draft_revise", e, draftRefusedInspector, "Draft "+e.ID+" was not revised: the outbound inspector objected to the new body ("+objection+"); nothing was changed.")
}

// freshMessageID generates a Message-ID under the sender's domain, as
// ComposeMessage would, for a version whose write-ahead record must name
// it before the version is composed.
func freshMessageID(from string) (string, error) {
	addr, err := parseAddress(from)
	if err != nil {
		return "", fmt.Errorf("from address: %w", err)
	}
	var h mail.Header
	if domain := addr.Domain(); domain != "" {
		err = h.GenerateMessageIDWithHostname(domain)
	} else {
		err = h.GenerateMessageID()
	}
	if err != nil {
		return "", fmt.Errorf("generate message-id: %w", err)
	}
	return h.MessageID()
}

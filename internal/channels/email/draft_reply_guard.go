package email

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
)

// Routes the draft ledger adds to the send decision.
const (
	// RouteDraftOpen: a reply would have been drafted, but Thane already
	// has an open draft answering the same message. Nothing is drafted.
	RouteDraftOpen = "draft_open"

	// RouteOperatorReplyStarted: on the operator's own mailbox, a reply
	// would have been drafted, but the drafts folder already holds a
	// draft answering the same message that is not one of Thane's open
	// drafts, most likely the operator's own. Nothing is drafted.
	RouteOperatorReplyStarted = "operator_reply_started"

	// RouteDraftLimit: a message would have been drafted, but the account
	// already has maxDraftEntriesPerAccount of Thane's drafts open, the
	// most the ledger tracks. Nothing is drafted.
	RouteDraftLimit = "draft_limit"

	// RouteDraftRevision: the decision the outbound inspector sees for
	// email_draft_revise, which changes only a draft's body.
	RouteDraftRevision = "draft_revision"
)

// refuseDraftConflict refuses a message Send would draft when the
// ledger cannot take it: the account already has the most open drafts
// the ledger tracks, or, for a reply, a draft already answers the same
// message: one of Thane's that is still open, which email_draft_revise
// edits instead, or, on the operator's own mailbox, one that is not
// Thane's, which comes first there. The check runs after reconcile, so
// a draft the operator has since sent or discarded no longer counts. A
// check that cannot complete stops the draft rather than risk a second
// answer. Caller must hold the account's draft lock.
func (s *Service) refuseDraftConflict(ctx context.Context, req SendRequest, decision Decision, folder string) error {
	if s.state == nil {
		return nil
	}
	decision.DraftsFolder = folder
	entries, err := s.reconcileDrafts(ctx, req.Account)
	if err != nil {
		return s.draftCheckFailed(ctx, req, decision, err)
	}
	if open := countOpenDrafts(entries); open >= maxDraftEntriesPerAccount {
		decision.Route = RouteDraftLimit
		decision.Reason = fmt.Sprintf("Email not drafted: account %q already has %d of Thane's drafts open in %q, the most Thane tracks for one account, so a new draft could not be tracked; nothing was sent or drafted. Tell the operator those drafts are waiting to be sent or discarded, or withdraw ones no longer wanted with email_draft_withdraw (email_drafts capability tag).", req.Account.Name, open, folder)
		return s.refuse(ctx, req.Tool, decision)
	}
	if req.InReplyTo == "" {
		return nil
	}
	for _, e := range entries {
		if e.Stage == DraftStageOpen && sameMessageID(e.InReplyTo, req.InReplyTo) {
			decision.Route = RouteDraftOpen
			decision.Reason = draftOpenSentence(e)
			return s.refuse(ctx, req.Tool, decision)
		}
	}
	if !req.Account.Config.OperatorMailbox() {
		return nil
	}
	found, err := req.Account.Client.draftsMatching(ctx, folder, []imap.SearchCriteriaHeaderField{{Key: "In-Reply-To", Value: bracketed(req.InReplyTo)}})
	if err != nil {
		return s.draftCheckFailed(ctx, req, decision, err)
	}
	for uid, msg := range found.Messages {
		if recordedDraft(entries, found, uid, msg) {
			continue
		}
		decision.Route = RouteOperatorReplyStarted
		decision.Reason = fmt.Sprintf("Email not drafted: %q already holds a reply to this message that is not one of Thane's open drafts, most likely the operator's own start on an answer, and on the operator's own mailbox their draft comes first; nothing was sent or drafted. Leave that draft alone, and if you have something to add, tell the operator instead of writing a second draft.", folder)
		return s.refuse(ctx, req.Tool, decision)
	}
	return nil
}

// draftOpenSentence refuses a second draft answering the message e
// answers, naming e and what can be done with it.
func draftOpenSentence(e draftEntry) string {
	if !e.uidKnown() {
		return fmt.Sprintf("Email not drafted: Thane's draft %s already answers this message in %q, so a second draft would leave the operator two answers to choose between; nothing was sent or drafted. The server never reported that draft's UID, so no draft tool can change or withdraw it: leave it for the operator, and tell them what you would change.", e.ID, e.Folder)
	}
	return fmt.Sprintf("Email not drafted: Thane's draft %s already answers this message in %q and is still Thane's to edit, so a second draft would leave the operator two answers to choose between; nothing was sent or drafted. Change that draft with email_draft_revise (draft_id %s, under the email_drafts capability tag), or withdraw it with email_draft_withdraw first if a fresh draft is really wanted.", e.ID, e.Folder, e.ID)
}

// countOpenDrafts counts the open entries.
func countOpenDrafts(entries []draftEntry) int {
	n := 0
	for _, e := range entries {
		if e.Stage == DraftStageOpen {
			n++
		}
	}
	return n
}

// draftCheckFailed logs and returns a conflict check that could not
// complete. It is a fault, not a decision, so it is not a PolicyRefusal.
func (s *Service) draftCheckFailed(ctx context.Context, req SendRequest, decision Decision, err error) error {
	err = fmt.Errorf("check the drafts folder of account %q before drafting, so nothing was drafted: %w", req.Account.Name, err)
	s.logDecision(ctx, req, decision, SendOutcome{}, err)
	return err
}

// recordedDraft reports whether the drafts-folder message at uid is one
// of Thane's open drafts: an open entry recorded that UID under the
// folder's UIDVALIDITY with the message's Message-ID, or, on a server
// that never reported the draft's UID, recorded its Message-ID.
func recordedDraft(entries []draftEntry, state draftFolderState, uid uint32, msg draftMessage) bool {
	for _, e := range entries {
		if e.Stage != DraftStageOpen || !sameMessageID(e.MessageID, msg.MessageID) {
			continue
		}
		if e.UID == 0 || (e.UID == uid && e.UIDValidity == state.UIDValidity) {
			return true
		}
	}
	return false
}

// openDraftsAnswering returns the draft_ids of the account's open
// entries that answer inReplyTo, read from the ledger alone, for the
// note a reply sent directly carries so a draft answering the same
// message can be withdrawn. A ledger that cannot be read yields none.
func (s *Service) openDraftsAnswering(account, inReplyTo string) []string {
	if s.state == nil || inReplyTo == "" {
		return nil
	}
	entries, err := s.draftEntries(account)
	if err != nil {
		s.logger.Debug("email open-draft lookup skipped", "account", account, "error", err)
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.Stage == DraftStageOpen && sameMessageID(e.InReplyTo, inReplyTo) {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

package email

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// The draft tools act only on drafts in the ledger (draft_ledger.go),
// and each one proves the draft is still Thane's before it acts. Every
// handler holds its account's draft lock (draft_lock.go) from its
// reconcile to its last ledger write.

// maxDraftNoteBytes bounds a revision note or a withdrawal reason, which
// the entry's history keeps.
const maxDraftNoteBytes = 500

// HandleDrafts lists the draft ledger after reconciling each account.
func (t *Tools) HandleDrafts(ctx context.Context, args map[string]any) (string, error) {
	s := t.service
	if err := s.requireDraftLedger(); err != nil {
		return "", err
	}
	accounts, err := t.draftAccounts(ctx, toolargs.TrimmedString(args, "account"))
	if err != nil {
		return "", err
	}
	includeClosed := toolargs.Bool(args, "include_closed")

	resp := draftsResponse{Drafts: []draftRowView{}, Errors: []draftAccountError{}}
	var listed []draftEntry
	for _, acct := range accounts {
		entries, err := s.reconcileDraftsLocked(ctx, acct)
		if err != nil {
			resp.Errors = append(resp.Errors, draftAccountError{Account: acct.Name, Error: err.Error()})
			continue
		}
		open := 0
		for _, e := range entries {
			if e.Stage == DraftStageOpen {
				open++
			} else if !includeClosed {
				continue
			}
			listed = append(listed, e)
		}
		resp.Open += open
		s.recordOp("email_drafts", acct.Name, "", fmt.Sprintf("%d open", open))
	}
	slices.SortStableFunc(listed, func(a, b draftEntry) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	now := time.Now()
	for _, e := range listed {
		resp.Drafts = append(resp.Drafts, newDraftRow(e, now))
	}
	resp.Count, resp.Total = len(resp.Drafts), len(resp.Drafts)
	return marshalDraftsResponse(resp)
}

// draftAccounts resolves the accounts email_drafts lists: the one named,
// else the one this loop is bound to, else every configured account.
func (t *Tools) draftAccounts(ctx context.Context, requested string) ([]ResolvedAccount, error) {
	if requested != "" || boundAccount(ctx) != "" {
		acct, err := t.service.ResolveAccount(ctx, requested)
		if err != nil {
			return nil, err
		}
		return []ResolvedAccount{acct}, nil
	}
	names := t.service.AccountNames()
	accounts := make([]ResolvedAccount, 0, len(names))
	for _, name := range names {
		acct, err := t.service.ResolveAccount(ctx, name)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, acct)
	}
	return accounts, nil
}

// draftFor loads the entry a draft tool names, resolves its account
// through the loop's binding, reconciles that account, and returns the
// entry as it stands afterwards. A draft_id the ledger does not hold is
// refused. Caller must hold the draft's account lock (lockDraft).
func (t *Tools) draftFor(ctx context.Context, tool, id string) (draftEntry, ResolvedAccount, error) {
	s := t.service
	if err := s.requireDraftLedger(); err != nil {
		return draftEntry{}, ResolvedAccount{}, err
	}
	notInLedger := fmt.Errorf("%s: draft_id %q is not in the draft ledger. The draft tools act only on drafts Thane wrote and recorded, never on a draft Thane did not write; call email_drafts (include_closed: true adds recently closed ones) for the ids that exist", tool, id)
	e, ok, err := s.draftByID(id)
	if err != nil {
		return draftEntry{}, ResolvedAccount{}, err
	}
	if !ok {
		return draftEntry{}, ResolvedAccount{}, notInLedger
	}
	acct, err := s.ResolveAccount(ctx, e.Account)
	if err != nil {
		return draftEntry{}, ResolvedAccount{}, err
	}
	entries, err := s.reconcileDrafts(ctx, acct)
	if err != nil {
		return draftEntry{}, ResolvedAccount{}, err
	}
	for _, current := range entries {
		if current.ID == id {
			return current, acct, nil
		}
	}
	return draftEntry{}, ResolvedAccount{}, notInLedger
}

// draftIDProblem validates the draft_id argument.
func draftIDProblem(id string) string {
	if id == "" {
		return "draft_id is required (string): the id of one of Thane's drafts, from email_drafts or from the draft_id an email_reply or email_send result carries"
	}
	return ""
}

// HandleDraftGet reads a draft beside the message it answers.
func (t *Tools) HandleDraftGet(ctx context.Context, args map[string]any) (string, error) {
	const tool = "email_draft_get"
	id := toolargs.TrimmedString(args, "draft_id")
	if p := draftIDProblem(id); p != "" {
		return "", fmt.Errorf("%s", p)
	}
	s := t.service
	unlock := t.lockDraft(id)
	defer unlock()
	e, acct, err := t.draftFor(ctx, tool, id)
	if err != nil {
		return "", err
	}

	draftBody := "[no draft body: the draft is " + string(e.Stage) + ", so it is no longer Thane's to read; closed_reason says why]"
	switch {
	case e.Stage == DraftStageOpen && e.UID == 0:
		draftBody = "[the server never reported this draft's UID, so it cannot be read back; this is the body Thane wrote]\n" + e.Body
	case e.Stage == DraftStageOpen:
		msg, err := acct.Client.ReadMessage(ctx, ReadOptions{Folder: e.Folder, UID: e.UID, Peek: true})
		if err != nil {
			return "", err
		}
		if !sameMessageID(msg.MessageID, e.MessageID) {
			if err := s.closeDraft(ctx, &e, DraftStageGone, closedMessageIDChanged); err != nil {
				return "", err
			}
			return "", s.refuseClosedDraft(ctx, tool, "read", e)
		}
		draftBody = msg.TextBody
	}

	header := newDraftGetHeader(e, acct.Config, newIdentityLookup(ctx, t.contacts, t.logger), time.Now())
	originalBody := "[this draft answers no message]"
	if e.Original != nil {
		original, err := t.readDraftOriginal(ctx, acct, *e.Original)
		if err != nil {
			return "", err
		}
		if original != nil {
			header.Original.Found, header.Original.UID = true, original.UID
			originalBody = original.TextBody
		} else {
			originalBody = fmt.Sprintf("[the original is no longer in %q; email_search with message_id %q finds it wherever it went]", e.Original.Folder, e.Original.MessageID)
		}
	}
	s.recordOp(tool, acct.Name, e.Folder, e.ID)
	return renderDraftGet(header, draftBody, originalBody)
}

// readDraftOriginal finds a draft's original by Message-ID in the
// folder it was read from and reads it with PEEK, or returns nil when it
// is no longer there.
func (t *Tools) readDraftOriginal(ctx context.Context, acct ResolvedAccount, o draftOriginal) (*Message, error) {
	found, err := acct.Client.SearchMessages(ctx, SearchOptions{Folder: o.Folder, MessageID: o.MessageID, Limit: 1})
	if err != nil {
		if FailureKindOf(err) == FailureFolderNotFound {
			return nil, nil
		}
		return nil, err
	}
	if len(found.Envelopes) == 0 {
		return nil, nil
	}
	return acct.Client.ReadMessage(ctx, ReadOptions{Folder: o.Folder, UID: found.Envelopes[0].UID, Peek: true})
}

// HandleDraftRevise replaces an open draft's body.
func (t *Tools) HandleDraftRevise(ctx context.Context, args map[string]any) (string, error) {
	const tool = "email_draft_revise"
	id := toolargs.TrimmedString(args, "draft_id")
	body := toolargs.String(args, "body")
	note := toolargs.TrimmedString(args, "note")
	var problems []string
	if p := draftIDProblem(id); p != "" {
		problems = append(problems, p)
	}
	if strings.TrimSpace(body) == "" {
		problems = append(problems, "body is required (markdown): the complete new body, which replaces the old one entirely")
	}
	if len(note) > maxDraftNoteBytes {
		problems = append(problems, fmt.Sprintf("note is %d bytes and may be at most %d: say what changed in a sentence", len(note), maxDraftNoteBytes))
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	s := t.service
	unlock := t.lockDraft(id)
	defer unlock()
	e, acct, err := t.draftFor(ctx, tool, id)
	if err != nil {
		return "", err
	}
	if e.Stage != DraftStageOpen {
		return "", s.refuseClosedDraft(ctx, tool, "revised", e)
	}
	if !e.uidKnown() {
		return "", s.refuseDraft(ctx, tool, e, draftRefusedNoUIDPlus, uidUnknownSentence(e, "revised"))
	}
	result, err := s.reviseDraft(ctx, acct, e, body, note)
	if err != nil {
		return "", err
	}
	s.recordOp(tool, acct.Name, result.DraftsFolder, fmt.Sprintf("%s revision %d", result.DraftID, result.Revision))
	return marshalResponse(result)
}

// HandleDraftWithdraw moves an open draft to the trash folder.
func (t *Tools) HandleDraftWithdraw(ctx context.Context, args map[string]any) (string, error) {
	const tool = "email_draft_withdraw"
	id := toolargs.TrimmedString(args, "draft_id")
	reason := toolargs.TrimmedString(args, "reason")
	var problems []string
	if p := draftIDProblem(id); p != "" {
		problems = append(problems, p)
	}
	if len(reason) > maxDraftNoteBytes {
		problems = append(problems, fmt.Sprintf("reason is %d bytes and may be at most %d: say why in a sentence", len(reason), maxDraftNoteBytes))
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	s := t.service
	unlock := t.lockDraft(id)
	defer unlock()
	e, acct, err := t.draftFor(ctx, tool, id)
	if err != nil {
		return "", err
	}
	if err := s.requireOrganize(acct, tool); err != nil {
		return "", err
	}
	if e.Stage != DraftStageOpen {
		return "", s.refuseClosedDraft(ctx, tool, "withdrawn", e)
	}
	if !e.uidKnown() {
		return "", s.refuseDraft(ctx, tool, e, draftRefusedNoUIDPlus, uidUnknownSentence(e, "withdrawn"))
	}

	r := s.newFolderResolver(acct)
	trash := r.folder(ctx, RoleTrash)
	if trash == "" && r.err != nil {
		return "", fmt.Errorf("%s cannot tell which folder is account %q's trash folder, because listing the account's folders failed (%w); draft %s was left where it is. Try again once email_folders answers for this account", tool, acct.Name, r.err, e.ID)
	}
	if trash == "" {
		return "", s.refuseDraft(ctx, tool, e, draftRefusedNoTrash, fmt.Sprintf("Draft %s was not withdrawn: no folder on account %q has the trash role, because trash_folder is not configured and the server marks no folder with the trash special-use attribute, and a withdrawn draft goes nowhere else; it is still in %q and still Thane's. Ask the operator to configure trash_folder for this account, or tell them this draft should not be sent.", e.ID, acct.Name, e.Folder))
	}

	out, err := acct.Client.withdrawDraft(ctx, e, trash)
	if err != nil {
		if out.Moved {
			return "", withdrawConfirmError(e, trash, err)
		}
		return "", t.refreshOnFolderMiss(ctx, acct, err)
	}
	if out.NoUIDPlus {
		return "", s.refuseDraft(ctx, tool, e, draftRefusedNoUIDPlus, noUIDPlusSentence(e, "withdrawn"))
	}
	if out.Verdict != ownershipProven {
		closed := s.closedReasonFor(ctx, acct, e.Folder, e, out.Verdict, nil)
		if err := s.closeDraft(ctx, &e, DraftStageGone, closed); err != nil {
			return "", err
		}
		return "", s.refuseClosedDraft(ctx, tool, "withdrawn", e)
	}
	if !out.Confirmed {
		return s.unconfirmedWithdrawal(ctx, tool, e, trash, out)
	}
	e.WithdrawnTo = &draftWithdrawal{Folder: trash, UID: out.TrashUID, Reason: reason, By: draftAuthor(ctx), At: time.Now().UTC()}
	if err := s.closeDraft(ctx, &e, DraftStageWithdrawn, closedWithdrawn); err != nil {
		return "", fmt.Errorf("draft %s was moved to %q, but the ledger could not record the withdrawal: %w", e.ID, trash, err)
	}
	s.recordOp(tool, acct.Name, trash, e.ID)
	return marshalResponse(draftWithdrawnView{
		Action:        "withdrawn",
		DraftID:       e.ID,
		Account:       e.Account,
		DraftsFolder:  e.Folder,
		TrashFolder:   trash,
		TrashUID:      out.TrashUID,
		TrashUIDKnown: out.Confirmed,
		Note:          "The draft was moved from " + e.Folder + " to " + trash + " and will not be sent; its entry is withdrawn. Nothing was sent.",
	})
}

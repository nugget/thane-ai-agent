package email

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Refusal reasons a draft tool reports in its refusal JSON.
const (
	draftRefusedGone      = "gone"
	draftRefusedHeld      = "held"
	draftRefusedWithdrawn = "withdrawn"
	draftRefusedNoUIDPlus = "no_uidplus"
	draftRefusedNoTrash   = "no_trash_folder"
	draftRefusedAccess    = "access"
	draftRefusedInspector = "inspector"
)

// draftRefusalView is the JSON a draft tool's refusal carries.
type draftRefusalView struct {
	Action       string     `json:"action"`
	Reason       string     `json:"reason"`
	DraftID      string     `json:"draft_id"`
	Account      string     `json:"account"`
	Stage        DraftStage `json:"stage"`
	ClosedReason string     `json:"closed_reason,omitempty"`
}

// draftRefusal is a draft tool's refusal: one sentence a model can act
// on, then the JSON naming the draft and the reason. Nothing in the
// mailbox changed.
type draftRefusal struct {
	Message string
	View    draftRefusalView
}

// Error renders the sentence, a newline, and the JSON.
func (e *draftRefusal) Error() string {
	data, err := json.Marshal(e.View)
	if err != nil {
		return e.Message
	}
	return e.Message + "\n" + string(data)
}

// refuseDraft logs a draft tool's refusal, keyed to the turn, and
// returns it.
func (s *Service) refuseDraft(ctx context.Context, tool string, e draftEntry, reason, message string) error {
	s.logger.Info("email draft action refused",
		"tool", tool,
		"draft_id", e.ID,
		"account", e.Account,
		"reason", reason,
		"stage", e.Stage,
		"closed_reason", e.ClosedReason,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	return &draftRefusal{Message: message, View: draftRefusalView{
		Action:       "refused",
		Reason:       reason,
		DraftID:      e.ID,
		Account:      e.Account,
		Stage:        e.Stage,
		ClosedReason: e.ClosedReason,
	}}
}

// refuseClosedDraft refuses an action on an entry that is not open, with
// the reason and sentence its stage and closed reason call for. verb is
// the past participle of what was refused ("revised", "withdrawn").
func (s *Service) refuseClosedDraft(ctx context.Context, tool, verb string, e draftEntry) error {
	switch {
	case e.Stage == DraftStageWithdrawn:
		return s.refuseDraft(ctx, tool, e, draftRefusedWithdrawn, fmt.Sprintf("Draft %s was not %s: Thane already withdrew it to the trash folder, so it is no longer a draft; nothing was changed. If an answer is still wanted, write a new reply.", e.ID, verb))
	case e.heldByOperator():
		return s.refuseDraft(ctx, tool, e, draftRefusedHeld, fmt.Sprintf("Draft %s was not %s: the operator has taken it over, because %s, and a draft the operator has touched is theirs; nothing was changed. Leave it to them, and if what you meant to change matters, tell them.", e.ID, verb, closedReasonClause(e)))
	default:
		return s.refuseDraft(ctx, tool, e, draftRefusedGone, fmt.Sprintf("Draft %s was not %s: it is no longer Thane's draft, because %s; nothing was changed. Do not write it again; if the message still needs an answer, bring it to the operator.", e.ID, verb, closedReasonClause(e)))
	}
}

// closedReasonClause explains a closed reason as the clause of a
// refusal sentence.
func closedReasonClause(e draftEntry) string {
	switch e.ClosedReason {
	case closedVanished:
		return fmt.Sprintf("it is no longer in %q and nothing replaced it, so the operator sent or discarded it", e.Folder)
	case closedOperatorTookOver:
		return fmt.Sprintf("a draft answering the same message is in %q under another UID, which is what an edit in the operator's client leaves", e.Folder)
	case closedTouchedDuringRevision:
		return "the operator changed it while Thane was revising it, so Thane removed its own new version again"
	case closedUIDValidityChanged:
		return fmt.Sprintf("%q was rebuilt (its UIDVALIDITY changed), so no message in it can be proven to be the draft Thane wrote", e.Folder)
	case closedFolderChanged:
		return fmt.Sprintf("the account's drafts folder is no longer %q, the folder the draft was written to", e.Folder)
	case closedNoDraftsFolder:
		return "no folder on the account has the drafts role any more"
	case closedMessageIDChanged:
		return fmt.Sprintf("the message at its UID in %q carries another Message-ID", e.Folder)
	case closedMarkedDeleted:
		return fmt.Sprintf("it is marked \\Deleted in %q, which is how the operator's client discards a draft before it expunges it, so the operator discarded it", e.Folder)
	}
	return "its entry closed as " + e.ClosedReason
}

// noUIDPlusSentence refuses a revision on a server that cannot report
// one UID alone.
func noUIDPlusSentence(e draftEntry, verb string) string {
	need, next := "changing a draft safely needs UIDPLUS to learn the new copy's UID and to expunge one UID alone", "tell the operator what you would change instead"
	if verb == "withdrawn" {
		need, next = "a withdrawal counts only when the server reports that its move carried this draft, which only a UIDPLUS server reports", "tell the operator this draft should not be sent"
	}
	return fmt.Sprintf("Draft %s was not %s: the IMAP server for account %q advertises neither UIDPLUS nor IMAP4rev2, and %s; nothing was changed. Retrying fails the same way, so %s.", e.ID, verb, e.Account, need, next)
}

// uidUnknownSentence refuses an action on a draft whose UID the server
// never reported and no read since could prove. verb is "revised" or
// "withdrawn".
func uidUnknownSentence(e draftEntry, verb string) string {
	next := "tell the operator what you would change instead"
	if verb == "withdrawn" {
		next = "tell the operator this draft should not be sent"
	}
	return fmt.Sprintf("Draft %s was not %s: the IMAP server for account %q never reported this draft's UID, and no message in %q could be proven to hold the bytes Thane wrote, so Thane cannot prove which message it is or act on it alone; nothing was changed. Retrying fails the same way, so %s.", e.ID, verb, e.Account, e.Folder, next)
}

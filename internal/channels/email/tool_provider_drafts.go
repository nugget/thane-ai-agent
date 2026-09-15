package email

import (
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// The draft tools sit behind the email_drafts capability tag, apart from
// the email tag, so a loop that only triages or writes first drafts never
// sees them, and a loop that edits drafts carries both tags.

// draftEditPath is the edit path every draft tool description teaches.
const draftEditPath = "The edit path for a draft in flight: find it with email_drafts, read it beside the message it answers with email_draft_get, then revise it with email_draft_revise for accuracy and for tone in the account's writes_as and voice, or withdraw it with email_draft_withdraw. Nothing here sends; the operator sends every draft by hand. "

// draftOwnershipRule is the ownership rule every draft tool description
// states.
const draftOwnershipRule = "A draft is Thane's only while the drafts folder still holds it at the UID Thane recorded with the Message-ID Thane wrote, not marked \\Deleted; an edit in the operator's client stores it anew under another UID, and from then on it is the operator's and no draft tool touches it. "

func draftIDParameter() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": "The draft's id in the draft ledger: from email_drafts, or the draft_id an email_reply or email_send result carries.",
	}
}

func (t *Tools) draftToolDefinitions() []*tools.Tool {
	return []*tools.Tool{
		{
			Name: "email_drafts",
			Description: "List the drafts Thane wrote, from its draft ledger, after checking each open one against its account's drafts folder. " +
				draftOwnershipRule +
				"Returns JSON {count, total, truncated, open, drafts:[{draft_id, account, stage, closed_reason, subject, to, original:{from, subject, message_id}, revisions, last_revised:{by, at}}], errors:[{account, error}]}, newest first. " +
				"stage is open (still Thane's; the only stage the other draft tools act on), gone (no longer Thane's), or withdrawn (moved to the trash with email_draft_withdraw). closed_reason says why an entry is gone: vanished (the operator sent or discarded it), marked_deleted (the operator's client flagged it \\Deleted to discard it and has not expunged it yet), operator_took_over (a draft answering the same message is there under another UID, because the operator edited it), operator_touched_during_revision, or uid_validity_changed, folder_changed, no_drafts_folder, and message_id_changed when the folder itself changed. " +
				"Entries that are not open are listed only with include_closed: true, and are forgotten 14 days after they close; each account keeps at most 200 entries, forgetting closed ones first and never an open one, so an account that already has 200 open drafts refuses a new one (decision.route draft_limit). " +
				"original is the message the draft answers, absent for a draft written with email_send. revisions counts the email_draft_revise calls applied; last_revised says who wrote the current version (a loop's name, or operator for a turn the operator was present for) and when, as a delta. " +
				"open counts the open drafts on the accounts listed. An account whose drafts folder could not be checked is listed under errors, and its drafts are left out. The result is capped at 16 KB; when that drops rows, count falls below total and truncated is true. " +
				draftEditPath,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"account": map[string]any{
						"type":        "string",
						"description": "Email account name (from email.accounts) whose drafts to list. Omit to list every account this loop may reach: only its bound account when it is bound, where naming a different account is refused.",
					},
					"include_closed": map[string]any{
						"type":        "boolean",
						"description": "Also list entries that are gone or withdrawn (default: false).",
					},
				},
			},
			Handler: t.HandleDrafts,
		},
		{
			Name: "email_draft_get",
			Description: "Read one of Thane's drafts beside the message it answers, by draft_id. Returns a JSON header " +
				"{draft_id, account, stage, closed_reason, drafts_folder, uid, message_id, from, writes_as, voice, owner, to, cc, subject, in_reply_to, revisions, history:[{by, at, note}], history_omitted, original:{folder, message_id, from, subject, uid, found}, draft_body_truncated, original_body_truncated}, " +
				"then a line containing only ---, the draft's text/plain part as stored in the drafts folder (the body Thane wrote, with its markdown formatting removed), another line containing only ---, and the original's readable body. Both are read without marking anything seen. " +
				"writes_as is the From mail from the account goes out under, voice is the operator's note on how that mail should sound, and owner says whose mailbox it is; revise against them. " +
				"history lists at most 10 versions, oldest first: the first is always the draft as it was written, and when history_omitted is above 0, that many versions between it and the latest 9 are left out. original.from carries the directory's answer about the sender, as email_read's addresses do. " +
				"The original is looked for by its Message-ID in the folder it was read from; when it has moved, original.found is false and its body section says so, and email_search with message_id finds it. " +
				"A draft that is not open has no body to read; its header says why. " +
				"The result stays within 32 KB, and the header within 16 KB of it: a subject over 1 KB, voice over 2 KB, address over 320 bytes, history author over 256 bytes, or note over 500 bytes is cut and ends with …[cut]; to and cc list at most 25 addresses each, with addresses_omitted counting the rest. " +
				"A header that would still pass 16 KB is replaced by {draft_id, account, stage, closed_reason, drafts_folder, uid, revisions, history_omitted, addresses_omitted, header_truncated: true, draft_body_truncated, original_body_truncated}, where the omitted counts cover every version and address; email_read of uid in drafts_folder shows the draft's own headers. " +
				"A body that would pass what is left is cut, ends with a marker, and sets its truncated flag. " +
				"A draft_id the ledger does not hold is refused. " + draftOwnershipRule,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"draft_id": draftIDParameter(),
				},
				"required": []string{"draft_id"},
			},
			Handler: t.HandleDraftGet,
		},
		{
			Name: "email_draft_revise",
			Description: "Replace the body of one of Thane's open drafts with a new version, for accuracy against the message it answers and for tone in the account's writes_as and voice (email_draft_get shows all three). " +
				"Only the body changes: recipients, subject, and threading headers stay exactly as they are, and nothing is sent. body is the complete new body in markdown and replaces the old one entirely, so carry over everything that should stay; note says briefly what changed and why, for the draft's history. " +
				"Go proves the draft is still Thane's, records the revision it is about to make, stores the new version under a fresh Message-ID, then flags the old version \\Deleted, checks that the flag held, and expunges that one UID alone, so no other message anyone marked for deletion is touched. The account's outbound inspector reviews the new body first. " +
				"Returns JSON {action: revised, draft_id, account, drafts_folder, uid, message_id, replaced_uid, revision, subject, to, cc, note}; uid and message_id are the new version's, and revision counts the revisions applied. " +
				"A refusal is one sentence followed by JSON {action: refused, reason, draft_id, account, stage, closed_reason}, and nothing in the mailbox changed: reason gone means the draft is no longer Thane's (the operator sent or discarded it, their client marked it \\Deleted, or its folder changed); held means the operator has taken it over (they edited it, or touched it while the revision ran, in which case Thane's new version was removed again); withdrawn means Thane already withdrew it; no_uidplus means the server lacks the UIDPLUS extension a safe replacement needs, or never reported this draft's UID; access means the account can no longer write mail; inspector means the outbound inspector objected to the new body. " +
				"A draft that is gone or held is the operator's: never write it again as a new draft, and if what you meant to change matters, tell the operator. A draft_id the ledger does not hold is refused.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"draft_id": draftIDParameter(),
					"body": map[string]any{
						"type":        "string",
						"description": "The complete new body in markdown; converted to text/plain and text/html like email_send's. Supports temp:LABEL references.",
					},
					"note": map[string]any{
						"type":        "string",
						"description": "One sentence on what changed and why, at most 500 bytes, kept in the draft's history.",
					},
				},
				"required": []string{"draft_id", "body"},
			},
			ContentResolveExempt: []string{"draft_id", "note"},
			Handler:              t.HandleDraftRevise,
		},
		{
			Name: "email_draft_withdraw",
			Description: "Withdraw one of Thane's open drafts by moving it to the account's trash folder, for a draft that should not be sent at all: the message no longer needs this answer, or a different one is wanted. " +
				"The draft must still be Thane's; a draft the operator has touched is theirs and is refused. The trash folder is found by its role: the account's trash_folder, else the folder the server marks with the trash special-use attribute. Withdrawing is not filing, so the account's move_into does not apply. " +
				"reason says why, for the draft's entry. Returns JSON {action, draft_id, account, drafts_folder, trash_folder, trash_uid, trash_uid_known, note}. " +
				"action withdrawn means the server reported that the move carried this draft's UID into the trash, the only proof Go accepts, and the entry's stage becomes withdrawn. " +
				"action unconfirmed means the server accepted the move without that report: the draft is still in the drafts folder, or it is gone from there, most likely because the operator sent or discarded it first. A copy of it in the trash proves nothing, since the operator's client may have filed it there. The entry is not marked withdrawn, and note says which; never report such a draft as withdrawn or say it will not be sent, and tell the operator it should not go out. " +
				"A refusal is one sentence followed by JSON {action: refused, reason, draft_id, account, stage, closed_reason}, and the draft stays where it is: reason gone, held, or withdrawn as for email_draft_revise; no_trash_folder when no folder on the account has the trash role, which only the operator can fix by configuring trash_folder; no_uidplus when the server lacks the UIDPLUS extension, without which it never reports which message a move carried, or never reported the draft's UID. " +
				"An account whose access is read refuses this tool, and a draft_id the ledger does not hold is refused.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"draft_id": draftIDParameter(),
					"reason": map[string]any{
						"type":        "string",
						"description": "One sentence on why the draft is withdrawn, at most 500 bytes.",
					},
				},
				"required": []string{"draft_id"},
			},
			ContentResolveExempt: []string{"draft_id", "reason"},
			Handler:              t.HandleDraftWithdraw,
		},
	}
}

package talents

import (
	"regexp"
	"strings"
	"testing"
)

// siteFolderName matches a folder name some server happens to use, as
// opposed to a role. The email talents name roles (junk, trash) and send
// the model to email_folders or the Email Accounts block for the name,
// because a name taught by example is a name the model files into on a
// server where it means something else.
var siteFolderName = regexp.MustCompile(`\b(Archive|Drafts|Trash|Junk|Spam)\b|\[Gmail\]|All Mail`)

// emailTalentText returns the in-tree email talents (the email trailhead
// and its leaves), keyed by name, with whitespace collapsed so a phrase
// check does not depend on where the markdown wraps.
func emailTalentText(t *testing.T) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, talent := range loadRepoTalents(t) {
		if talent.Name == "email" || strings.HasPrefix(talent.Name, "email_") {
			out[talent.Name] = strings.Join(strings.Fields(talent.Content), " ")
		}
	}
	for _, name := range []string{"email", "email_triage", "email_respond", "email_organize", "email_drafts"} {
		if _, ok := out[name]; !ok {
			t.Fatalf("email talent %q not loaded; the guard would be meaningless", name)
		}
	}
	return out
}

// TestEmailTalentNamesNoSiteFolder guards the email talents against
// teaching a folder name as a destination. Folder names come from the
// server's listing or config, never from an example.
func TestEmailTalentNamesNoSiteFolder(t *testing.T) {
	for name, text := range emailTalentText(t) {
		for _, hit := range siteFolderName.FindAllString(text, -1) {
			t.Errorf("talent %s names the site folder %q; name the role and send the model to email_folders or the Email Accounts block instead", name, hit)
		}
		if strings.Contains(strings.ToLower(text), "archive read mail") {
			t.Errorf("talent %s suggests archiving read mail, which teaches clearing an inbox away", name)
		}
	}
}

// TestEmailTalentTeachesWhoseMailbox pins the teaching that goes with an
// operator mailbox, and the stale lines it replaced: drafts no longer
// carry the audit Bcc, email_move no longer reads folder as the target,
// and example bodies no longer sign as the assistant.
func TestEmailTalentTeachesWhoseMailbox(t *testing.T) {
	text := emailTalentText(t)
	present := []struct {
		name   string
		talent string
		want   string
	}{
		{"whose mailbox section", "email", "## Whose mailbox"},
		{"keyed on owner", "email", "`owner: operator`"},
		{"reads leave mail unseen", "email", "`reads_mark_seen: false`"},
		{"flag what needs the operator", "email", "`email_mark` flag `flagged`"},
		{"write as the owner", "email", "in the name `writes_as` shows and the `voice` the entry gives"},
		{"never sign as the assistant", "email", "Never sign it with your own name"},
		{"assistant accounts keep their own voice", "email", "written by you, in your own voice"},
		{"never from another account", "email", "never worked around by writing from another"},
		{"mark_seen default follows owner", "email_triage", "`mark_seen` defaults to false on an operator mailbox"},
		{"unattended seen refused on read", "email_triage", "retry with `mark_seen: false`"},
		{"folders result lists attributes", "email_triage", "selectable, delimiter, attributes, messages, unseen"},
		{"every role listed", "email_triage", "`inbox`, `drafts`, `sent`, `trash`, `junk`, `archive`, `all`, `flagged`, `important`, or empty"},
		{"access refusal names no other account", "email_respond", "never write it from another account; report that the account cannot compose"},
		{"drafts carry no bcc", "email_respond", "It carries no audit Bcc, so `bcc_count` is 0."},
		{"draft on purpose carries no bcc", "email_respond", "The draft carries no audit `Bcc` on any account"},
		{"bcc rides only sent mail", "email_respond", "which rides only sent mail"},
		{"seen refused unattended on operator mailbox", "email_organize", "adding `seen` is refused in any turn the operator is not present for"},
		{"exactly one target", "email_organize", "The target is exactly one of `destination` or `destination_role`."},
		{"folder is the source", "email_organize", "`folder` is the source (default INBOX) and never the target."},
		{"move recorded with uids", "email_organize", "records each move with both UID lists"},
		{"move record is capped", "email_organize", "at most 10 of each with the rest counted"},
		{"a draft goes out under the account's from", "email_respond", "under the account's own From, which is the operator's name only on an operator mailbox"},
		{"undo names the source explicitly", "email_organize", "the target set explicitly to the original `source_folder`"},
	}
	for _, tt := range present {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(text[tt.talent], tt.want) {
				t.Errorf("talent %s must contain %q", tt.talent, tt.want)
			}
		})
	}

	absent := []struct {
		name string
		gone string
	}{
		{"assistant signature in an example body", `\n\n— Thane`},
		{"audit copy on a draft", "audit copy included"},
		{"draft carries the audit bcc", "The draft carries the audit `Bcc`"},
		{"folder-as-destination shorthand", "the `folder` value is treated as the destination"},
		{"another account as a recovery", "use an account that can send"},
		{"every draft goes out as the operator", "as themselves"},
		{"every draft goes out in the operator's name", "in their own name"},
	}
	for _, tt := range absent {
		t.Run(tt.name, func(t *testing.T) {
			for name, body := range text {
				if strings.Contains(body, tt.gone) {
					t.Errorf("talent %s still contains %q", name, tt.gone)
				}
			}
		})
	}
}

// TestEmailTalentTeachesFilingPolicy pins the slice 2 teaching: what
// counts as obvious spam, filing it by role, the junk guard's refused
// list and its recovery, the undo recipe, move_into and the INBOX
// return, the drafts folder as a source, and hidden_content. It also
// pins that the lines it replaced are gone.
func TestEmailTalentTeachesFilingPolicy(t *testing.T) {
	text := emailTalentText(t)
	present := []struct {
		name   string
		talent string
		want   string
	}{
		{"entry shows filing fields", "email", "shows its `junk_folder`, the `move_into` folders `email_move` accepts there, and any `filing_note`"},
		{"whose mailbox files spam by role", "email", "which `email_move` files with `destination_role: \"junk\"`"},
		{"whose mailbox names move_into", "email", "the entry's `move_into` lists the only folders mail may move into"},
		{"folders bullet offers the role", "email", "`email_move` takes `destination_role` (such as `junk` or `trash`) and Go finds the folder"},
		{"read result lists hidden_content", "email_triage", "body_source, hidden_content, body_truncated"},
		{"hidden text is withheld and counted", "email_triage", "that text is withheld from the body and `hidden_content` `{present: true, chars}` says so"},
		{"hidden_content alone is not abuse", "email_triage", "so `hidden_content` alone is not a sign of abuse"},
		{"mark refuses the drafts folder", "email_organize", "The account's drafts folder is never the `folder` of an `email_mark` call."},
		{"move refuses drafts both ways", "email_organize", "The account's drafts folder is neither a destination nor a source."},
		{"neither or both refused", "email_organize", "A call with neither or both is refused and moves nothing"},
		{"move_into limits destinations", "email_organize", "those folders are the only destinations `email_move` accepts there"},
		{"operator default is junk alone", "email_organize", "unless the operator configured more it holds the junk folder alone"},
		{"move_into holds when attended", "email_organize", "so it holds in the operator's own turn too"},
		{"inbox return always allowed", "email_organize", "Moving mail back to INBOX out of a `move_into` folder is always allowed"},
		{"result carries moved", "email_organize", "`moved` lists each message that moved as `{uid, destination_uid, message_id, from, trust_zone}`"},
		{"result action refused", "email_organize", "`action` is `moved`, or `refused` when the junk guard refused every message and nothing moved"},
		{"spam section", "email_organize", "## Obvious spam, and nothing else"},
		{"spam needs an unmatched sender", "email_organize", "its sender's `contact_status` is `unmatched`"},
		{"spam needs a hard sign", "email_organize", "it gives itself away with a hard sign"},
		{"bulk alone is not spam", "email_organize", "Bulk or automated alone is never spam."},
		{"hidden_content is evidence", "email_organize", "`hidden_content` on a read result is evidence that the sender hid text"},
		{"spam moves by role", "email_organize", "\"destination_role\": \"junk\""},
		{"guard lists refused", "email_organize", "under `refused` as `{uid, from, trust_zone, reason, recovery}`"},
		{"guard recovery flags", "email_organize", "flag it with `email_mark` flag `flagged` if it needs the operator"},
		{"guard recovery escalates", "email_organize", "bring it to them with `request_core_attention` if it cannot wait"},
		{"guard refusal not retried", "email_organize", "Do not retry the move, by role or by name"},
		{"no guard when attended", "email_organize", "In the operator's own turn there is no guard"},
		{"undo recipe", "email_organize", "`email_move {account, folder: <junk_folder from the account's entry>, uids: <destination_uids from the move result>, destination_role: \"inbox\"}`"},
		{"undo without destination uids", "email_organize", "taking each `message_id` from the result's `moved` list"},
		{"trash by role", "email_organize", "`destination_role: \"trash\"` resolves the account's trash folder"},
		{"rendering names the idioms Go recognises", "email_triage", "The rendering leaves out text the HTML's own markup hides with an inline idiom Go recognises"},
		{"hidden text is what a reader would not see", "email_triage", "`hidden_content` is evidence that the sender put text in the message that a person reading it would not see."},
		{"absent hidden_content proves nothing", "email_triage", "its absence does not show that the body is what a reader saw"},
		{"guard covers addresses it cannot fully check", "email_organize", "when one of them is, or may be, at such a zone"},
		{"refused trust_zone is the judged zone", "email_organize", "Its `trust_zone` is the zone the guard judged"},
		{"a refusal waits for the operator", "email_organize", "a refused message waits for the operator, not for another attempt"},
		{"undo into an unlisted folder is refused", "email_organize", "A move back into a folder the account's `move_into` does not list is refused like any other"},
		{"operator entry shows move_into when limited", "email_organize", "An operator mailbox shows it whenever the list is limited"},
		{"operator entry always shows junk_folder", "email", "an operator mailbox shows its `junk_folder` even when it allows every folder"},
	}
	for _, tt := range present {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(text[tt.talent], tt.want) {
				t.Errorf("talent %s must contain %q", tt.talent, tt.want)
			}
		})
	}

	absent := []struct {
		name string
		gone string
	}{
		{"destination alone is required", "`destination` is required"},
		{"spam filed by reading the role off the listing", "the folder whose role is `junk`"},
		{"trash filed by reading the role off the listing", "block lists with role `trash`, by its exact name"},
		{"undo only by destination", "`destination` set explicitly to the original `source_folder`"},
		{"rendered body claimed faithful", "A rendered HTML body is what a person reading the message would see"},
		{"hidden text gap told backwards", "showed a reader something other than what reached you"},
		{"guard claimed to answer a repeat the same way", "the guard answers the same way every time"},
		{"operator entry claimed to always show move_into", "An operator mailbox always shows it"},
	}
	for _, tt := range absent {
		t.Run(tt.name, func(t *testing.T) {
			for name, body := range text {
				if strings.Contains(body, tt.gone) {
					t.Errorf("talent %s still contains %q", name, tt.gone)
				}
			}
		})
	}
}

// TestEmailTalentTeachesDraftsOnlyGate pins the slice 3 teaching: the
// relaxed draft gate and what it still refuses, writing a draft for the
// operator to review, the personally addressed list-mail rule and its
// two refusals, and the refusal when no folder has the drafts role. It
// also pins that draft: true is not taught as a way past the gate.
func TestEmailTalentTeachesDraftsOnlyGate(t *testing.T) {
	text := emailTalentText(t)
	present := []struct {
		name   string
		talent string
		want   string
	}{
		{"trailhead names the relaxed gate", "email", "where the entry shows `draft_gate: \"relaxed\"`, the operator sends every draft by hand"},
		{"trailhead keeps the refusals", "email", "An `automated` mailbox, a failed lookup, and any recipient the account's recipient-domain rules refuse are refused there too"},
		{"draft_only by bare address", "email_respond", "The draft names such a recipient by bare address, dropping any display name"},
		{"report what the directory holds", "email_respond", "what the directory holds for each `draft_only` recipient"},
		{"junk stays refused", "email_respond", "mail marked `Precedence: junk`, which classic autoresponders put on their replies, whatever list header sits beside it"},
		{"list fields alone stay refused", "email_respond", "mail whose only list marks are fields such as `List-Unsubscribe`, which a sender adds to its own mailings"},
		{"judge automatic replies by content", "email_respond", "read the body, and do not answer an automatic reply"},
		{"requested draft keeps its flag", "email_respond", "If you asked for the draft with `draft: true`, do not resend it without the flag"},
		{"trailhead list-mail exception", "email", "list mail that names the account's own address in its `to` or `cc` can be answered as a draft"},
		{"triage list-mail exception", "email_triage", "apart from the one list-mail case that `email_respond` drafts"},
		{"respond section", "email_respond", "## A drafts-only account"},
		{"anyone a person could answer", "email_respond", "a draft may go to anyone a person could answer"},
		{"zone-only refusals draft", "email_respond", "A recipient refused only for its zone"},
		{"recipient gating", "email_respond", "It shows `gating: \"draft_only\"` in `decision.recipients`"},
		{"entry refuses nothing", "email_respond", "`drafts_for` lists every zone and its `refuses` is empty"},
		{"what stays refused", "email_respond", "an `automated` mailbox, a `lookup_failed` address (retry later), an address that does not parse, a domain the account's recipient-domain rules deny or leave out, and more than 50 recipients"},
		{"still all-or-nothing", "email_respond", "one of those beside a stranger refuses the whole message"},
		{"draft true does not relax", "email_respond", "`draft: true` does not relax it anywhere"},
		{"draft true holds, never admits", "email_respond", "It holds, and it never admits"},
		{"write it for review", "email_respond", "A draft there is the operator's to send, so write it for them to review."},
		{"finished message, not a note", "email_respond", "write the finished message, never a note about one"},
		{"review context goes in the report", "email_respond", "put what they need to know before sending in your report to them instead"},
		{"list-mail rule", "email_respond", "whose own `to` or `cc` names the account's own address"},
		{"list-mail route", "email_respond", "`decision.route` `personally_addressed_list_reply`, `draft: true` or not"},
		{"list-only stays refused", "email_respond", "list mail that reached the account only through a list address"},
		{"aliases do not count", "email_respond", "an alias or a plus address does not count as named"},
		{"auto_submitted stays refused", "email_respond", "anything `auto_submitted`, such as an automatic reply, a bounce, or a notification"},
		{"reply_to may be the list", "email_respond", "a list that sets its own address as the `reply_to` puts the whole list in the draft's `to`"},
		{"drafted route list", "email_respond", "or `personally_addressed_list_reply` (list mail addressed to the account"},
		{"refused route list", "email_respond", "`no_drafts_folder` (the message would have been drafted, but no folder on the account has the drafts role"},
		{"no drafts folder section", "email_respond", "## When no folder holds drafts"},
		{"resolution order", "email_respond", "the account's configured `drafts_folder`, else the folder the server marks as drafts, found in the cached folder listing or by one fresh listing"},
		{"no guessed name", "email_respond", "Go never guesses a name."},
		{"nothing sent instead", "email_respond", "nothing is sent in its place, on any delivery mode"},
		{"operator configures drafts_folder", "email_respond", "Only the operator can close the gap, by configuring `drafts_folder`"},
	}
	for _, tt := range present {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(text[tt.talent], tt.want) {
				t.Errorf("talent %s must contain %q", tt.talent, tt.want)
			}
		})
	}

	absent := []struct {
		name string
		gone string
	}{
		{"drafts folder by a site name", "held in Drafts"},
		{"requested draft by a site name", "holds the message in Drafts"},
	}
	for _, tt := range absent {
		t.Run(tt.name, func(t *testing.T) {
			for name, body := range text {
				if strings.Contains(body, tt.gone) {
					t.Errorf("talent %s still contains %q", name, tt.gone)
				}
			}
		})
	}
}

// TestEmailTalentTeachesDraftEditPath pins the slice 4 teaching: the
// email_drafts leaf and its edit path (find, read beside the original,
// revise the body for accuracy and tone, or withdraw), the ownership
// rule, gone and held as hands off, the two-pass tier, and the one-draft
// refusals in email_respond. It also pins that nothing teaches a review
// or approve verb, which the operator ruled out.
func TestEmailTalentTeachesDraftEditPath(t *testing.T) {
	text := emailTalentText(t)
	present := []struct {
		name   string
		talent string
		want   string
	}{
		{"trailhead routes to the leaf", "email", "activate `email_drafts` beside `email`"},
		{"drafted disposition names draft_id", "email", "the result's `draft_id` is how you change it later"},
		{"drafted mail reversible while yours", "email", "while it is still yours, the `email_drafts` tools revise its body or withdraw it"},
		{"operator drafts folder marks only yours", "email", "only your own drafts carry `thane_draft`"},
		{"operator reply comes first", "email", "refuses to draft another answer to it (`operator_reply_started`"},
		{"list row shape", "email_triage", "size, thane_draft}]}"},
		{"annotation needs uid and message-id", "email_triage", "only when both its UID and its Message-ID match what Thane recorded"},
		{"unannotated row is not yours", "email_triage", "A row without `thane_draft` is not one of your open drafts"},
		{"result shape carries draft_id", "email_respond", "drafts_folder, draft_uid, draft_id, signed"},
		{"drafted bullet: revise, never redraft", "email_respond", "Never resend it, and never write a second draft to change it; revise the one you have."},
		{"refused routes list draft_open", "email_respond", "`draft_open` (one of your drafts already answers the message; revise that one)"},
		{"refused routes list operator_reply_started", "email_respond", "`operator_reply_started` (on an operator mailbox, a reply that is not one of your open drafts is already there, most likely the operator's; leave it to them)"},
		{"refused routes list draft_limit", "email_respond", "`draft_limit` (the account already has 200 of your drafts open"},
		{"a sent reply names the open draft", "email_respond", "the result's `note` names that `draft_id`: withdraw it with `email_draft_withdraw`"},
		{"gone lists marked_deleted", "email_drafts", "`marked_deleted` when their client flagged it"},
		{"one draft section", "email_respond", "## One draft per message"},
		{"draft_open route", "email_respond", "`email_reply` refuses it with `decision.route` `draft_open`"},
		{"draft_open recovery", "email_respond", "Change the existing draft with `email_draft_revise` instead"},
		{"withdraw before a fresh draft", "email_respond", "withdraw the old one with `email_draft_withdraw` first and then reply"},
		{"operator_reply_started route", "email_respond", "`email_reply` refuses with `decision.route` `operator_reply_started`"},
		{"leave the operator's draft", "email_respond", "Leave their draft alone and write no second one beside it"},
		{"draft true clears neither", "email_respond", "`draft: true` does not clear either refusal"},
		{"withdraw is not a move", "email_organize", "Taking back one of your own drafts is `email_draft_withdraw`'s job"},
		{"leaf heading", "email_drafts", "# Your drafts in flight"},
		{"in flight until sent or discarded", "email_drafts", "A draft you write is in flight until the operator sends it or discards it from their own client."},
		{"operator sends every draft", "email_drafts", "The operator reads and sends every draft by hand"},
		{"carry email too", "email_drafts", "Carry `email` beside `email_drafts`."},
		{"ownership is proven", "email_drafts", "A draft is yours only while Go can prove it"},
		{"operator edit makes it theirs", "email_drafts", "from then on the draft is theirs"},
		{"never an operator's draft", "email_drafts", "A draft the operator wrote is never yours, whatever it says or answers."},
		{"find with email_drafts", "email_drafts", "`email_drafts` lists the ledger"},
		{"read beside the original", "email_drafts", "Read one with `email_draft_get`"},
		{"read both before changing", "email_drafts", "Read both bodies before you change a word"},
		{"revise for accuracy and tone", "email_drafts", "Revise for two things. Accuracy:"},
		{"tone is writes_as and voice", "email_drafts", "goes out under the account's `writes_as` and in its `voice`"},
		{"body only", "email_drafts", "Only the body changes. Recipients, subject, and threading stay exactly as they were drafted"},
		{"wrong audience is withdrawn", "email_drafts", "a draft that needs a different audience or subject is withdrawn, not revised"},
		{"body replaces everything", "email_drafts", "`body` is markdown and replaces the whole old body"},
		{"key on draft_id", "email_drafts", "Keep track of a draft by its `draft_id`, never by a UID you saw earlier."},
		{"no approve step", "email_drafts", "there is no approve step"},
		{"withdraw to trash", "email_drafts", "`email_draft_withdraw` moves a draft to the account's trash folder"},
		{"withdraw ignores move_into", "email_drafts", "the account's `move_into` does not apply"},
		{"no trash folder refusal", "email_drafts", "the refusal (`no_trash_folder`) names the gap"},
		{"gone and held section", "email_drafts", "## Gone and held: hands off"},
		{"held is operator takeover", "email_drafts", "`held`: the operator has taken the draft over."},
		{"leave it", "email_drafts", "Either way, leave it."},
		{"never recreate", "email_drafts", "Never write it again as a new draft and never try to restore your version"},
		{"tier section", "email_drafts", "## A first draft, then an edit"},
		{"local first pass", "email_drafts", "A first pass, often a free local model"},
		{"editing loop carries both tags", "email_drafts", "a loop carrying `email` and `email_drafts`"},
		{"first pass writes finished mail", "email_drafts", "If you are the first pass, write the finished message anyway"},
	}
	for _, tt := range present {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(text[tt.talent], tt.want) {
				t.Errorf("talent %s must contain %q", tt.talent, tt.want)
			}
		})
	}

	absent := []struct {
		name string
		gone string
	}{
		{"old drafted bullet without the edit path", "do not resend it, and do not try to send it"},
		{"design's review verb", "email_draft_review"},
		{"design's held stage", "held_by_operator"},
		{"review stage", "reviewed_by"},
	}
	for _, tt := range absent {
		t.Run(tt.name, func(t *testing.T) {
			for name, body := range text {
				if strings.Contains(body, tt.gone) {
					t.Errorf("talent %s still contains %q", name, tt.gone)
				}
			}
		})
	}
}

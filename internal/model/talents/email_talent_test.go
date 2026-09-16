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
		{"entry shows filing fields", "email", "shows its `junk_folder`, the `move_into` folders `email_move` accepts there in a turn the operator is not present for, and any `filing_note`"},
		{"whose mailbox lifts move_into when attended", "email", "In the operator's own turn `move_into` does not apply, because they decide where their mail goes"},
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
		{"move_into binds only unattended turns", "email_organize", "**Each account limits where its mail may go when the operator is not present.**"},
		{"move_into lifted when attended", "email_organize", "In the operator's own turn (`attended: true`) `move_into` does not apply"},
		{"attended move still spares drafts", "email_organize", "any folder the account has but the drafts folder, whatever `move_into` lists"},
		{"refused move names the operator's way through", "email_organize", "they can move it themselves or ask for it in their own message through Thane's native API or console, or in a channel conversation bound to their contact, where it goes through"},
		{"refused move says voice is refused the same way", "email_organize", "asking again by voice or through Home Assistant is refused the same way"},
		{"move_into lists the shim as unattended", "email_organize", "or a request through the Ollama-compatible shim, such as Home Assistant voice, even when the operator is the one speaking"},
		{"undo names the operator's way through", "email_organize", "ask for it in their own message through Thane's native API or in a channel conversation bound to their contact, where the move goes through"},
		{"send lists the shim as unattended", "email", "and a request through the Ollama-compatible shim, such as Home Assistant voice, even one the operator speaks, are all unattended"},
		{"question one qualifies clearing", "email", "never by clearing mail away on your own (in their own turn, move what they ask to where they ask)"},
		{"whose mailbox qualifies clearing", "email", "so you help by marking, never by clearing mail away on your own."},
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
		{"undo into an unlisted folder is refused", "email_organize", "In a turn the operator is not present for, a move back into a folder the account's `move_into` does not list is refused like any other"},
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
		{"move_into claimed to hold when attended", "so it holds in the operator's own turn too"},
		{"move_into claimed to refuse in every turn", "any other move is refused in every turn"},
		{"question one forbids clearing even when asked", "never by clearing mail away, and what you draft"},
		{"whose mailbox forbids clearing even when asked", "never by clearing mail away. Reads"},
		{"recovery points at the conversation the refusal came in on", "or ask for it in their own conversation."},
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

// TestEmailTalentTeachesTwoPasses pins the slice 5 teaching: the wake
// loop and the review loop in the entry, that a bound loop's narrowed
// block cannot say which review loops run (loop_status can), what
// queues review work and that nothing records it, escalate versus
// flag, the review pass's
// queue procedure with its queue_ack and queue_defer discipline, and the
// border lines that keep a loop from triaging mail twice. It also pins
// that nothing teaches a review marker, a local-only requirement, or a
// queue tool the review loop does not carry.
func TestEmailTalentTeachesTwoPasses(t *testing.T) {
	text := emailTalentText(t)
	present := []struct {
		name   string
		talent string
		want   string
	}{
		{"shape bullet names escalate", "email", "**You want a more capable pass to look at a message** — call `email_escalate`"},
		{"entry shows routing", "email", "While this site polls for new mail, every entry shows `wake_loop`, the loop the account's new mail wakes; an entry also shows `review_loop` with `pending_review`"},
		{"wake loop defaults named", "email", "Unless the operator chose another, it is `email-owner-triage`, the triage pass, on an operator mailbox, and `email-default-handler`, the default handler, on every other account."},
		{"read the wake loop off the entry", "email", "Every entry shows `wake_loop` while this site polls for new mail, so read the loop from it rather than working it out from `owner`."},
		{"no wake_loop means no polling", "email", "An entry without `wake_loop` is on a site that does not poll for mail, and no loop sees new mail there."},
		{"no review_loop means no review pass there", "email", "An entry without `review_loop` has no review pass on that account."},
		{"review loops are read from loop_status", "email", "Do not judge from the entries whether a review loop runs on this site, because a loop bound to one account sees only that account's entry: `loop_status` (in the `loops` tag) lists the loops that are running, and the built-in review loop, `email-draft-review`, is among them when any account names it as its `review_loop`."},
		{"bound block narrows", "email", "In a loop bound to one account it lists only that account, and its entry shows `bound: true`: the site's other accounts are hidden from you, not missing."},
		{"wake metadata flags", "email", "so `\\Flagged` there is someone else's flag: the message is already flagged"},
		{"review wake is unattended", "email", "A poller wake, a review wake, a scheduled loop"},
		{"passes section", "email", "## Two passes over new mail"},
		{"routing is configuration", "email", "is the operator's configuration, and no tool changes it"},
		{"triage does one thing", "email", "The triage pass does exactly one thing per message"},
		{"review woken by queued work", "email", "A review loop is woken by queued work, never by the mail itself or by a timer, and only while work waits."},
		{"drafts queue themselves", "email", "by any loop but the review loop itself, as `draft:<account>:<draft_id>`"},
		{"escalations queue by message id", "email", "as `message:<account>:<message_id>`, keyed by Message-ID rather than UID"},
		{"queueing coalesces", "email", "replaces the item already waiting instead of adding a second"},
		{"pending_review is a snapshot", "email", "`pending_review_as_of` says when it was counted"},
		{"no review record", "email", "no record that a draft was reviewed or approved"},
		{"escalate or flag section", "email", "### Escalate or flag"},
		{"choose by who acts", "email", "Choose by who has to act"},
		{"operator decisions are flagged", "email", "A more capable model has none of those either, so escalating such a message only delays the flag."},
		{"no review_loop means flag", "email", "so `email_escalate` is refused there with nothing queued, and the refusal gives the `email_mark` call to make instead"},
		{"drafted messages need no escalation", "email", "A message you just drafted an answer to is already queued through its draft."},
		{"escalate changes nothing", "email", "Escalating changes nothing in the mailbox"},
		{"null count is not zero", "email", "is null when it could not be counted, which is not zero"},
		{"no second triage loop", "email", "Do not build one to triage new mail message by message"},
		{"a drafted result may be queued", "email_respond", "is also queued for that review pass, which may revise or withdraw it"},
		{"first pass hands nothing over", "email_drafts", "so there is nothing to hand over; `email_escalate` is for a message you did not answer"},
		{"queue section", "email_drafts", "### When your work comes from a queue"},
		{"queue, not the ledger", "email_drafts", "Take work only from the queue, never from `email_drafts`"},
		{"pull once", "email_drafts", "Call `queue_pull` once, with a `limit` you can finish in this wake, at most 10."},
		{"message subject procedure", "email_drafts", "search INBOX the same way when that folder no longer holds it"},
		{"review drafts are not re-queued", "email_drafts", "A draft you write here is not queued back to you, so write it finished."},
		{"ack once the outcome is written", "email_drafts", "Call `queue_ack` for each subject once its outcome is written"},
		{"never ack early", "email_drafts", "never acknowledge before the call that settles it has answered"},
		{"never leave settled work", "email_drafts", "never leave a settled item unacknowledged"},
		{"defer only on connection failure", "email_drafts", "Call `queue_defer` instead only when the account's mailbox could not be reached at all"},
		{"doubt is not deferral", "email_drafts", "Doubt is not a reason to defer."},
		{"retained_newer", "email_drafts", "When `queue_ack` answers `retained_newer`"},
		{"answered mail is not drafted", "email", "A message whose `flags` include `\\Answered` has already been replied"},
		{"queued message already answered", "email_drafts", "If its `flags` include `\\Answered`, someone has already"},
		{"summary names the message subject", "email_drafts", "its `message_subject` (the"},
		{"stop after the batch", "email_drafts", "Stop after this batch."},
		{"ledger path remains", "email_drafts", "### When you work from the ledger"},
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
		{"design's router factor", "local_required"},
		{"design's draft stages", "stage: staged"},
		{"design's ready stage", "stage: ready"},
		{"a queue tool the review loop lacks", "queue_enqueue"},
		{"curators are not named", "curator"},
		{"the old coupling paragraph", "the coupling is a queue"},
		{"wake_loop hidden at the owner's default", "only when the operator chose a loop other than the owner's default"},
		{"wake_loop shown only off the default", "wakes a loop other than its owner's default"},
		{"review loop judged from the visible entries", "is added only on a site where some entry shows"},
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

// TestEmailTalentTeachesLabels pins the slice 6 teaching and the arc's
// synthesis: the five-question decision frame in the email trailhead,
// what labels are, that a label with apply is Go's, how a row tells a
// label's flag from an attention flag, that flagging clears only Thane's
// colour, the keywords verdict, answered refused on a drafts account,
// email_mark label and its refusals, and email_search's label and
// unflagged filters and undeclared-argument refusal. It also pins that
// no mail client and no site keyword is taught: labels are a portable
// vocabulary, and a keyword learned by example is one the model searches
// for on a site that declares another.
func TestEmailTalentTeachesLabels(t *testing.T) {
	text := emailTalentText(t)
	present := []struct {
		name   string
		talent string
		want   string
	}{
		{"frame section", "email", "## Five questions before you act"},
		{"frame asks whose mailbox", "email", "**Whose mailbox is it?**"},
		{"frame asks where mail may go", "email", "**Where may its mail go?**"},
		{"frame asks draft, flag, or escalate", "email", "**Draft, flag, or escalate?**"},
		{"frame asks whose draft", "email", "**Is this draft mine?**"},
		{"frame asks what a mark says", "email", "**What does this mark say?**"},
		{"frame says where care goes", "email", "What Go cannot check is the judgment"},
		{"entry shows labels", "email", "It shows `labels` and `keywords` when the account's mailbox carries labels"},
		{"labels section", "email", "## Labels"},
		{"entry label shape", "email", "`labels` `[{label, meaning, shows_as, apply}]`"},
		{"no labels means none written or taken", "email", "An entry without `labels` means no label is written on that account, and `email_mark` and `email_search` refuse one there"},
		{"undeclared keyword is no label", "email", "A keyword in a message's `flags` that the entry's labels do not declare is the operator's or their client's, not a label"},
		{"apply labels are Go's", "email", "**A label with `apply` is Go's, never yours.**"},
		{"contact_matched from the directory", "email", "Under `contact_matched`, when new mail reaches INBOX from a sender whose `contact_status` is `matched`"},
		{"the label claims only a match", "email", "it says nothing about who wrote the message or whether it matters"},
		{"wake flags carry no Thane mark", "email", "without any mark that is still Thane's for a label"},
		{"a mark is Thane's only where Thane marked it", "email", "**A mark is Thane's only on the message Thane marked, in the folder Thane marked it in.**"},
		{"an operator's move ends the claim", "email", "When the operator moves the message, or a second copy of it arrives under the same Message-ID, the marks that copy carries are the operator's"},
		{"email_move keeps the claim with COPYUID", "email", "`email_move` keeps Go's record with the message when its result shows `destination_uids_known: true` and the call moved no other copy under the same Message-ID"},
		{"unproven marks are the operator's", "email", "when Go cannot prove a mark is Thane's, the mark is the operator's"},
		{"a moved or second copy has no flag_label", "email_triage", "and neither does a flag on a second copy of the message, or on one the operator moved after Thane marked it"},
		{"a label on another copy is refused", "email_organize", "and on a copy of a message other than the one Thane's record describes"},
		{"flag_label is the label's mark", "email", "the label's mark, not a request for the operator's attention"},
		{"any other flag is attention", "email", "Any other flag, whatever its colour, is someone's attention flag"},
		{"flagging clears Thane's colour", "email", "Go clears the colour Thane wrote, so the flag reads plain, as the operator's attention flag"},
		{"never another's colour", "email", "A colour anyone else set is never cleared."},
		{"unflagging takes Thane's colour", "email", "with the colour keywords Thane wrote for it"},
		{"keywords verdict", "email", "`keywords: \"permanent\"` means it does"},
		{"no keyword without permanence", "email", "so Go writes no keyword there, a label's colour keywords included"},
		{"absent verdict proves nothing", "email", "its absence says nothing about the server"},
		{"a draft is not an answer", "email", "which is why `email_mark` refuses `answered` on an account whose `delivery` is `drafts`"},
		{"a missing answered proves nothing", "email", "a missing `\\Answered` does not show that a draft you wrote went unsent"},
		{"contact_save refusal is outside the operator's message", "email", "outside the operator's own message Go refuses the additions that would lend a higher zone"},
		{"row labels", "email_triage", "labels, flag_label, size, thane_draft}]}"},
		{"read labels", "email_triage", "date, flags, labels, flag_label, thane_draft, size"},
		{"flag_label comes from Go's record", "email_triage", "Go reads that from its own record of what it set, never from the colour"},
		{"colour keywords named", "email_triage", "the colour keywords `$MailFlagBit0` to `$MailFlagBit2` among them"},
		{"raw flags stay", "email_triage", "The raw `flags` stay beside both."},
		{"search label and unflagged", "email_triage", "`flagged` or `unflagged` (with or without `\\Flagged`, whoever set it), `label`"},
		{"undeclared search argument refused", "email_triage", "so is an argument `email_search` does not take"},
		{"flagged search finds label flags", "email_triage", "`flagged: true` finds a label's flag as well as an attention flag"},
		{"apply a label section", "email_organize", "## Apply a label"},
		{"a label the account lacks is refused", "email_organize", "A label the account's entry does not list is refused"},
		{"colour refused over any other flag", "email_organize", "A label with a colour is refused on a message that already carries any flag but that label's own"},
		{"removal takes only Thane's marks", "email_organize", "removes only the keyword and flag Thane recorded setting"},
		{"keywords refusal quotes the server", "email_organize", "the refusal quotes the server, and the entry's `keywords` shows INBOX's answer"},
		{"answered refused on drafts", "email_organize", "On an account whose `delivery` is `drafts`, `email_mark` refuses `answered`, adding or removing"},
		{"flag result lists cleared colours", "email_organize", "uids_affected, uids_not_found, thane_color_cleared, note}"},
		{"refused label not retried", "email_organize", "A refused message refuses the same way again, so do not retry it"},
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
		{"a mail client by name", "Apple"},
		{"a site's keyword", "thane-contact"},
		{"a flag read from its colour", "flag_color"},
		{"a claim about what a mail client marks", "marks the message answered"},
		{"the self-referencing escalate cross-reference", "see \"Escalate or flag\" above"},
		{"the trailhead's zone-by-zone delivery recital", "Under the default `by_trust_zone` delivery"},
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

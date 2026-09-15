package email

import "strings"

// email_mark's description and schema grow with email.labels: the label
// parameter and its rules appear only when a label the model applies is
// declared, and the flag-colour rules and their result keys only when a
// label has a colour, so a site without labels reads the tool exactly as
// before.

// markDescription is email_mark's model-facing description.
func (t *Tools) markDescription() string {
	labels := t.labels()
	d := "Add or remove a flag on messages in one folder: seen, flagged, or answered. Provide uids (array of integers) or uid (single integer) " +
		"from an email_list or email_search result in the same account and folder; add defaults to true. Returns JSON " +
		"{action: flag_added|flag_removed, account, folder, flag, uids_affected, uids_not_found}. " +
		"A UID under uids_not_found no longer exists in that folder (moved or deleted) — list again rather than retrying. An account whose access is read refuses this tool. " +
		"On an account whose Email Accounts entry shows owner: operator, adding seen is refused unless the operator is present for this turn, because unread is how the operator sees what is new; flagged is how to mark what needs them. " +
		"On an account whose delivery is drafts, answered is refused, added or removed, and nothing changes: what Thane writes there is a draft, a draft is not an answer, and Thane cannot know when the operator sends one. " +
		"The account's drafts folder is refused as folder and nothing changes: it holds drafts waiting for the operator to send or discard. When listing the account's folders fails while finding that folder, the call is refused and nothing changes. " +
		"A call takes at most 100 UIDs."
	if labels.hasColors() {
		d += " The flag result also carries thane_color_cleared and note. Adding flagged to a message whose flag_label names a label clears the colour Thane wrote for that label, so the flag reads as a plain flag, the operator's attention flag; removing flagged from such a message removes the colour's keywords with the flag. " +
			"Either way the message is listed under thane_color_cleared, and a colour anyone else set stays. note says so if clearing failed after the flag was set; if it fails before a removal, the call is refused and the flag stays."
	}
	if markable := labels.markable(); len(markable) > 0 {
		d += " Pass label instead of flag to apply or remove one of the labels the account's Email Accounts entry lists without apply (" + strings.Join(markable, ", ") + "): its keyword, and its flag colour on a message without a flag. A label the account's entry does not list is refused, and nothing changes. " +
			"A label with a colour is refused on a message that already carries any flag but that label's own, the operator's or another label's; that message is listed under refused with the reason, and nothing changes on it. " +
			"Removing a label (add: false) removes only the keyword and flag Thane recorded setting; anything else the message carries for the label is the operator's, stays, and is listed under refused. " +
			"A label written with keywords is refused, and nothing changes, in a folder whose PERMANENTFLAGS do not keep keywords (the entry's keywords shows INBOX's verdict; anything but permanent refuses). A label whose entry shows apply is Go's and is refused here. " +
			"A label call returns JSON {action: label_added|label_removed|refused, account, folder, label, uids_affected, uids_not_found, refused:[{uid, reason}]}; action is refused only when every message found was refused."
	}
	return d
}

// markParameters is email_mark's schema. flag is required unless a
// label the model applies is declared; the handler then requires exactly
// one of flag and label.
func (t *Tools) markParameters(uidsParam, uidParam map[string]any) map[string]any {
	props := map[string]any{
		"uids": uidsParam,
		"uid":  uidParam,
		"flag": map[string]any{
			"type":        "string",
			"enum":        ValidFlagNames(),
			"description": "Flag to add or remove.",
		},
		"add": map[string]any{
			"type":        "boolean",
			"description": "true adds the flag, false removes it (default: true).",
		},
		"folder":  folderParameter("Folder containing the messages."),
		"account": accountParameter(),
	}
	markable := t.labels().markable()
	if len(markable) == 0 {
		return map[string]any{"type": "object", "properties": props, "required": []string{"flag"}}
	}
	props["flag"].(map[string]any)["description"] = "Flag to add or remove. Pass flag or label, not both."
	props["add"].(map[string]any)["description"] = "true adds the flag or label, false removes it (default: true)."
	props["label"] = map[string]any{
		"type":        "string",
		"enum":        markable,
		"description": "Label to apply or remove instead of a flag, from the labels the account's Email Accounts entry lists without apply. Pass flag or label, not both.",
	}
	return map[string]any{"type": "object", "properties": props}
}

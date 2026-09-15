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
var siteFolderName = regexp.MustCompile(`\b(Archive|Trash|Junk|Spam)\b|\[Gmail\]|All Mail`)

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
	for _, name := range []string{"email", "email_triage", "email_respond", "email_organize"} {
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
		{"destination required", "email_organize", "`destination` is required"},
		{"folder is the source", "email_organize", "`folder` is the source (default INBOX) and never the target."},
		{"move recorded with uids", "email_organize", "records each move with both UID lists"},
		{"move record is capped", "email_organize", "at most 10 of each with the rest counted"},
		{"a draft goes out under the account's from", "email_respond", "under the account's own From, which is the operator's name only on an operator mailbox"},
		{"undo names the source explicitly", "email_organize", "`destination` set explicitly to the original `source_folder`"},
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

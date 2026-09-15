package config

import (
	"fmt"
	"slices"
	"strings"
)

// Filing policy: where email_move may put mail on one account. Config
// never names a folder a site happens to use. An entry is either a
// special-use role, which the email package resolves against the
// account's configuration and the server's folder listing, or an exact
// folder name the operator wrote.

// EmailMoveIntoAny is the mailbox.move_into entry that allows every
// folder. It must be the only entry.
const EmailMoveIntoAny = "*"

// EmailMoveIntoRolePrefix marks a mailbox.move_into entry that names a
// special-use role rather than a folder, as in "role:junk".
const EmailMoveIntoRolePrefix = "role:"

// maxEmailFilingNoteBytes bounds mailbox.filing_note, which is rendered
// into every turn that wears the email tag.
const maxEmailFilingNoteBytes = 300

// emailFolderRoles is the closed set of special-use roles an email
// folder listing reports: RFC 6154's attributes plus inbox, which is a
// role by name. The email package's FolderRole constants must match it;
// a test there pins the two together.
var emailFolderRoles = []string{"inbox", "drafts", "sent", "trash", "junk", "archive", "all", "flagged", "important"}

// EmailFolderRoles returns the special-use roles a mailbox.move_into
// entry may name after role:, drafts included.
func EmailFolderRoles() []string {
	return slices.Clone(emailFolderRoles)
}

// MoveIntoTokens returns the account's effective mailbox.move_into:
// the configured entries, trimmed, or the owner's default when none are
// configured. The default is [role:junk] on an operator mailbox, so
// only spam leaves the operator's INBOX, and ["*"] otherwise.
func (a EmailAccountConfig) MoveIntoTokens() []string {
	if len(a.Mailbox.MoveInto) == 0 {
		if a.OperatorMailbox() {
			return []string{EmailMoveIntoRolePrefix + "junk"}
		}
		return []string{EmailMoveIntoAny}
	}
	out := make([]string, len(a.Mailbox.MoveInto))
	for i, token := range a.Mailbox.MoveInto {
		out[i] = strings.TrimSpace(token)
	}
	return out
}

// MovesAnywhere reports whether email_move may file into every folder
// of the account: its effective move_into is ["*"].
func (a EmailAccountConfig) MovesAnywhere() bool {
	tokens := a.MoveIntoTokens()
	return len(tokens) == 1 && tokens[0] == EmailMoveIntoAny
}

// validateFiling checks the account's move_into entries and its filing
// note. A role must be one a folder listing can report, the drafts
// folder can never be listed, and "*" cannot be combined with anything,
// because a list that allows everything and also names folders says
// two things at once. The drafts name is compared without case, as the
// runtime refusal compares it: some servers fold the case of every
// mailbox name, so a case variant can reach the drafts folder.
func (a EmailAccountConfig) validateFiling(i int) error {
	where := fmt.Sprintf("email.accounts[%d] (%s)", i, a.Name)
	drafts := strings.TrimSpace(a.DraftsFolder)
	for j, raw := range a.Mailbox.MoveInto {
		token := strings.TrimSpace(raw)
		role, isRole := strings.CutPrefix(token, EmailMoveIntoRolePrefix)
		switch {
		case token == "":
			return fmt.Errorf("%s: mailbox.move_into[%d] is empty; write role:<role> or an exact folder name", where, j)
		case token == EmailMoveIntoAny:
			if len(a.Mailbox.MoveInto) > 1 {
				return fmt.Errorf("%s: mailbox.move_into[%d] is %q, which allows every folder and cannot be combined with other entries", where, j, EmailMoveIntoAny)
			}
		case isRole && role == "drafts", !isRole && drafts != "" && strings.EqualFold(token, drafts):
			return fmt.Errorf("%s: mailbox.move_into[%d] %q names the drafts folder, which is never a move destination", where, j, token)
		case isRole && !slices.Contains(emailFolderRoles, role):
			return fmt.Errorf("%s: mailbox.move_into[%d] %q names no special-use role; use role: with one of %s, or an exact folder name", where, j, token, strings.Join(destinationRoles(), ", "))
		}
	}
	if n := len(a.Mailbox.FilingNote); n > maxEmailFilingNoteBytes {
		return fmt.Errorf("%s: mailbox.filing_note is %d bytes, over the %d-byte limit; keep it to one sentence", where, n, maxEmailFilingNoteBytes)
	}
	return nil
}

// destinationRoles is every role a move may target: all of them but
// drafts.
func destinationRoles() []string {
	return slices.DeleteFunc(EmailFolderRoles(), func(r string) bool { return r == "drafts" })
}

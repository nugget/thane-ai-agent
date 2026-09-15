package config

import "fmt"

// Mailbox owners: whose mailbox an account is.
const (
	// EmailMailboxOwnerAssistant is a mailbox Thane keeps, for itself or
	// for a purpose the operator gave it. It is the default.
	EmailMailboxOwnerAssistant = "assistant"

	// EmailMailboxOwnerOperator is the operator's own mailbox, which
	// Thane helps with but does not own.
	EmailMailboxOwnerOperator = "operator"
)

// maxEmailMailboxVoiceBytes bounds mailbox.voice. The voice is rendered
// into every turn that wears the email tag, so it stays a short note.
const maxEmailMailboxVoiceBytes = 500

// EmailMailboxConfig says whose mailbox an account is and how mail
// written from it should sound. It changes nothing about what the
// account may do or where its mail goes; that is policy.
type EmailMailboxConfig struct {
	// Owner is whose mailbox this is: "assistant" (default) for a
	// mailbox Thane keeps, or "operator" for the operator's own inbox,
	// which Thane helps with but does not own. On an operator mailbox,
	// email_read leaves mail unread unless the call asks otherwise, and
	// a turn the operator is not present for cannot mark mail seen,
	// whether by reading it or with email_mark. Any other value is
	// refused.
	Owner string `yaml:"owner"`

	// Voice is an operator-authored note, at most 500 bytes, on how mail
	// written from this account should sound, for example "first person
	// as Alice; brief; sign with her first name only". It is shown to
	// the model in the account's Email Accounts entry.
	Voice string `yaml:"voice"`

	// MoveInto lists where email_move may file this account's mail. An
	// entry is role:<role>, the folder holding that special-use role
	// (inbox, sent, trash, junk, archive, all, flagged or important;
	// junk_folder and trash_folder answer for their roles first), or an
	// exact folder name; "*" alone allows every folder. Moving mail back
	// to INBOX out of a listed folder is always allowed, so a move can be
	// undone. The drafts folder can never be listed. Default: [role:junk]
	// when owner is operator, so only spam leaves the operator's INBOX,
	// and ["*"] otherwise.
	MoveInto []string `yaml:"move_into"`

	// FilingNote is an optional operator-authored sentence, at most 300
	// bytes, on how this mailbox is filed, shown to the model in the
	// account's Email Accounts entry.
	FilingNote string `yaml:"filing_note"`
}

// MailboxOwner returns the effective owner, applying the default.
func (a EmailAccountConfig) MailboxOwner() string {
	if a.Mailbox.Owner != "" {
		return a.Mailbox.Owner
	}
	return EmailMailboxOwnerAssistant
}

// OperatorMailbox reports whether this is the operator's own mailbox.
func (a EmailAccountConfig) OperatorMailbox() bool {
	return a.MailboxOwner() == EmailMailboxOwnerOperator
}

// ReadMarksSeenByDefault reports whether reading a message marks it
// seen when the call does not say: false on an operator mailbox, where
// unread is how the operator sees what is new, and true otherwise.
func (a EmailAccountConfig) ReadMarksSeenByDefault() bool {
	return !a.OperatorMailbox()
}

// validateMailbox checks the account's mailbox block.
func (a EmailAccountConfig) validateMailbox(i int) error {
	switch a.Mailbox.Owner {
	case "", EmailMailboxOwnerAssistant, EmailMailboxOwnerOperator:
	default:
		return fmt.Errorf("email.accounts[%d] (%s): mailbox.owner %q is not one of assistant, operator", i, a.Name, a.Mailbox.Owner)
	}
	if n := len(a.Mailbox.Voice); n > maxEmailMailboxVoiceBytes {
		return fmt.Errorf("email.accounts[%d] (%s): mailbox.voice is %d bytes, over the %d-byte limit; keep it to a short note on how mail from this account should sound", i, a.Name, n, maxEmailMailboxVoiceBytes)
	}
	return a.validateFiling(i)
}

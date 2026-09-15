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
	return nil
}

package email

import "strings"

// mailboxView is the part of an account's Email Accounts entry that
// says whose mailbox it is and how mail written from it should sound.
// Every field is omitted at its default, so an assistant mailbox with
// no voice renders exactly as it did before these fields existed and
// the block grows only for accounts the operator marked.
type mailboxView struct {
	// Owner is "operator" on the operator's own mailbox and omitted
	// everywhere else.
	Owner string `json:"owner,omitempty"`

	// WritesAs is the full From the account writes under, display name
	// included, shown beside owner or voice: mail drafted there goes out
	// in that name, which the bare address does not say.
	WritesAs string `json:"writes_as,omitempty"`

	// Voice is the operator's note on how mail from the account should
	// sound.
	Voice string `json:"voice,omitempty"`

	// ReadsMarkSeen is what email_read does to the seen flag when the
	// call does not pass mark_seen. It is shown only on an operator
	// mailbox, where it is false.
	ReadsMarkSeen *bool `json:"reads_mark_seen,omitempty"`
}

// newMailboxView projects an account's mailbox block for its entry.
func newMailboxView(cfg AccountConfig) mailboxView {
	view := mailboxView{Voice: strings.TrimSpace(cfg.Mailbox.Voice)}
	if cfg.OperatorMailbox() {
		view.Owner = cfg.MailboxOwner()
		marksSeen := cfg.ReadMarksSeenByDefault()
		view.ReadsMarkSeen = &marksSeen
	}
	if view.Owner != "" || view.Voice != "" {
		view.WritesAs = strings.TrimSpace(cfg.DefaultFrom)
	}
	return view
}

// entryDraftsFolder is the drafts folder an account's entry shows. An
// account that can draft shows where a drafted message lands, resolved
// the way the send path resolves it. An operator mailbox that cannot
// draft still shows its configured drafts folder, even before any
// folder listing, because what the operator keeps there is theirs and
// email_move refuses it as a destination. Every other account that
// cannot draft shows none, so its entry renders as it did before the
// mailbox block existed.
func (s *Service) entryDraftsFolder(cfg AccountConfig) string {
	if cfg.CanDraft() {
		return s.knownDraftsFolder(cfg)
	}
	if cfg.OperatorMailbox() {
		return strings.TrimSpace(cfg.DraftsFolder)
	}
	return ""
}

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

// filingView is the part of an account's entry that says where
// email_move may file its mail. move_into renders only when it is
// limited. junk_folder renders beside it, and on every operator mailbox
// even when the operator allowed every folder, because the undo of a
// spam move starts from it. filing_note renders whenever the operator
// wrote one. An assistant account at its defaults therefore renders as
// it did before.
type filingView struct {
	// JunkFolder is the folder holding the junk role, from junk_folder
	// or the cached listing, omitted until one of those is known.
	JunkFolder string `json:"junk_folder,omitempty"`

	// MoveInto lists the folders email_move may file into, resolved to
	// names; a role no folder is known to hold yet shows as role:<role>.
	MoveInto []string `json:"move_into,omitempty"`

	// FilingNote is the operator's sentence on how the mailbox is filed.
	FilingNote string `json:"filing_note,omitempty"`
}

// newFilingView projects an account's filing policy for its entry,
// resolved from configuration and the cached folder listing only: a
// render never touches the server.
func (s *Service) newFilingView(cfg AccountConfig) filingView {
	view := filingView{FilingNote: strings.TrimSpace(cfg.Mailbox.FilingNote)}
	if cfg.MovesAnywhere() && !cfg.OperatorMailbox() {
		return view
	}
	known := func(role FolderRole) string { return s.knownRoleFolder(cfg, role) }
	view.JunkFolder = known(RoleJunk)
	if !cfg.MovesAnywhere() {
		view.MoveInto = resolveFilingPolicy(cfg, known).entryNames()
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

// Package email provides native IMAP and SMTP email for the Thane agent.
// It replaces the previous MCP email server approach with direct IMAP
// connections for reading and SMTP for sending, supporting multiple
// accounts, folder navigation, search, flag management, and
// markdown-to-MIME message composition.
package email

import (
	"io"
	"time"

	"github.com/emersion/go-imap/v2"
)

// drainLiteral reads and discards the contents of an IMAP literal reader.
// This prevents blocking the IMAP stream when a body section is fetched
// but not consumed. Nil readers are handled gracefully.
func drainLiteral(r imap.LiteralReader) {
	if r == nil {
		return
	}
	_, _ = io.Copy(io.Discard, r)
}

// Envelope is the summary metadata for an email message, suitable for
// list views and search results.
type Envelope struct {
	// UID is the IMAP unique identifier for this message within its folder.
	UID uint32

	// Date is the message's Date header.
	Date time.Time

	// From is the sender, formatted as "Name <addr>" or just the address.
	From string

	// To is the list of recipients.
	To []string

	// Subject is the message subject line.
	Subject string

	// Flags contains IMAP flags (e.g., \Seen, \Flagged).
	Flags []string

	// Size is the message size in bytes.
	Size uint32
}

// Message is a fully-fetched email with body content extracted from
// the MIME structure.
type Message struct {
	Envelope

	// MessageID is the Message-ID header value (without angle brackets).
	MessageID string

	// InReplyTo contains Message-IDs this message is a reply to.
	InReplyTo []string

	// References contains the full References chain for threading.
	References []string

	// Cc is the list of CC recipients.
	Cc []string

	// ReplyTo is the Reply-To address, if different from From.
	ReplyTo string

	// TextBody is the plain-text body content. Preferred over HTMLBody
	// for LLM consumption.
	TextBody string

	// HTMLBody is the raw HTML body, if present. Included for reference
	// but the agent should prefer TextBody.
	HTMLBody string
}

// Folder represents an IMAP mailbox with its status counters.
type Folder struct {
	// Name is the full mailbox name (e.g., "INBOX", "Sent", "Archive").
	Name string

	// Attributes contains IMAP mailbox attributes (e.g., \Noselect, \Trash).
	Attributes []string

	// Messages is the total number of messages in the folder.
	Messages uint32

	// Unseen is the count of messages without the \Seen flag.
	Unseen uint32
}

// ListOptions controls the behavior of email listing operations.
type ListOptions struct {
	// Folder is the mailbox to list from. Default: "INBOX".
	Folder string

	// Limit is the maximum number of messages to return. Default: 20.
	Limit int

	// Unseen restricts the listing to unseen messages only.
	Unseen bool

	// Account is the account name. Empty uses the primary account.
	Account string
}

// SearchOptions controls email search behavior.
type SearchOptions struct {
	// Folder is the mailbox to search. Default: "INBOX".
	Folder string

	// Query is a free-text string to match against message content.
	Query string

	// From filters by sender address or name.
	From string

	// Since filters for messages on or after this date.
	Since time.Time

	// Before filters for messages before this date.
	Before time.Time

	// Limit is the maximum number of results. Default: 20.
	Limit int

	// Account is the account name. Empty uses the primary account.
	Account string
}

// MarkAction describes a flag operation on one or more messages.
type MarkAction struct {
	// UIDs is the list of message UIDs to modify.
	UIDs []uint32

	// Folder is the mailbox containing the messages. Default: "INBOX".
	Folder string

	// Flag is the flag to add or remove: "seen", "flagged", or "answered".
	Flag string

	// Add controls the operation: true adds the flag, false removes it.
	Add bool

	// Account is the account name. Empty uses the primary account.
	Account string
}

// validFlags maps user-facing flag names to IMAP flag strings.
var validFlags = map[string]string{
	"seen":     `\Seen`,
	"flagged":  `\Flagged`,
	"answered": `\Answered`,
}

// ValidFlag reports whether the given flag name is supported and returns
// the corresponding IMAP flag string.
func ValidFlag(name string) (string, bool) {
	f, ok := validFlags[name]
	return f, ok
}

// SendOptions describes an outbound email message. The Body field
// contains markdown that the compose layer converts to both
// text/plain and text/html MIME parts.
type SendOptions struct {
	// To is the list of recipient addresses (required).
	To []string

	// Cc is the list of CC addresses.
	Cc []string

	// Subject is the email subject line (required).
	Subject string

	// Body is the message body in markdown format (required).
	Body string

	// Account is the account name. Empty uses the primary account.
	Account string
}

// ReplyOptions describes a reply to an existing message. The tool
// fetches the original message for threading headers.
type ReplyOptions struct {
	// UID is the IMAP UID of the message being replied to (required).
	UID uint32

	// Folder is the folder containing the original message. Default: "INBOX".
	Folder string

	// Body is the reply body in markdown format (required).
	Body string

	// ReplyAll sends the reply to all original recipients.
	ReplyAll bool

	// Account is the account name. Empty uses the primary account.
	Account string
}

// MoveOptions describes an IMAP message move operation.
type MoveOptions struct {
	// UIDs is the list of message UIDs to move (required).
	UIDs []uint32

	// Folder is the source folder. Default: "INBOX".
	Folder string

	// Destination is the target folder (required).
	Destination string

	// Account is the account name. Empty uses the primary account.
	Account string
}

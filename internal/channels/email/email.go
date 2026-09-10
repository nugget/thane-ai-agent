package email

import (
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// DefaultFolder is the mailbox every operation targets when the caller
// names none. It is the one folder RFC 3501 guarantees exists.
const DefaultFolder = "INBOX"

// DefaultListLimit is how many messages a list or search returns when
// the caller does not say.
const DefaultListLimit = 20

// MaxListLimit caps a single list or search so one call cannot pull an
// unbounded mailbox into a tool result. Callers learn the true match
// count from [ListResult.TotalMatched].
const MaxListLimit = 100

// normalizeFolder returns DefaultFolder for an empty or blank name and
// the trimmed name otherwise.
func normalizeFolder(folder string) string {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return DefaultFolder
	}
	return folder
}

// clampLimit applies the list default and ceiling.
func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultListLimit
	}
	if limit > MaxListLimit {
		return MaxListLimit
	}
	return limit
}

// drainLiteral reads and discards the contents of an IMAP literal reader.
// This prevents blocking the IMAP stream when a body section is fetched
// but not consumed. Nil readers are handled gracefully.
func drainLiteral(r imap.LiteralReader) {
	if r == nil {
		return
	}
	_, _ = io.Copy(io.Discard, r)
}

// Envelope is the summary of one message as the IMAP ENVELOPE reports
// it, plus its flags and size. It is what list and search return and
// what the poller stamps on wake events; the body is not fetched.
type Envelope struct {
	// UID identifies the message within the folder it was listed
	// from. It is not unique across folders or accounts.
	UID uint32 `json:"uid"`

	// Date is the message's Date header.
	Date time.Time `json:"date"`

	// From is the first From mailbox. RFC 5322 allows several; the
	// first is the one clients display and the one trust decisions use.
	From Address `json:"from"`

	// ReplyTo lists the Reply-To mailboxes, empty when the header is
	// absent. A reply goes here when present, else to From.
	ReplyTo []Address `json:"reply_to,omitempty"`

	// To and Cc list the visible recipients.
	To []Address `json:"to,omitempty"`
	Cc []Address `json:"cc,omitempty"`

	// Subject is the decoded Subject header.
	Subject string `json:"subject"`

	// MessageID is the Message-ID header without angle brackets, or
	// empty when the message has none.
	MessageID string `json:"message_id,omitempty"`

	// InReplyTo lists the Message-IDs this message replies to.
	InReplyTo []string `json:"in_reply_to,omitempty"`

	// Flags holds the IMAP system and keyword flags as the server
	// spells them (for example `\Seen`, `\Flagged`).
	Flags []string `json:"flags,omitempty"`

	// Size is the RFC822.SIZE of the message in bytes.
	Size uint32 `json:"size"`
}

// Attachment describes one non-text part of a message without its
// content: enough to tell the model what is attached and how large it
// is, so a later fetch can be a deliberate choice.
type Attachment struct {
	// Filename is the part's declared filename, or empty.
	Filename string `json:"filename,omitempty"`

	// ContentType is the media type, lowercase (for example
	// "application/pdf").
	ContentType string `json:"content_type"`

	// Size is the decoded part size in bytes.
	Size int64 `json:"size"`

	// Inline is true for parts with an inline disposition (typically
	// images referenced from an HTML body) as opposed to attachments.
	Inline bool `json:"inline,omitempty"`
}

// Message is a fully fetched email: its envelope, threading headers,
// the readable body, and a description of its attachments.
type Message struct {
	Envelope

	// References is the References header chain, oldest first.
	References []string `json:"references,omitempty"`

	// TextBody is the readable body. It is the message's text/plain
	// part when one exists and a plain-text rendering of the HTML
	// part otherwise; BodySource says which.
	TextBody string `json:"text_body,omitempty"`

	// HTMLBody is the raw text/html part, bounded by [maxBodySize]. It
	// is retained for callers that need the markup; model-facing
	// renderings use TextBody.
	HTMLBody string `json:"-"`

	// BodySource is "text" when TextBody came from a text/plain part,
	// "html" when it was rendered from the HTML part, and empty when
	// the message has no readable body.
	BodySource string `json:"body_source,omitempty"`

	// BodyTruncated is true when the body part exceeded [maxBodySize]
	// and TextBody holds only its head.
	BodyTruncated bool `json:"body_truncated,omitempty"`

	// RawTruncated is true when the whole message exceeded
	// [maxRawMessageSize]; parts beyond the cut were never parsed,
	// so Attachments may be incomplete.
	RawTruncated bool `json:"raw_truncated,omitempty"`

	// Attachments lists the non-text parts found while parsing.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// FolderRole names the special-use purpose of a mailbox (RFC 6154),
// derived from the server's attributes or, for INBOX, from its name.
type FolderRole string

// Folder roles the server can advertise. An empty role means the
// folder has no declared special use.
const (
	RoleInbox     FolderRole = "inbox"
	RoleDrafts    FolderRole = "drafts"
	RoleSent      FolderRole = "sent"
	RoleTrash     FolderRole = "trash"
	RoleJunk      FolderRole = "junk"
	RoleArchive   FolderRole = "archive"
	RoleAll       FolderRole = "all"
	RoleFlagged   FolderRole = "flagged"
	RoleImportant FolderRole = "important"
)

// Folder is one IMAP mailbox with its role and counters.
type Folder struct {
	// Name is the full mailbox name as the server spells it, including
	// any hierarchy prefix (for example "[Gmail]/Sent Mail").
	Name string `json:"name"`

	// Role is the special-use role, or empty.
	Role FolderRole `json:"role,omitempty"`

	// Delimiter is the hierarchy delimiter the server uses under this
	// mailbox, or empty when the server reports none.
	Delimiter string `json:"delimiter,omitempty"`

	// Selectable is false for mailboxes that only exist to hold
	// children (`\Noselect`); they cannot contain messages.
	Selectable bool `json:"selectable"`

	// Attributes holds the raw IMAP mailbox attributes.
	Attributes []string `json:"attributes,omitempty"`

	// Messages and Unseen are the folder's counters, zero for
	// non-selectable folders.
	Messages uint32 `json:"messages"`
	Unseen   uint32 `json:"unseen"`
}

// FindFolderByRole returns the first folder carrying role.
func FindFolderByRole(folders []Folder, role FolderRole) (Folder, bool) {
	for _, f := range folders {
		if f.Role == role {
			return f, true
		}
	}
	return Folder{}, false
}

// MailboxStatus is the STATUS of one folder: enough for a poller to
// seed and validate a high-water mark without listing messages.
type MailboxStatus struct {
	Folder      string `json:"folder"`
	Messages    uint32 `json:"messages"`
	Unseen      uint32 `json:"unseen"`
	UIDNext     uint32 `json:"uid_next"`
	UIDValidity uint32 `json:"uid_validity"`
}

// ListOptions controls [Client.ListMessages].
type ListOptions struct {
	// Folder is the mailbox to list. Empty means DefaultFolder.
	Folder string

	// Limit is the maximum number of envelopes to return, clamped to
	// [1, MaxListLimit] with DefaultListLimit for zero. Ignored when
	// SinceUID is set.
	Limit int

	// SinceUID restricts the listing to messages with UIDs strictly
	// greater than this value and lifts Limit, so a poller can fetch
	// everything newer than its mark in one call.
	SinceUID uint32

	// Unseen restricts the listing to messages without `\Seen`.
	Unseen bool

	// Account is the account name. Empty uses the primary account.
	Account string
}

// ListResult is what list and search return: the envelopes newest
// first, plus the folder and UIDVALIDITY they were read under and how
// many messages matched before the limit was applied.
type ListResult struct {
	Folder       string     `json:"folder"`
	UIDValidity  uint32     `json:"uid_validity"`
	TotalMatched int        `json:"total_matched"`
	Envelopes    []Envelope `json:"messages"`
}

// Truncated reports whether the limit dropped matches.
func (r ListResult) Truncated() bool {
	return r.TotalMatched > len(r.Envelopes)
}

// SearchOptions controls [Client.SearchMessages]. Every non-zero field
// is an additional server-side criterion; they combine with AND.
type SearchOptions struct {
	// Folder is the mailbox to search. Empty means DefaultFolder.
	Folder string

	// Query matches text anywhere in the message (IMAP TEXT).
	Query string

	// From, To, and Subject match substrings of those headers.
	From    string
	To      string
	Subject string

	// Since and Before bound the internal date. IMAP compares dates,
	// not times; the time of day is ignored.
	Since  time.Time
	Before time.Time

	// Unseen and Flagged restrict to messages with those flag states.
	Unseen  bool
	Flagged bool

	// MessageID and InReplyTo match those headers exactly (without
	// angle brackets), which is how a caller finds an original message
	// or an existing reply to it.
	MessageID string
	InReplyTo string

	// Limit is the maximum number of results, clamped like
	// [ListOptions.Limit].
	Limit int

	// Account is the account name. Empty uses the primary account.
	Account string
}

// ReadOptions controls [Client.ReadMessage].
type ReadOptions struct {
	// Folder is the mailbox containing the message. Empty means
	// DefaultFolder.
	Folder string

	// UID is the message to read (required).
	UID uint32

	// Peek fetches the body without setting `\Seen`. The default
	// (false) marks the message seen, because a read for triage is a
	// read; pass true when the read must leave the unseen state alone.
	Peek bool
}

// MarkAction describes a flag operation on one or more messages.
type MarkAction struct {
	// UIDs is the list of message UIDs to modify.
	UIDs []uint32

	// Folder is the mailbox containing the messages. Empty means
	// DefaultFolder.
	Folder string

	// Flag is the flag to add or remove: "seen", "flagged", or "answered".
	Flag string

	// Add controls the operation: true adds the flag, false removes it.
	Add bool

	// Account is the account name. Empty uses the primary account.
	Account string
}

// MarkResult reports which of the requested UIDs the server changed. A
// UID that no longer exists in the folder is requested but not
// affected, which is how a stale UID stops looking like success.
type MarkResult struct {
	Folder    string   `json:"folder"`
	Flag      string   `json:"flag"`
	Added     bool     `json:"added"`
	Requested []uint32 `json:"requested"`
	Affected  []uint32 `json:"affected"`
}

// validFlags maps user-facing flag names to IMAP flag strings.
var validFlags = map[string]imap.Flag{
	"seen":     imap.FlagSeen,
	"flagged":  imap.FlagFlagged,
	"answered": imap.FlagAnswered,
}

// ValidFlag reports whether the given flag name is supported and returns
// the corresponding IMAP flag string.
func ValidFlag(name string) (string, bool) {
	f, ok := validFlags[name]
	return string(f), ok
}

// ValidFlagNames lists the accepted flag names in a stable order, for
// error text and schemas.
func ValidFlagNames() []string {
	return []string{"seen", "flagged", "answered"}
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

	// Folder is the folder containing the original message. Empty
	// means DefaultFolder.
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

	// Folder is the source folder. Empty means DefaultFolder.
	Folder string

	// Destination is the target folder (required). It must already
	// exist in the same account; moves never create folders.
	Destination string

	// Account is the account name. Empty uses the primary account.
	Account string
}

// MoveResult reports a completed move. DestUIDs are the messages' new
// UIDs in the destination when the server returned COPYUID (UIDPLUS);
// DestUIDsKnown is false otherwise and the caller must list the
// destination to find them.
type MoveResult struct {
	SourceFolder    string   `json:"source_folder"`
	Destination     string   `json:"destination"`
	UIDs            []uint32 `json:"uids"`
	DestUIDs        []uint32 `json:"dest_uids,omitempty"`
	DestUIDValidity uint32   `json:"dest_uid_validity,omitempty"`
	DestUIDsKnown   bool     `json:"dest_uids_known"`
}

// AppendResult reports where an appended message landed. UID is zero
// when the server did not return APPENDUID (no UIDPLUS).
type AppendResult struct {
	Folder      string `json:"folder"`
	UID         uint32 `json:"uid,omitempty"`
	UIDValidity uint32 `json:"uid_validity,omitempty"`
}

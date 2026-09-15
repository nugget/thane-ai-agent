package email

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// The response structs in this file are the model-facing contract of
// the email tools. Every result names the account and folder it was
// resolved in beside every UID, because UIDs are scoped to both. Every
// address carries the directory's answer about who it is. Timestamps
// render as deltas, empty lists render as empty arrays with an explicit
// count, and mutations carry an action word.

// contactView is the matched directory record behind an address.
type contactView struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`

	// IsOwner means this contact record is the operator's. It says
	// nothing about whether the operator wrote a given message; a From
	// header is a claim until an Authentication verifies it.
	IsOwner bool `json:"is_owner"`
}

// addressView is one mailbox as rendered to the model, with the
// directory's answer attached so the model never has to look the
// address up itself or infer a person from a display name.
type addressView struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`

	// TrustZone is the effective zone: the contact's when matched, the
	// least privileged candidate's when ambiguous, "unknown" otherwise,
	// and known at most for an automated address.
	TrustZone string `json:"trust_zone"`

	// Automated is true only on a no-reply, notification, or bounce
	// address; absent means the mailbox name marks nothing.
	Automated bool `json:"automated,omitempty"`

	// Contact is the matched record, or null when the status is
	// anything but matched.
	Contact *contactView `json:"contact"`

	ContactStatus ContactStatus      `json:"contact_status"`
	Candidates    []ContactCandidate `json:"candidates,omitempty"`

	// CandidatesTotal counts every record sharing an ambiguous address;
	// candidates lists at most ten of them.
	CandidatesTotal int `json:"candidates_total,omitempty"`
}

func viewAddress(a Address, lookup *identityLookup) *addressView {
	if a.IsZero() {
		return nil
	}
	view := addressView{Name: truncateUTF8(a.Name, maxNameOutput), Address: a.Address, TrustZone: ZoneUnknown, ContactStatus: ContactUnmatched}
	if lookup != nil {
		match := lookup.resolve(a)
		view.TrustZone = match.TrustZone
		view.Automated = match.Automated
		view.ContactStatus = match.Status
		view.Candidates = match.Candidates
		view.CandidatesTotal = match.CandidatesTotal
		if match.Binding != nil {
			view.Contact = &contactView{ID: match.Binding.ContactID, Name: match.Binding.ContactName, IsOwner: match.Binding.IsOwner}
		}
	}
	return &view
}

func viewAddresses(list []Address, lookup *identityLookup) []addressView {
	if len(list) == 0 {
		return nil
	}
	out := make([]addressView, 0, len(list))
	for _, a := range list {
		if v := viewAddress(a, lookup); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

// The results below are held to the sizes AGENTS.md sets for tool
// output: search results 16 KB, transcripts 32 KB. The per-message
// bounds keep any single message from filling a result on its own.
const (
	maxListOutput       = 16 * 1024
	maxReadOutput       = 32 * 1024
	maxSubjectOutput    = 1024
	maxSummaryAddresses = 10
	maxHeaderAddresses  = 25
	maxNameOutput       = 256
	maxMessageIDOutput  = 512
)

// capAddresses returns at most n addresses and how many were left out.
func capAddresses(list []Address, n int) ([]Address, int) {
	if len(list) <= n {
		return list, 0
	}
	return list[:n], len(list) - n
}

// messageSummaryView is one envelope in a list or search result.
type messageSummaryView struct {
	UID       uint32        `json:"uid"`
	From      *addressView  `json:"from,omitempty"`
	To        []addressView `json:"to,omitempty"`
	Cc        []addressView `json:"cc,omitempty"`
	Subject   string        `json:"subject"`
	Date      string        `json:"date,omitempty"`
	MessageID string        `json:"message_id,omitempty"`
	Flags     []string      `json:"flags,omitempty"`
	Size      uint32        `json:"size"`

	// AddressesOmitted counts to and cc addresses past
	// maxSummaryAddresses per list that the summary leaves out.
	AddressesOmitted int `json:"addresses_omitted,omitempty"`
}

// listResponse is the result of email_list and email_search.
type listResponse struct {
	Account      string               `json:"account"`
	Folder       string               `json:"folder"`
	Count        int                  `json:"count"`
	TotalMatched int                  `json:"total_matched"`
	Truncated    bool                 `json:"truncated"`
	Messages     []messageSummaryView `json:"messages"`
}

func newListResponse(account string, listed ListResult, lookup *identityLookup, now time.Time) listResponse {
	resp := listResponse{
		Account:      account,
		Folder:       listed.Folder,
		Count:        len(listed.Envelopes),
		TotalMatched: listed.TotalMatched,
		Truncated:    listed.Truncated(),
		Messages:     make([]messageSummaryView, 0, len(listed.Envelopes)),
	}
	for _, env := range listed.Envelopes {
		to, toOmitted := capAddresses(env.To, maxSummaryAddresses)
		cc, ccOmitted := capAddresses(env.Cc, maxSummaryAddresses)
		resp.Messages = append(resp.Messages, messageSummaryView{
			UID:              env.UID,
			From:             viewAddress(env.From, lookup),
			To:               viewAddresses(to, lookup),
			Cc:               viewAddresses(cc, lookup),
			AddressesOmitted: toOmitted + ccOmitted,
			Subject:          truncateUTF8(env.Subject, maxSubjectOutput),
			Date:             deltaOrEmpty(env.Date, now),
			MessageID:        truncateUTF8(env.MessageID, maxMessageIDOutput),
			Flags:            env.Flags,
			Size:             env.Size,
		})
	}
	return resp
}

// readResponse is the header object of an email_read result; the body
// follows it after a separator line.
type readResponse struct {
	Account        string         `json:"account"`
	Folder         string         `json:"folder"`
	UID            uint32         `json:"uid"`
	MessageID      string         `json:"message_id,omitempty"`
	InReplyTo      []string       `json:"in_reply_to,omitempty"`
	References     []string       `json:"references,omitempty"`
	From           *addressView   `json:"from,omitempty"`
	To             []addressView  `json:"to,omitempty"`
	Cc             []addressView  `json:"cc,omitempty"`
	ReplyTo        []addressView  `json:"reply_to,omitempty"`
	Subject        string         `json:"subject"`
	Date           string         `json:"date,omitempty"`
	Flags          []string       `json:"flags,omitempty"`
	Size           uint32         `json:"size"`
	MarkedSeen     bool           `json:"marked_seen"`
	BodySource     string         `json:"body_source,omitempty"`
	HiddenContent  *hiddenContent `json:"hidden_content,omitempty"`
	BodyTruncated  bool           `json:"body_truncated,omitempty"`
	RawTruncated   bool           `json:"raw_truncated,omitempty"`
	Attachments    []Attachment   `json:"attachments"`
	Authentication Authentication `json:"authentication"`

	// HeaderMarks renders flat as auto_submitted and bulk, each omitted
	// when the message's headers claim nothing.
	HeaderMarks

	// AccessNote explains a read that could not mark the message seen
	// because the account's access level is read.
	AccessNote string `json:"access_note,omitempty"`

	// AttachmentsOmitted counts non-text parts past the fifty described.
	AttachmentsOmitted int `json:"attachments_omitted,omitempty"`

	// AddressesOmitted counts to, cc, and reply_to addresses past
	// maxHeaderAddresses per list that the header leaves out.
	AddressesOmitted int `json:"addresses_omitted,omitempty"`
}

// hiddenContent says that an HTML body's own markup hid something from
// a person reading the message, through an idiom the renderer
// recognises (see [visibility]): text, an image's description, or a
// link's target. The read result omits it when nothing was hidden.
type hiddenContent struct {
	// Present is always true, so the object reads as a statement
	// wherever it appears.
	Present bool `json:"present"`

	// Chars counts the hidden characters of text and image
	// descriptions, whitespace aside. That text is withheld from the
	// body.
	Chars int `json:"chars"`
}

// newHiddenContent returns nil when nothing was hidden.
func newHiddenContent(hidden bool, chars int) *hiddenContent {
	if !hidden {
		return nil
	}
	return &hiddenContent{Present: true, Chars: chars}
}

// bodySeparator divides the read result's JSON header from the body
// text, the same shape forge uses for issue bodies.
const bodySeparator = "\n\n---\n"

func newReadResponse(account, folder string, msg *Message, markedSeen bool, auth Authentication, lookup *identityLookup, now time.Time) readResponse {
	attachments := msg.Attachments
	if attachments == nil {
		attachments = []Attachment{}
	}
	to, toOmitted := capAddresses(msg.To, maxHeaderAddresses)
	cc, ccOmitted := capAddresses(msg.Cc, maxHeaderAddresses)
	replyTo, replyToOmitted := capAddresses(msg.ReplyTo, maxHeaderAddresses)
	return readResponse{
		Account:            account,
		Folder:             folder,
		UID:                msg.UID,
		MessageID:          msg.MessageID,
		InReplyTo:          msg.InReplyTo,
		References:         msg.References,
		From:               viewAddress(msg.From, lookup),
		To:                 viewAddresses(to, lookup),
		Cc:                 viewAddresses(cc, lookup),
		ReplyTo:            viewAddresses(replyTo, lookup),
		AddressesOmitted:   toOmitted + ccOmitted + replyToOmitted,
		AttachmentsOmitted: msg.AttachmentsOmitted,
		Subject:            msg.Subject,
		Date:               deltaOrEmpty(msg.Date, now),
		Flags:              msg.Flags,
		Size:               msg.Size,
		MarkedSeen:         markedSeen,
		BodySource:         msg.BodySource,
		HiddenContent:      newHiddenContent(msg.Hidden, msg.HiddenChars),
		BodyTruncated:      msg.BodyTruncated,
		RawTruncated:       msg.RawTruncated,
		Attachments:        attachments,
		Authentication:     auth,
		HeaderMarks:        msg.HeaderMarks,
	}
}

// renderRead joins the header JSON and the body text, cutting the body
// so the whole result stays within maxReadOutput. A cut sets
// body_truncated in the header and ends the body with a marker.
func renderRead(header readResponse, msg *Message) (string, error) {
	data, err := marshalResponse(header)
	if err != nil {
		return "", err
	}
	body := msg.TextBody
	if body == "" {
		body = "[no readable body]"
	}
	if len(data)+len(bodySeparator)+len(body) <= maxReadOutput {
		return data + bodySeparator + body, nil
	}
	header.BodyTruncated = true
	if data, err = marshalResponse(header); err != nil {
		return "", err
	}
	marker := fmt.Sprintf("\n\n[body cut to keep this result within %d KB]", maxReadOutput/1024)
	budget := max(maxReadOutput-len(data)-len(bodySeparator)-len(marker), 0)
	return data + bodySeparator + truncateUTF8(body, budget) + marker, nil
}

// marshalListResponse renders a list or search result within
// maxListOutput, dropping the oldest messages (the list is newest
// first) until it fits; a drop lowers count and sets truncated.
func marshalListResponse(resp listResponse) (string, error) {
	for {
		data, err := marshalResponse(resp)
		if err != nil || len(data) <= maxListOutput || len(resp.Messages) == 0 {
			return data, err
		}
		cut := len(resp.Messages) - max(1, len(resp.Messages)/10)
		resp.Messages = resp.Messages[:cut]
		resp.Count = len(resp.Messages)
		resp.Truncated = true
	}
}

// foldersResponse is the result of email_folders.
type foldersResponse struct {
	Account string `json:"account"`

	// Count is how many folders the result lists; Total is how many the
	// account has. They differ only when Truncated is true.
	Count     int      `json:"count"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated"`
	Folders   []Folder `json:"folders"`
}

// markResponse is the result of email_mark.
type markResponse struct {
	Action       string   `json:"action"`
	Account      string   `json:"account"`
	Folder       string   `json:"folder"`
	Flag         string   `json:"flag"`
	UIDsAffected []uint32 `json:"uids_affected"`
	UIDsNotFound []uint32 `json:"uids_not_found"`
}

// moveResponse is the result of email_move.
type moveResponse struct {
	Action               string   `json:"action"`
	Account              string   `json:"account"`
	SourceFolder         string   `json:"source_folder"`
	DestinationFolder    string   `json:"destination_folder"`
	UIDs                 []uint32 `json:"uids"`
	DestinationUIDs      []uint32 `json:"destination_uids"`
	DestinationUIDsKnown bool     `json:"destination_uids_known"`

	// UIDsNotFound lists requested UIDs the server's COPYUID did not
	// include: they were not in the source folder and did not move. It
	// is empty when destination_uids_known is false, because the server
	// then confirmed nothing.
	UIDsNotFound []uint32 `json:"uids_not_found"`

	// Moved lists each moved message with its UID in the destination
	// when the server confirmed it, its Message-ID, its sender, and the
	// sender's trust zone.
	Moved []movedMessage `json:"moved"`

	// Refused lists the messages the junk guard kept out of the junk
	// folder, with why and what to do instead. They did not move.
	Refused []refusedMessage `json:"refused"`

	// MovedOmitted counts the entries at the end of moved that carry
	// only uid and destination_uid, their detail dropped to hold the
	// result to maxMoveOutput. They moved like the rest.
	MovedOmitted int `json:"moved_omitted,omitempty"`

	// RefusedOmitted counts the entries at the end of refused that
	// carry only uid, their detail dropped to hold the result to
	// maxMoveOutput. They stayed where they were like the rest.
	RefusedOmitted int `json:"refused_omitted,omitempty"`

	Note string `json:"note,omitempty"`
}

// sendResponse is the result of email_send and email_reply. The
// disposition says what happened; the decision says why, and carries
// the one per-recipient assessment list, the same place a refusal
// carries it.
type sendResponse struct {
	Disposition Disposition `json:"disposition"`
	Account     string      `json:"account"`
	MessageID   string      `json:"message_id"`
	To          []string    `json:"to"`
	Cc          []string    `json:"cc"`
	BccCount    int         `json:"bcc_count"`
	Subject     string      `json:"subject"`
	InReplyTo   string      `json:"in_reply_to,omitempty"`

	// SentFolder is the folder a copy of the sent message was written
	// to, and SentFolderCopy says whether that worked: "stored" or
	// "failed". Both are omitted when the account keeps no Sent copy.
	SentFolder     string `json:"sent_folder,omitempty"`
	SentFolderCopy string `json:"sent_folder_copy,omitempty"`

	DraftsFolder string   `json:"drafts_folder,omitempty"`
	DraftUID     uint32   `json:"draft_uid,omitempty"`
	Signed       bool     `json:"signed"`
	Note         string   `json:"note,omitempty"`
	Decision     Decision `json:"decision"`
}

// newSendResponse renders a delivered or drafted outcome.
func newSendResponse(outcome SendOutcome, subject, inReplyTo string) sendResponse {
	resp := sendResponse{
		Disposition:    outcome.Decision.Disposition,
		Account:        outcome.Decision.Account,
		MessageID:      outcome.Composed.MessageID,
		To:             nonNilStrings(addressStrings(outcome.Composed.To)),
		Cc:             nonNilStrings(addressStrings(outcome.Composed.Cc)),
		BccCount:       outcome.BccCount,
		Subject:        subject,
		InReplyTo:      inReplyTo,
		SentFolder:     outcome.SentFolder,
		SentFolderCopy: outcome.SentFolderCopy,
		DraftsFolder:   outcome.DraftsFolder,
		DraftUID:       outcome.DraftUID,
		Signed:         outcome.Signed,
		Decision:       outcome.Decision,
	}
	if resp.Disposition == DispositionDrafted {
		resp.Note = "Held in " + outcome.DraftsFolder + " for the operator to send from their own client; nothing has left the mailbox. Do not resend it."
	}
	return resp
}

// marshalResponse renders a result as compact JSON.
func marshalResponse(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(data), nil
}

// deltaOrEmpty renders a timestamp as a delta, or empty for the zero
// time so a missing Date header does not render as an epoch delta.
func deltaOrEmpty(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	return promptfmt.FormatDeltaOnly(t, now)
}

// nonNilUIDs returns an empty slice for nil so JSON renders [] rather
// than null.
func nonNilUIDs(uids []uint32) []uint32 {
	if uids == nil {
		return []uint32{}
	}
	return uids
}

// nonNilStrings returns an empty slice for nil so JSON renders [].
func nonNilStrings(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

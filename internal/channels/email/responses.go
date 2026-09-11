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
	IsOwner bool `json:"is_owner,omitempty"`
}

// addressView is one mailbox as rendered to the model, with the
// directory's answer attached so the model never has to look the
// address up itself or infer a person from a display name.
type addressView struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`

	// TrustZone is the effective zone: the contact's when matched, the
	// least privileged candidate's when ambiguous, "unknown" otherwise.
	TrustZone string `json:"trust_zone"`

	// Contact is the matched record, or null when the status is
	// anything but matched.
	Contact *contactView `json:"contact"`

	ContactStatus ContactStatus      `json:"contact_status"`
	Candidates    []ContactCandidate `json:"candidates,omitempty"`
}

func viewAddress(a Address, lookup *identityLookup) *addressView {
	if a.IsZero() {
		return nil
	}
	view := addressView{Name: a.Name, Address: a.Address, TrustZone: ZoneUnknown, ContactStatus: ContactUnmatched}
	if lookup != nil {
		match := lookup.resolve(a)
		view.TrustZone = match.TrustZone
		view.ContactStatus = match.Status
		view.Candidates = match.Candidates
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
		resp.Messages = append(resp.Messages, messageSummaryView{
			UID:       env.UID,
			From:      viewAddress(env.From, lookup),
			To:        viewAddresses(env.To, lookup),
			Cc:        viewAddresses(env.Cc, lookup),
			Subject:   env.Subject,
			Date:      deltaOrEmpty(env.Date, now),
			MessageID: env.MessageID,
			Flags:     env.Flags,
			Size:      env.Size,
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
	BodyTruncated  bool           `json:"body_truncated,omitempty"`
	RawTruncated   bool           `json:"raw_truncated,omitempty"`
	Attachments    []Attachment   `json:"attachments"`
	Authentication Authentication `json:"authentication"`

	// AccessNote explains a read that could not mark the message seen
	// because the account's access level is read.
	AccessNote string `json:"access_note,omitempty"`
}

// bodySeparator divides the read result's JSON header from the body
// text, the same shape forge uses for issue bodies.
const bodySeparator = "\n\n---\n"

func newReadResponse(account, folder string, msg *Message, markedSeen bool, auth Authentication, lookup *identityLookup, now time.Time) readResponse {
	attachments := msg.Attachments
	if attachments == nil {
		attachments = []Attachment{}
	}
	return readResponse{
		Account:        account,
		Folder:         folder,
		UID:            msg.UID,
		MessageID:      msg.MessageID,
		InReplyTo:      msg.InReplyTo,
		References:     msg.References,
		From:           viewAddress(msg.From, lookup),
		To:             viewAddresses(msg.To, lookup),
		Cc:             viewAddresses(msg.Cc, lookup),
		ReplyTo:        viewAddresses(msg.ReplyTo, lookup),
		Subject:        msg.Subject,
		Date:           deltaOrEmpty(msg.Date, now),
		Flags:          msg.Flags,
		Size:           msg.Size,
		MarkedSeen:     markedSeen,
		BodySource:     msg.BodySource,
		BodyTruncated:  msg.BodyTruncated,
		RawTruncated:   msg.RawTruncated,
		Attachments:    attachments,
		Authentication: auth,
	}
}

// renderRead joins the header JSON and the body text.
func renderRead(header readResponse, msg *Message) (string, error) {
	data, err := marshalResponse(header)
	if err != nil {
		return "", err
	}
	body := msg.TextBody
	if body == "" {
		body = "[no readable body]"
	}
	return data + bodySeparator + body, nil
}

// foldersResponse is the result of email_folders.
type foldersResponse struct {
	Account string   `json:"account"`
	Count   int      `json:"count"`
	Folders []Folder `json:"folders"`
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
	Note                 string   `json:"note,omitempty"`
}

// sendResponse is the result of email_send and email_reply. The
// disposition says what happened; the decision says why.
type sendResponse struct {
	Disposition    Disposition           `json:"disposition"`
	Account        string                `json:"account"`
	MessageID      string                `json:"message_id"`
	To             []string              `json:"to"`
	Cc             []string              `json:"cc"`
	BccCount       int                   `json:"bcc_count"`
	Subject        string                `json:"subject"`
	InReplyTo      string                `json:"in_reply_to,omitempty"`
	SentFolderCopy string                `json:"sent_folder_copy,omitempty"`
	DraftsFolder   string                `json:"drafts_folder,omitempty"`
	DraftUID       uint32                `json:"draft_uid,omitempty"`
	Signed         bool                  `json:"signed"`
	Recipients     []RecipientAssessment `json:"recipients"`
	Note           string                `json:"note,omitempty"`
	Decision       Decision              `json:"decision"`
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
		SentFolderCopy: outcome.SentFolderCopy,
		DraftsFolder:   outcome.DraftsFolder,
		DraftUID:       outcome.DraftUID,
		Signed:         outcome.Signed,
		Recipients:     outcome.Decision.Recipients,
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

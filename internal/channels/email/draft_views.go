package email

import (
	"time"
)

// The result shapes of the draft tools. Times render as deltas, lists
// render as arrays, and every row names its draft_id and account.

// draftsResponse is email_drafts' result.
type draftsResponse struct {
	// Count is how many rows the result lists; Total is how many matched.
	// They differ only when Truncated is true.
	Count     int  `json:"count"`
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`

	// Open counts the open drafts on the accounts listed, whether or not
	// include_closed asked for the others too.
	Open int `json:"open"`

	Drafts []draftRowView `json:"drafts"`

	// Errors lists accounts whose ledger could not be checked against
	// their drafts folder; their drafts are left out.
	Errors []draftAccountError `json:"errors"`
}

// draftAccountError is one account email_drafts could not reconcile.
type draftAccountError struct {
	Account string `json:"account"`
	Error   string `json:"error"`
}

// draftRowView is one ledger entry in email_drafts.
type draftRowView struct {
	DraftID      string            `json:"draft_id"`
	Account      string            `json:"account"`
	Stage        DraftStage        `json:"stage"`
	ClosedReason string            `json:"closed_reason,omitempty"`
	Subject      string            `json:"subject"`
	To           []string          `json:"to"`
	Original     *draftOriginalRow `json:"original,omitempty"`
	Revisions    int               `json:"revisions"`
	LastRevised  draftRevisionView `json:"last_revised"`
}

// draftOriginalRow is the message a listed draft answers.
type draftOriginalRow struct {
	From      string `json:"from,omitempty"`
	Subject   string `json:"subject,omitempty"`
	MessageID string `json:"message_id"`
}

// draftRevisionView is one version's provenance with its time as a
// delta.
type draftRevisionView struct {
	By   string `json:"by"`
	At   string `json:"at"`
	Note string `json:"note,omitempty"`
}

func newDraftRevisionView(r draftRevision, now time.Time, withNote bool) draftRevisionView {
	view := draftRevisionView{By: r.By, At: deltaOrEmpty(r.At, now)}
	if withNote {
		view.Note = r.Note
	}
	return view
}

func newDraftRow(e draftEntry, now time.Time) draftRowView {
	row := draftRowView{
		DraftID:      e.ID,
		Account:      e.Account,
		Stage:        e.Stage,
		ClosedReason: e.ClosedReason,
		Subject:      truncateUTF8(e.Subject, maxSubjectOutput),
		To:           nonNilStrings(e.To),
		Revisions:    e.Revisions,
		LastRevised:  newDraftRevisionView(e.lastRevision(), now, false),
	}
	if e.Original != nil {
		row.Original = &draftOriginalRow{
			From:      e.Original.From.Address,
			Subject:   truncateUTF8(e.Original.Subject, maxSubjectOutput),
			MessageID: truncateUTF8(e.Original.MessageID, maxMessageIDOutput),
		}
	}
	return row
}

// marshalDraftsResponse renders email_drafts within maxListOutput,
// dropping the oldest rows (the list is newest first) until it fits.
func marshalDraftsResponse(resp draftsResponse) (string, error) {
	for {
		data, err := marshalResponse(resp)
		if err != nil || len(data) <= maxListOutput || len(resp.Drafts) == 0 {
			return data, err
		}
		resp.Drafts = resp.Drafts[:len(resp.Drafts)-max(1, len(resp.Drafts)/10)]
		resp.Count = len(resp.Drafts)
		resp.Truncated = true
	}
}

// maxDraftGetHistory caps the versions email_draft_get lists.
const maxDraftGetHistory = 10

// draftGetHeader is the JSON header of email_draft_get.
type draftGetHeader struct {
	DraftID      string     `json:"draft_id"`
	Account      string     `json:"account"`
	Stage        DraftStage `json:"stage"`
	ClosedReason string     `json:"closed_reason,omitempty"`
	DraftsFolder string     `json:"drafts_folder"`
	UID          uint32     `json:"uid,omitempty"`
	MessageID    string     `json:"message_id"`

	// From is the draft's From; WritesAs is the account's, which a new
	// draft would carry; Voice is the operator's note on how mail from
	// the account should sound; Owner is whose mailbox it is.
	From     string `json:"from"`
	WritesAs string `json:"writes_as,omitempty"`
	Voice    string `json:"voice,omitempty"`
	Owner    string `json:"owner"`

	To []string `json:"to"`
	Cc []string `json:"cc,omitempty"`

	// AddressesOmitted counts to and cc addresses past
	// maxHeaderAddresses per list that the header leaves out.
	AddressesOmitted int `json:"addresses_omitted,omitempty"`

	Subject   string `json:"subject"`
	InReplyTo string `json:"in_reply_to,omitempty"`

	Revisions      int                 `json:"revisions"`
	History        []draftRevisionView `json:"history"`
	HistoryOmitted int                 `json:"history_omitted,omitempty"`

	Original *draftOriginalView `json:"original,omitempty"`

	DraftBodyTruncated    bool `json:"draft_body_truncated,omitempty"`
	OriginalBodyTruncated bool `json:"original_body_truncated,omitempty"`
}

// draftOriginalView is the message a draft answers, as email_draft_get
// found it. Found is false when it is no longer in Folder.
type draftOriginalView struct {
	Folder    string       `json:"folder"`
	MessageID string       `json:"message_id"`
	From      *addressView `json:"from,omitempty"`
	Subject   string       `json:"subject,omitempty"`
	UID       uint32       `json:"uid,omitempty"`
	Found     bool         `json:"found"`
}

// newDraftGetHeader builds email_draft_get's header with every
// variable-length field clipped (draft_get_bounds.go).
func newDraftGetHeader(e draftEntry, cfg AccountConfig, lookup *identityLookup, now time.Time) draftGetHeader {
	to, toOmitted := clipList(e.To, maxHeaderAddresses, maxDraftAddressOutput)
	cc, ccOmitted := clipList(e.Cc, maxHeaderAddresses, maxDraftAddressOutput)
	header := draftGetHeader{
		DraftID:          e.ID,
		Account:          clipTo(e.Account, maxNameOutput),
		Stage:            e.Stage,
		ClosedReason:     e.ClosedReason,
		DraftsFolder:     clipTo(e.Folder, maxDraftFolderOutput),
		UID:              e.UID,
		MessageID:        clipTo(e.MessageID, maxMessageIDOutput),
		From:             clipTo(e.From, maxDraftAddressOutput),
		WritesAs:         clipTo(cfg.DefaultFrom, maxDraftAddressOutput),
		Voice:            clipTo(cfg.Mailbox.Voice, maxDraftVoiceOutput),
		Owner:            clipTo(cfg.MailboxOwner(), maxNameOutput),
		To:               nonNilStrings(to),
		Cc:               cc,
		AddressesOmitted: toOmitted + ccOmitted,
		Subject:          clipTo(e.Subject, maxSubjectOutput),
		InReplyTo:        clipTo(e.InReplyTo, maxMessageIDOutput),
		Revisions:        e.Revisions,
		History:          []draftRevisionView{},
	}
	// The first version, as the draft was written, always shows; the
	// versions left out come between it and the latest ones.
	history := e.History
	if len(history) > maxDraftGetHistory {
		header.HistoryOmitted = len(history) - maxDraftGetHistory
		history = append(history[:1:1], history[len(history)-maxDraftGetHistory+1:]...)
	}
	for _, r := range history {
		view := newDraftRevisionView(r, now, true)
		view.By, view.Note = clipTo(view.By, maxNameOutput), clipTo(view.Note, maxDraftNoteBytes)
		header.History = append(header.History, view)
	}
	if e.Original != nil {
		from := viewAddress(e.Original.From, lookup)
		if from != nil {
			from.Address = clipTo(from.Address, maxDraftAddressOutput)
		}
		header.Original = &draftOriginalView{
			Folder:    clipTo(e.Original.Folder, maxDraftFolderOutput),
			MessageID: clipTo(e.Original.MessageID, maxMessageIDOutput),
			From:      from,
			Subject:   clipTo(e.Original.Subject, maxSubjectOutput),
		}
	}
	return header
}

// draftCutMarker ends a body email_draft_get cut to fit.
const draftCutMarker = "\n\n[cut to keep this result within 32 KB]"

// draftHeaderSlack reserves room for the truncation flags a cut adds to
// the header after the budget was computed.
const draftHeaderSlack = 64

// renderDraftGet joins the header, the draft's body, and the original's
// body within maxReadOutput: the header keeps to its share
// (marshalDraftGetHeader), and the bodies are cut to fit the rest.
func renderDraftGet(header draftGetHeader, draftBody, originalBody string) (string, error) {
	data, err := marshalDraftGetHeader(header, false, false)
	if err != nil {
		return "", err
	}
	if len(data)+2*len(bodySeparator)+len(draftBody)+len(originalBody) <= maxReadOutput {
		return data + bodySeparator + draftBody + bodySeparator + originalBody, nil
	}
	budget := maxReadOutput - len(data) - 2*len(bodySeparator) - 2*len(draftCutMarker) - draftHeaderSlack
	draftShare, originalShare := splitBudget(budget, len(draftBody), len(originalBody))
	draftCut, originalCut := len(draftBody) > draftShare, len(originalBody) > originalShare
	if draftCut {
		draftBody = truncateUTF8(draftBody, draftShare) + draftCutMarker
	}
	if originalCut {
		originalBody = truncateUTF8(originalBody, originalShare) + draftCutMarker
	}
	if data, err = marshalDraftGetHeader(header, draftCut, originalCut); err != nil {
		return "", err
	}
	return data + bodySeparator + draftBody + bodySeparator + originalBody, nil
}

// splitBudget divides budget bytes between two bodies of lengths a and
// b: evenly, except that a body shorter than its half gives the rest to
// the other.
func splitBudget(budget, a, b int) (int, int) {
	budget = max(budget, 0)
	half := budget / 2
	switch {
	case a <= half:
		return a, budget - a
	case b <= budget-half:
		return budget - b, b
	}
	return half, budget - half
}

// draftWithdrawnView is email_draft_withdraw's result.
type draftWithdrawnView struct {
	Action        string `json:"action"`
	DraftID       string `json:"draft_id"`
	Account       string `json:"account"`
	DraftsFolder  string `json:"drafts_folder"`
	TrashFolder   string `json:"trash_folder"`
	TrashUID      uint32 `json:"trash_uid,omitempty"`
	TrashUIDKnown bool   `json:"trash_uid_known"`
	Note          string `json:"note"`
}

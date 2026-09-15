package email

// email_draft_get is held to maxReadOutput, and its JSON header to half
// of that, so the two bodies always keep the rest. Every variable-length
// header field is clipped on a rune boundary and ends with
// fieldCutMarker; the address lists and the history are capped, with
// counts of what was left out; and a header that still passes its share,
// which JSON escaping of clipped text can cause, gives way to
// draftGetMinimalHeader. Only then are the bodies cut to fit.

const (
	// maxDraftGetHeaderOutput is the most of the result the header takes.
	maxDraftGetHeaderOutput = maxReadOutput / 2

	// maxDraftAddressOutput bounds each address string in the header: an
	// RFC 5321 path with room for a display name.
	maxDraftAddressOutput = 320

	// maxDraftVoiceOutput bounds the account's voice note.
	maxDraftVoiceOutput = 2048

	// maxDraftFolderOutput bounds each folder name.
	maxDraftFolderOutput = 512
)

// clipTo cuts s to at most limit bytes on a rune boundary, ending a cut
// value with fieldCutMarker.
func clipTo(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return truncateUTF8(s, limit-len(fieldCutMarker)) + fieldCutMarker
}

// clipList returns at most n of list, each clipped to limit bytes, and
// how many were left out. It returns nil for an empty list and never
// changes list.
func clipList(list []string, n, limit int) ([]string, int) {
	if len(list) == 0 {
		return nil, 0
	}
	kept := list[:min(len(list), n)]
	out := make([]string, 0, len(kept))
	for _, s := range kept {
		out = append(out, clipTo(s, limit))
	}
	return out, len(list) - len(kept)
}

// draftGetMinimalHeader replaces a draftGetHeader that would pass
// maxDraftGetHeaderOutput even with every field clipped. It keeps what
// names the draft and where email_read finds it, and counts the rest.
type draftGetMinimalHeader struct {
	DraftID      string     `json:"draft_id"`
	Account      string     `json:"account"`
	Stage        DraftStage `json:"stage"`
	ClosedReason string     `json:"closed_reason,omitempty"`
	DraftsFolder string     `json:"drafts_folder"`
	UID          uint32     `json:"uid,omitempty"`
	Revisions    int        `json:"revisions"`

	// HistoryOmitted counts every version of the history, and
	// AddressesOmitted every to and cc address, since none are listed.
	HistoryOmitted   int `json:"history_omitted"`
	AddressesOmitted int `json:"addresses_omitted"`

	// HeaderTruncated is always true: the full header was left out.
	HeaderTruncated bool `json:"header_truncated"`

	DraftBodyTruncated    bool `json:"draft_body_truncated,omitempty"`
	OriginalBodyTruncated bool `json:"original_body_truncated,omitempty"`
}

// minimal is the header's minimal form.
func (h draftGetHeader) minimal() draftGetMinimalHeader {
	return draftGetMinimalHeader{
		DraftID:               h.DraftID,
		Account:               h.Account,
		Stage:                 h.Stage,
		ClosedReason:          h.ClosedReason,
		DraftsFolder:          h.DraftsFolder,
		UID:                   h.UID,
		Revisions:             h.Revisions,
		HistoryOmitted:        len(h.History) + h.HistoryOmitted,
		AddressesOmitted:      len(h.To) + len(h.Cc) + h.AddressesOmitted,
		HeaderTruncated:       true,
		DraftBodyTruncated:    h.DraftBodyTruncated,
		OriginalBodyTruncated: h.OriginalBodyTruncated,
	}
}

// marshalDraftGetHeader renders the header with its body flags set, or
// its minimal form when the full header would pass
// maxDraftGetHeaderOutput.
func marshalDraftGetHeader(h draftGetHeader, draftCut, originalCut bool) (string, error) {
	h.DraftBodyTruncated, h.OriginalBodyTruncated = draftCut, originalCut
	data, err := marshalResponse(h)
	if err != nil || len(data) <= maxDraftGetHeaderOutput {
		return data, err
	}
	return marshalResponse(h.minimal())
}

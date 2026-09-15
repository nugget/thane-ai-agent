package email

import (
	"fmt"
	"slices"
)

// Bounds on email_move and email_mark, held to the tool-result sizes
// AGENTS.md sets. One call carries at most maxBatchUIDs UIDs. An
// email_move result is held to maxMoveOutput: each entry's text fields
// are clipped first, and past the budget the entries at the end of
// moved, then of refused, keep only their UIDs and are counted in
// moved_omitted and refused_omitted. No UID is ever dropped: uids,
// destination_uids, and each entry's uid and destination_uid are what
// a later turn needs to find the messages again or undo the move.

const (
	// maxBatchUIDs is how many UIDs one email_move or email_mark call
	// may carry.
	maxBatchUIDs = 100

	// maxMoveOutput is the size an email_move result is held to, the
	// same as a list or search result's.
	maxMoveOutput = maxListOutput

	// maxMoveFieldOutput bounds each from, message_id, and reason in an
	// email_move result, marker included.
	maxMoveFieldOutput = 256

	// fieldCutMarker ends a field clipped to maxMoveFieldOutput.
	fieldCutMarker = "…[cut]"
)

// batchProblem returns the argument problem for a call carrying more
// than maxBatchUIDs UIDs, or "" when it carries no more. outcome
// finishes the sentence.
func batchProblem(tool string, n int, outcome string) string {
	if n <= maxBatchUIDs {
		return ""
	}
	return fmt.Sprintf("uids lists %d messages, %d over the %d one %s call takes: split them across calls of at most %d each; %s", n, n-maxBatchUIDs, maxBatchUIDs, tool, maxBatchUIDs, outcome)
}

// clipField cuts s to at most maxMoveFieldOutput bytes on a rune
// boundary, ending a cut value with fieldCutMarker.
func clipField(s string) string {
	if len(s) <= maxMoveFieldOutput {
		return s
	}
	return truncateUTF8(s, maxMoveFieldOutput-len(fieldCutMarker)) + fieldCutMarker
}

// marshalMoveResponse renders an email_move result within
// maxMoveOutput: it clips each entry's text fields, then sheds entry
// detail from the end of the lists until the result fits.
func marshalMoveResponse(resp moveResponse) (string, error) {
	resp.Moved = slices.Clone(resp.Moved)
	resp.Refused = slices.Clone(resp.Refused)
	for i := range resp.Moved {
		resp.Moved[i].From = clipField(resp.Moved[i].From)
		resp.Moved[i].MessageID = clipField(resp.Moved[i].MessageID)
	}
	for i := range resp.Refused {
		resp.Refused[i].From = clipField(resp.Refused[i].From)
		resp.Refused[i].Reason = clipField(resp.Refused[i].Reason)
	}
	for {
		data, err := marshalResponse(resp)
		if err != nil || len(data) <= maxMoveOutput {
			return data, err
		}
		if !resp.shedDetail() {
			// Only UIDs and folder names are left, which the batch cap
			// keeps well under the budget.
			return data, nil
		}
	}
}

// shedDetail strips the detail from the last tenth, at least one, of
// the moved entries that still carry it, or of the refused entries once
// no moved entry does, and reports whether it stripped any. Moved
// detail goes first: a refused entry's reason and recovery say what to
// do next, while a moved entry's UIDs already say where it went.
func (r *moveResponse) shedDetail() bool {
	if kept := len(r.Moved) - r.MovedOmitted; kept > 0 {
		n := max(1, kept/10)
		for i := kept - n; i < kept; i++ {
			r.Moved[i] = movedMessage{UID: r.Moved[i].UID, DestinationUID: r.Moved[i].DestinationUID}
		}
		r.MovedOmitted += n
		return true
	}
	if kept := len(r.Refused) - r.RefusedOmitted; kept > 0 {
		n := max(1, kept/10)
		for i := kept - n; i < kept; i++ {
			r.Refused[i] = refusedMessage{UID: r.Refused[i].UID}
		}
		r.RefusedOmitted += n
		return true
	}
	return false
}

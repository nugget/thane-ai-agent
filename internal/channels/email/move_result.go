package email

import (
	"fmt"
	"slices"
	"strings"
)

// Bounds on email_move and email_mark, held to the tool-result sizes
// AGENTS.md sets. One call carries at most maxBatchUIDs UIDs. An
// email_move result is held to maxMoveOutput. Every text field is
// clipped to maxMoveFieldOutput: the result's own action, account,
// folder names, and note, and each entry's from, message_id, and
// reason. Past the budget, the entries at the end of moved, then of
// refused, keep only their UIDs and are counted in moved_omitted and
// refused_omitted. uids, destination_uids, and each entry's uid and
// destination_uid are what a later turn needs to find the messages
// again or undo the move, and any batch a call can carry fits with
// every one of them. Only a server whose COPYUID names more messages
// than the call sent can pass the budget after that, and then the lists
// are cut short as a last resort (see [moveResponse.cutLists]).

const (
	// maxBatchUIDs is how many UIDs one email_move or email_mark call
	// may carry.
	maxBatchUIDs = 100

	// maxMoveOutput is the size an email_move result is held to, the
	// same as a list or search result's.
	maxMoveOutput = maxListOutput

	// maxMoveFieldOutput bounds each text field of an email_move result,
	// marker included.
	maxMoveFieldOutput = 256

	// fieldCutMarker ends a field clipped to maxMoveFieldOutput.
	fieldCutMarker = "…[cut]"
)

// moveCutNote is the note, or the end of it, of a result whose lists
// were cut short to fit maxMoveOutput.
var moveCutNote = fmt.Sprintf("this result would pass %d KB even with every entry reduced to its UIDs, so its lists were cut short from the end; list source_folder and destination_folder to see where each message is", maxMoveOutput/1024)

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
	return clipTo(s, maxMoveFieldOutput)
}

// marshalMoveResponse renders an email_move result within
// maxMoveOutput: it clips every text field, sheds entry detail from the
// end of the lists until the result fits, and cuts the lists short only
// when that is not enough.
func marshalMoveResponse(resp moveResponse) (string, error) {
	resp = clipMoveFields(resp)
	for {
		data, err := marshalResponse(resp)
		if err != nil || len(data) <= maxMoveOutput {
			return data, err
		}
		if !resp.shedDetail() && !resp.cutLists() {
			// Not reached: with every list empty only the clipped
			// strings are left, about half the budget even when JSON
			// takes six bytes to escape each of their bytes.
			return data, nil
		}
	}
}

// clipMoveFields clips every text field of resp to maxMoveFieldOutput,
// on copies of its entry lists so the caller's stay whole.
func clipMoveFields(resp moveResponse) moveResponse {
	for _, s := range []*string{&resp.Action, &resp.Account, &resp.SourceFolder, &resp.DestinationFolder, &resp.Note} {
		*s = clipField(*s)
	}
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
	return resp
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

// cutLists is the last resort for a result that passes maxMoveOutput
// with every entry reduced to its UIDs, which only a server whose
// COPYUID names more messages than the call sent can cause. It cuts the
// last tenth, at least one, from the first of these that is not empty:
// moved, whose entries repeat uids and destination_uids; uids and
// destination_uids together, so they stay paired; uids_not_found;
// refused. moved_omitted and refused_omitted follow their lists, and
// the note ends with moveCutNote. It reports whether it cut anything.
func (r *moveResponse) cutLists() bool {
	switch {
	case len(r.Moved) > 0:
		r.Moved = cutTail(r.Moved)
		r.MovedOmitted = min(r.MovedOmitted, len(r.Moved))
	case len(r.UIDs) > 0 || len(r.DestinationUIDs) > 0:
		r.UIDs = cutTail(r.UIDs)
		r.DestinationUIDs = cutTail(r.DestinationUIDs)
	case len(r.UIDsNotFound) > 0:
		r.UIDsNotFound = cutTail(r.UIDsNotFound)
	case len(r.Refused) > 0:
		r.Refused = cutTail(r.Refused)
		r.RefusedOmitted = min(r.RefusedOmitted, len(r.Refused))
	default:
		return false
	}
	switch {
	case r.Note == "":
		r.Note = moveCutNote
	case !strings.HasSuffix(r.Note, moveCutNote):
		r.Note += "; " + moveCutNote
	}
	return true
}

// cutTail drops the last tenth, at least one, of s. An empty s stays
// as it is.
func cutTail[T any](s []T) []T {
	if len(s) == 0 {
		return s
	}
	return s[:len(s)-max(1, len(s)/10)]
}

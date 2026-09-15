package email

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// A claim in Thane's label record holds for one copy of a message: the
// one Thane marked, named by its folder, that folder's UIDVALIDITY, and
// its UID. No other message ever has all three, because a server never
// reuses a UID under one UIDVALIDITY and gives a rebuilt folder a new
// one. A second copy under the same Message-ID, even one byte for byte
// the same, is another message, and so is a message after a move, which
// gives it a new UID. So when anyone else moves a message, Thane's claim
// lapses and the marks it carries read as the operator's from then on.
// email_move carries the claim to the new copy only when the server
// names that copy (COPYUID) and the move took no other copy under the
// same Message-ID.

// messageCopy names one copy of a message within an account.
type messageCopy struct {
	Folder      string `json:"folder"`
	UIDValidity uint32 `json:"uid_validity"`
	UID         uint32 `json:"uid"`
}

// newMessageCopy names the copy at uid in folder under uidValidity.
// INBOX is spelled one way whatever case it was given in (RFC 3501);
// every other folder name is kept as spelled.
func newMessageCopy(folder string, uidValidity, uid uint32) messageCopy {
	return messageCopy{Folder: verdictKey(folder), UIDValidity: uidValidity, UID: uid}
}

// known reports whether c names a copy, with all three parts set. A
// folder whose server reports no UIDVALIDITY names no copy, so Thane
// marks nothing there and claims nothing.
func (c messageCopy) known() bool {
	return c.Folder != "" && c.UIDValidity != 0 && c.UID != 0
}

// copyOf names the copy at uid in the session's folder.
func (s *flagSession) copyOf(uid uint32) messageCopy {
	return newMessageCopy(s.folder, s.uidValidity, uid)
}

// covers reports whether the record describes copy c. A record bound to
// no copy describes any copy while it claims no mark; one claiming a
// mark without naming its copy describes none, so its marks read as the
// operator's.
func (m labelMarks) covers(c messageCopy) bool {
	if !c.known() {
		return false
	}
	if m.Copy == (messageCopy{}) {
		return !m.claims()
	}
	return m.Copy == c
}

// claims reports whether the record claims any mark on its copy.
func (m labelMarks) claims() bool {
	return len(m.Keywords) > 0 || m.SetFlagged || m.Color != ""
}

// bind ties the record to the copy Thane is about to mark. A record
// already bound keeps its copy.
func (m *labelMarks) bind(c messageCopy) {
	if m.Copy == (messageCopy{}) {
		m.Copy = c
	}
}

// carryLabelClaims moves Thane's claims on the messages a move filed to
// their new copies, pairing each source UID with the destination UID
// the server's COPYUID gave it. Without COPYUID nothing is carried: the
// claims stay on copies that no longer exist, so they lapse and the
// moved marks read as the operator's. A claim that cannot be carried
// lapses the same way and is logged; the move has happened either way.
//
// The pairing is only as good as go-imap keeps it, and it sorts each
// COPYUID set as it parses it, so result pairs the smallest source UID
// with the smallest destination UID whatever order the server paired
// them in (RFC 4315 pairs them by position). Between messages under
// different Message-IDs a wrong pairing only lapses a claim, since a
// record is looked up by its own Message-ID. Between two copies under
// one Message-ID it would hand Thane's claim to the other copy, so a
// claim is carried only for a Message-ID the move took one copy of.
func (s *Service) carryLabelClaims(ctx context.Context, account string, result MoveResult, senders map[uint32]moveSender) {
	if s.state == nil || !result.DestUIDsKnown {
		return
	}
	copies := movedCopies(result.UIDs, senders)
	for i, uid := range result.UIDs {
		if i >= len(result.DestUIDs) {
			return
		}
		sender, ok := senders[uid]
		if !ok || sender.envelope.MessageID == "" {
			continue
		}
		from := newMessageCopy(result.SourceFolder, result.SourceUIDValidity, uid)
		to := newMessageCopy(result.Destination, result.DestUIDValidity, result.DestUIDs[i])
		carried, err := s.carryLabelClaim(ctx, account, sender.envelope.MessageID, from, to, copies(sender.envelope.MessageID))
		if err != nil {
			s.logger.Warn("email label claim not carried by the move; Thane will read the moved message's marks as the operator's",
				"account", account,
				"source_folder", from.Folder,
				"uid", from.UID,
				"destination_folder", to.Folder,
				"destination_uid", to.UID,
				"message_id", sender.envelope.MessageID,
				"error", err,
			)
			continue
		}
		if carried {
			s.logger.Info("email label claim carried by the move",
				"account", account,
				"source_folder", from.Folder,
				"uid", from.UID,
				"destination_folder", to.Folder,
				"destination_uid", to.UID,
				"destination_uid_validity", to.UIDValidity,
				"message_id", sender.envelope.MessageID,
				"loop_id", tools.LoopIDFromContext(ctx),
			)
		}
	}
}

// movedCopies returns, for a Message-ID, how many of the moved UIDs may
// be copies under it: those whose envelope carries it, plus every moved
// UID whose envelope was not read, since that one could carry any
// Message-ID.
func movedCopies(uids []uint32, senders map[uint32]moveSender) func(messageID string) int {
	counts := make(map[string]int, len(uids))
	unread := 0
	for _, uid := range uids {
		sender, ok := senders[uid]
		if !ok {
			unread++
			continue
		}
		if id := normalizeMessageID(sender.envelope.MessageID); id != "" {
			counts[id]++
		}
	}
	return func(messageID string) int {
		return counts[normalizeMessageID(messageID)] + unread
	}
}

// carryLabelClaim rebinds the message's record from copy from to copy
// to when the record describes from, and reports whether it did. A
// record describing another copy, or none, is left alone. When the move
// took more than one copy that may share the Message-ID, which of them
// became to is unknown (see carryLabelClaims), so the claim is not
// carried and the error says why.
func (s *Service) carryLabelClaim(ctx context.Context, account, messageID string, from, to messageCopy, copies int) (bool, error) {
	if !from.known() || !to.known() {
		return false, nil
	}
	marks, err := loadLabelMarks(s.state, account, messageID)
	if err != nil {
		return false, err
	}
	if marks.Copy != from {
		return false, nil
	}
	if copies > 1 {
		return false, fmt.Errorf("the move took %d copies that may share this Message-ID, and go-imap does not keep which new UID the server gave each, so none can be proven to be the copy Thane marked", copies)
	}
	marks.Copy = to
	marks.By = draftAuthor(ctx)
	if err := saveLabelMarks(s.state, &marks); err != nil {
		return false, err
	}
	return true, nil
}

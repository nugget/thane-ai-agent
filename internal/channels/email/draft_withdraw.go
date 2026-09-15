package email

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Withdrawing a draft moves its one UID to the trash folder, and the
// server's OK proves only that the command ran: a UID another session
// removed after the proof moves as nothing, and the server still answers
// OK. So a withdrawal counts only when the server's COPYUID maps the
// recorded UID to a UID in the trash. Nothing else shows whose move put
// the draft there: the draft's exact bytes in the trash are also what
// the operator's client leaves when it files a sent draft there, or the
// old copy of one they edited, just before Thane's move. A move without
// that report is an unconfirmed withdrawal: the entry is not closed as
// withdrawn, reconcile closes it as gone once its UID is gone, and the
// result never says the draft will not be sent. A server without
// UIDPLUS never reports COPYUID, so withdrawal is refused there before
// anything moves.

// withdrawOutcome is what email_draft_withdraw's IMAP step did.
type withdrawOutcome struct {
	// NoUIDPlus is true when the server advertises neither UIDPLUS nor
	// IMAP4rev2, so nothing was proven or moved.
	NoUIDPlus bool

	// Verdict is the identity proof's answer before the move; the move
	// ran only when it is ownershipProven.
	Verdict ownershipVerdict

	// Moved is true once the server answered the MOVE with OK, so an
	// error after it comes from checking the move, not from making it.
	Moved bool

	// Confirmed is true when the server's COPYUID reported the move
	// carried this draft into the trash folder, where it is at TrashUID.
	Confirmed bool
	TrashUID  uint32

	// SourceGone, for a move that was not confirmed, is true when the
	// recorded UID is no longer in the drafts folder. It says the draft
	// left, not where it went or whose move took it.
	SourceGone bool
}

// withdrawDraft proves e's draft, moves that one UID into trash, and
// reads the server's COPYUID for it, under one hold of the lock. A
// server without UIDPLUS is answered NoUIDPlus before anything moves.
// A UIDPLUS server without MOVE gets go-imap's COPY, STORE, and UID
// EXPUNGE of that one UID, whose COPY reports the COPYUID.
func (c *Client) withdrawDraft(ctx context.Context, e draftEntry, trash string) (withdrawOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnected(ctx); err != nil {
		return withdrawOutcome{}, err
	}
	if !c.capsLocked(ctx).Has(imap.CapUIDPlus) {
		return withdrawOutcome{NoUIDPlus: true}, nil
	}
	state, err := c.draftFolderStateLocked(ctx, e.Folder, false, []uint32{e.UID})
	if err != nil {
		return withdrawOutcome{}, err
	}
	if verdict := proveOwnership(e, state); verdict != ownershipProven {
		return withdrawOutcome{Verdict: verdict}, nil
	}
	set := imap.UIDSet{}
	set.AddNum(imap.UID(e.UID))
	release := c.guard(ctx)
	data, err := c.client.Move(set, trash).Wait()
	release()
	if err != nil {
		return withdrawOutcome{}, c.folderError(ctx, "move draft to trash folder", trash, err)
	}
	out := withdrawOutcome{Verdict: ownershipProven, Moved: true}
	if uid, ok := copyUIDFor(data, e.UID); ok {
		out.Confirmed, out.TrashUID = true, uid
		return out, nil
	}
	out.SourceGone, err = c.sourceGoneLocked(ctx, e, state.Folder)
	return out, err
}

// copyUIDFor returns the destination UID a MOVE's COPYUID gives source
// UID uid, when the COPYUID names exactly that one source UID.
func copyUIDFor(data *imapclient.MoveData, uid uint32) (uint32, bool) {
	if data == nil {
		return 0, false
	}
	src, ok := data.SourceUIDs.(imap.UIDSet)
	if !ok {
		return 0, false
	}
	dst, ok := data.DestUIDs.(imap.UIDSet)
	if !ok {
		return 0, false
	}
	srcUIDs, srcComplete := src.Nums()
	dstUIDs, dstComplete := dst.Nums()
	if !srcComplete || !dstComplete || len(srcUIDs) != 1 || len(dstUIDs) != 1 || uint32(srcUIDs[0]) != uid || dstUIDs[0] == 0 {
		return 0, false
	}
	return uint32(dstUIDs[0]), true
}

// sourceGoneLocked reports whether e's UID has left folder, which is
// still selected, after a move the server answered without a COPYUID
// for it. Caller must hold c.mu.
func (c *Client) sourceGoneLocked(ctx context.Context, e draftEntry, folder string) (bool, error) {
	set := imap.UIDSet{}
	set.AddNum(imap.UID(e.UID))
	release := c.guard(ctx)
	flags, err := c.fetchFlags(set)
	release()
	if err != nil {
		return false, c.wrap(ctx, "check the draft left the drafts folder", folder, e.UID, err)
	}
	_, still := flags[e.UID]
	return !still, nil
}

// withdrawConfirmError is the error for a withdrawal whose MOVE the
// server accepted without reporting that it carried the draft, and
// whose check of the drafts folder then failed: the ledger was not
// changed, and the draft may be in either folder.
func withdrawConfirmError(e draftEntry, trash string, err error) error {
	return fmt.Errorf("draft %s: the server accepted the move to %q without reporting that it carried this draft, and checking whether the draft is still in %q failed (%w). Its entry was not marked withdrawn, and the draft may still be in %q or may already be gone; call email_drafts to see where it stands before withdrawing it again", e.ID, trash, e.Folder, err, e.Folder)
}

// unconfirmedWithdrawal answers a withdrawal whose move was accepted but
// not reported to carry the draft. The entry is left as it is: reconcile
// closes it as gone once its UID is gone, and a draft still at its UID
// stays open and Thane's.
func (s *Service) unconfirmedWithdrawal(ctx context.Context, tool string, e draftEntry, trash string, out withdrawOutcome) (string, error) {
	s.logger.Warn("email draft withdrawal unconfirmed",
		"draft_id", e.ID,
		"account", e.Account,
		"folder", e.Folder,
		"uid", e.UID,
		"trash_folder", trash,
		"source_gone", out.SourceGone,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	s.recordOp(tool, e.Account, trash, e.ID+" unconfirmed")
	note := fmt.Sprintf("Thane could not confirm this draft was withdrawn. The server accepted the move to %q without reporting that it carried this draft, and the draft is still at uid %d in %q, so it can still be sent; its entry stays open and it is still Thane's. Tell the operator this draft should not be sent.", trash, e.UID, e.Folder)
	if out.SourceGone {
		note = fmt.Sprintf("Thane could not confirm this draft was withdrawn. The server accepted the move to %q without reporting that it carried this draft, and the draft is no longer in %q: most likely the operator sent or discarded it first, leaving the move nothing to carry. A copy of it in %q would not show otherwise, because the operator's client may have filed it there. Its entry was not marked withdrawn; the next draft tool call closes it as gone. If it must not go out, tell the operator, who can check whether it was sent.", trash, e.Folder, trash)
	}
	return marshalResponse(draftWithdrawnView{
		Action:       "unconfirmed",
		DraftID:      e.ID,
		Account:      e.Account,
		DraftsFolder: e.Folder,
		TrashFolder:  trash,
		Note:         note,
	})
}

package email

import (
	"context"
)

// The two removals crash recovery may make (draft_recover.go), each
// proved and made under one hold of the client lock.

// removeProvenCopy removes the new version a write-ahead record names,
// proven three ways: it is at the recorded UID under the recorded
// UIDVALIDITY, it carries the recorded Message-ID, and its complete
// bytes hash to sum, the digest of the bytes Thane appended. It must
// also not be marked \Deleted, because that flag could be the operator's
// client discarding it, and recovery never expunges what the operator
// marked. removed is false, and the message is left as it is, when any
// of that does not hold or the removal did not.
func (c *Client) removeProvenCopy(ctx context.Context, copyOf draftEntry, sum string) (removed bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnected(ctx); err != nil {
		return false, err
	}
	state, err := c.draftFolderStateLocked(ctx, copyOf.Folder, false, []uint32{copyOf.UID})
	if err != nil {
		return false, err
	}
	if proveOwnership(copyOf, state) != ownershipProven || sum == "" {
		return false, nil
	}
	raw, err := c.fetchRawLocked(ctx, state.Folder, copyOf.UID)
	if err != nil {
		return false, err
	}
	if raw == nil || contentSum(raw) != sum {
		return false, nil
	}
	return c.removeOwnLocked(ctx, state.Folder, copyOf.UID, state.Messages[copyOf.UID].ModSeq)
}

// expungeRetiredDraft finishes the removal a retiring write-ahead record
// began: e's draft must still be at its UID with its Message-ID and
// marked \Deleted, the flag the record says Thane was setting, and then
// that one UID is expunged. gone is true when the draft is no longer
// there afterwards, including when it was already gone; it is false, and
// nothing is changed, when the draft is there without the flag, which
// means someone cleared it since.
func (c *Client) expungeRetiredDraft(ctx context.Context, e draftEntry) (gone bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnected(ctx); err != nil {
		return false, err
	}
	state, err := c.draftFolderStateLocked(ctx, e.Folder, false, []uint32{e.UID})
	if err != nil {
		return false, err
	}
	switch proveOwnership(e, state) {
	case ownershipVanished:
		return true, nil
	case ownershipDeleted:
		return c.expungeOwnLocked(ctx, state.Folder, e.UID, 0)
	}
	return false, nil
}

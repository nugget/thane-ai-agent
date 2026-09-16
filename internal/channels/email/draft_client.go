package email

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// The IMAP work behind the draft ledger. Each method that takes c.mu
// holds it for its whole run, so a proof and the change it licenses see
// the same session, and every change names its UIDs: no method here
// issues a plain EXPUNGE or touches a message it did not name.

// draftMessage is one message in a drafts folder as the identity proof
// reads it.
type draftMessage struct {
	MessageID string
	Flags     []imap.Flag

	// ModSeq is the message's CONDSTORE mod-sequence, or zero when the
	// server has no CONDSTORE.
	ModSeq uint64
}

// draftFolderState is what one SELECT or EXAMINE and one UID FETCH saw
// in a drafts folder: its UIDVALIDITY and the named UIDs that exist.
type draftFolderState struct {
	Folder      string
	UIDValidity uint32
	Messages    map[uint32]draftMessage
}

// draftFolderState examines folder and fetches only the given UIDs,
// which is how reconcile checks the ledger without trusting a capped
// listing.
func (c *Client) draftFolderState(ctx context.Context, folder string, uids []uint32) (draftFolderState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnected(ctx); err != nil {
		return draftFolderState{}, err
	}
	return c.draftFolderStateLocked(ctx, folder, true, uids)
}

// draftFolderStateLocked selects folder (read-only when readOnly) and
// fetches the given UIDs. Caller must hold c.mu and have called
// ensureConnected.
func (c *Client) draftFolderStateLocked(ctx context.Context, folder string, readOnly bool, uids []uint32) (draftFolderState, error) {
	folder = normalizeFolder(folder)
	selected, err := c.selectFolder(ctx, folder, readOnly)
	if err != nil {
		return draftFolderState{}, err
	}
	state := draftFolderState{Folder: folder, UIDValidity: selected.UIDValidity, Messages: map[uint32]draftMessage{}}
	set := imap.UIDSet{}
	for _, uid := range uids {
		if uid != 0 {
			set.AddNum(imap.UID(uid))
		}
	}
	if len(set) == 0 {
		return state, nil
	}
	state.Messages, err = c.fetchDraftMessagesLocked(ctx, folder, set)
	return state, err
}

// fetchDraftMessagesLocked fetches the Message-ID, flags, and, with
// CONDSTORE, mod-sequence of every UID in set that exists in the
// selected folder. Caller must hold c.mu and have selected the folder.
func (c *Client) fetchDraftMessagesLocked(ctx context.Context, folder string, set imap.UIDSet) (map[uint32]draftMessage, error) {
	opts := &imap.FetchOptions{UID: true, Envelope: true, Flags: true, ModSeq: c.capsLocked(ctx).Has(imap.CapCondStore)}
	release := c.guard(ctx)
	defer release()
	cmd := c.client.Fetch(set, opts)
	out := make(map[uint32]draftMessage)
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		var uid uint32
		var m draftMessage
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch data := item.(type) {
			case imapclient.FetchItemDataUID:
				uid = uint32(data.UID)
			case imapclient.FetchItemDataEnvelope:
				if data.Envelope != nil {
					m.MessageID = data.Envelope.MessageID
				}
			case imapclient.FetchItemDataFlags:
				m.Flags = data.Flags
			case imapclient.FetchItemDataModSeq:
				m.ModSeq = data.ModSeq
			case imapclient.FetchItemDataBodySection:
				drainLiteral(data.Literal)
			}
		}
		if uid != 0 {
			out[uid] = m
		}
	}
	if err := cmd.Close(); err != nil {
		return nil, c.wrap(ctx, "fetch drafts", folder, 0, err)
	}
	return out, nil
}

// capsLocked reads the server's capabilities under a guard. Caller must
// hold c.mu and have called ensureConnected, and must not hold a guard.
func (c *Client) capsLocked(ctx context.Context) imap.CapSet {
	release := c.guard(ctx)
	defer release()
	return c.client.Caps()
}

// draftsMatching examines folder and returns every message any of the
// header criteria matches, each searched on its own, so a caller can
// see the drafts that carry a Message-ID or answer a message.
func (c *Client) draftsMatching(ctx context.Context, folder string, fields []imap.SearchCriteriaHeaderField) (draftFolderState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnected(ctx); err != nil {
		return draftFolderState{}, err
	}
	folder = normalizeFolder(folder)
	selected, err := c.selectFolder(ctx, folder, true)
	if err != nil {
		return draftFolderState{}, err
	}
	state := draftFolderState{Folder: folder, UIDValidity: selected.UIDValidity, Messages: map[uint32]draftMessage{}}
	set := imap.UIDSet{}
	for _, field := range fields {
		uids, err := c.searchUIDs(ctx, folder, &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{field}})
		if err != nil {
			return draftFolderState{}, err
		}
		set.AddNum(uids...)
	}
	if len(set) == 0 {
		return state, nil
	}
	state.Messages, err = c.fetchDraftMessagesLocked(ctx, folder, set)
	return state, err
}

// findProvenCopy returns the UID of the message in folder that carries
// messageID and whose complete bytes hash to sum, with the folder's
// UIDVALIDITY, or UID 0 when none does. The bytes are the proof: a
// message holding exactly the bytes Thane wrote, under a Message-ID
// Thane generated for them, is Thane's copy.
func (c *Client) findProvenCopy(ctx context.Context, folder, messageID, sum string) (uint32, uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureConnected(ctx); err != nil {
		return 0, 0, err
	}
	folder = normalizeFolder(folder)
	selected, err := c.selectFolder(ctx, folder, true)
	if err != nil {
		return 0, 0, err
	}
	uids, err := c.searchUIDs(ctx, folder, &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: bracketed(messageID)}}})
	if err != nil {
		return 0, 0, err
	}
	for _, uid := range uids {
		raw, err := c.fetchRawLocked(ctx, folder, uint32(uid))
		if err != nil {
			return 0, 0, err
		}
		if raw != nil && contentSum(raw) == sum {
			return uint32(uid), selected.UIDValidity, nil
		}
	}
	return 0, selected.UIDValidity, nil
}

// fetchRawLocked returns one message's complete bytes, read with PEEK,
// or nil when the UID does not exist; bytes past maxRawMessageSize are
// drained and left out, so an oversized message never hashes as proven.
// Caller must hold c.mu and have selected the folder.
func (c *Client) fetchRawLocked(ctx context.Context, folder string, uid uint32) ([]byte, error) {
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	release := c.guard(ctx)
	defer release()
	cmd := c.client.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}}})
	var raw []byte
	var readErr error
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			if data, ok := item.(imapclient.FetchItemDataBodySection); ok && data.Literal != nil {
				raw, readErr = io.ReadAll(io.LimitReader(data.Literal, maxRawMessageSize))
				if rest, _ := io.Copy(io.Discard, data.Literal); rest > 0 {
					raw = nil
				}
			}
		}
	}
	if err := cmd.Close(); err != nil {
		return nil, c.wrap(ctx, "fetch draft", folder, uid, err)
	}
	if readErr != nil {
		return nil, c.wrap(ctx, "read draft", folder, uid, readErr)
	}
	return raw, nil
}

// contentSum is the hex SHA-256 of a message's bytes.
func contentSum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// removeOwnLocked deletes one message the caller has proven Thane's
// from the selected folder, after checking it is still there and not
// yet marked \Deleted: a flag already set could be the operator's
// client discarding the draft, and the read-back in expungeOwnLocked
// could not tell it from Thane's own. It reports false, and leaves the
// message as it is, when the check or the removal does not hold. Caller
// must hold c.mu and have selected the folder read-write.
func (c *Client) removeOwnLocked(ctx context.Context, folder string, uid uint32, modSeq uint64) (bool, error) {
	unflagged, err := c.unflaggedLocked(ctx, folder, uid)
	if err != nil || !unflagged {
		return false, err
	}
	return c.expungeOwnLocked(ctx, folder, uid, modSeq)
}

// unflaggedLocked reports whether uid is still in the selected folder
// and not marked \Deleted, the check removeOwnLocked makes before its
// store. Caller must hold c.mu and have selected the folder.
func (c *Client) unflaggedLocked(ctx context.Context, folder string, uid uint32) (bool, error) {
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	release := c.guard(ctx)
	before, err := c.fetchFlags(set)
	release()
	if err != nil {
		return false, c.wrap(ctx, "check draft flags", folder, uid, err)
	}
	have, present := before[uid]
	return present && !flagIn(have, imap.FlagDeleted), nil
}

// expungeOwnLocked deletes one message the caller has proven Thane's
// from the selected folder: flagOwnLocked, then expungeFlaggedLocked.
// It reports false when the flag did not hold or the message outlived
// the expunge, which means someone changed it in between. Caller must
// hold c.mu and have selected the folder read-write.
func (c *Client) expungeOwnLocked(ctx context.Context, folder string, uid uint32, modSeq uint64) (bool, error) {
	flagged, err := c.flagOwnLocked(ctx, folder, uid, modSeq)
	if err != nil || !flagged {
		return false, err
	}
	return c.expungeFlaggedLocked(ctx, folder, uid)
}

// flagOwnLocked marks one message the caller has proven Thane's
// \Deleted: UID STORE +FLAGS.SILENT (\Deleted), with UNCHANGEDSINCE when
// the server has CONDSTORE and modSeq is known, and a read-back that the
// flag holds. It reports false when the read-back does not show the
// flag. Caller must hold c.mu and have selected the folder read-write.
func (c *Client) flagOwnLocked(ctx context.Context, folder string, uid uint32, modSeq uint64) (bool, error) {
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	var opts *imap.StoreOptions
	if modSeq != 0 && c.capsLocked(ctx).Has(imap.CapCondStore) {
		opts = &imap.StoreOptions{UnchangedSince: modSeq}
	}
	release := c.guard(ctx)
	err := c.client.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, opts).Close()
	var flags map[uint32][]imap.Flag
	if err == nil {
		flags, err = c.fetchFlags(set)
	}
	release()
	if err != nil {
		return false, c.wrap(ctx, "flag draft \\Deleted", folder, uid, err)
	}
	have, ok := flags[uid]
	return ok && flagIn(have, imap.FlagDeleted), nil
}

// expungeFlaggedLocked removes one message flagOwnLocked flagged: UID
// EXPUNGE of that UID alone, which never removes another message anyone
// flagged \Deleted, and a read-back, because UID EXPUNGE of a UID that
// is already gone still answers OK. It reports false when the message
// outlived the expunge. Caller must hold c.mu and have selected the
// folder read-write.
func (c *Client) expungeFlaggedLocked(ctx context.Context, folder string, uid uint32) (bool, error) {
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	release := c.guard(ctx)
	err := c.client.UIDExpunge(set).Close()
	var flags map[uint32][]imap.Flag
	if err == nil {
		flags, err = c.fetchFlags(set)
	}
	release()
	if err != nil {
		return false, c.wrap(ctx, "expunge draft", folder, uid, err)
	}
	_, survived := flags[uid]
	return !survived, nil
}

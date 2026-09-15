package email

import (
	"context"

	"github.com/emersion/go-imap/v2"
)

// move runs one validated email_move. Before anything moves it resolves
// the drafts folder once and the destination, applies the drafts
// protections, applies the account's filing policy in a turn the
// operator is not present for, reads each message's sender, and lets
// the junk guard hold back what it must; then it moves the rest and
// reports every moved message with its sender.
func (t *Tools) move(ctx context.Context, acct ResolvedAccount, req moveRequest) (moveResponse, error) {
	s := t.service
	r := s.newFolderResolver(acct)
	opts := req.opts
	opts.Folder = normalizeFolder(opts.Folder)

	drafts, err := s.protectedDrafts(ctx, "email_move", acct, r, "nothing was moved")
	if err != nil {
		return moveResponse{}, err
	}
	dest, err := s.resolveMoveDestination(ctx, acct, r, req, drafts)
	if err != nil {
		return moveResponse{}, err
	}
	opts.Destination = dest
	if err := s.refuseDraftsDestination(ctx, acct, drafts, opts.Folder, dest); err != nil {
		return moveResponse{}, err
	}
	if err := s.refuseDraftsSource(ctx, "email_move", acct, drafts, opts.Folder, "nothing was moved"); err != nil {
		return moveResponse{}, err
	}
	policy := resolveFilingPolicy(acct.Config, func(role FolderRole) string { return r.folder(ctx, role) })
	if r.err != nil {
		return moveResponse{}, r.err
	}
	// move_into binds only turns the operator is not present for. In
	// theirs the move goes ahead and the log records that it went
	// outside the rule; the drafts protections above hold in both.
	if attended(ctx) {
		s.logMoveOutsideMoveInto(ctx, acct, policy, opts.Folder, dest)
	} else if err := s.refuseFiling(ctx, acct, policy, opts.Folder, dest); err != nil {
		return moveResponse{}, err
	}

	senders, err := t.moveSenders(ctx, acct, opts.Folder, opts.UIDs)
	if err != nil {
		return moveResponse{}, err
	}
	var refused []refusedMessage
	if !attended(ctx) {
		junk := r.folder(ctx, RoleJunk)
		if r.err != nil {
			return moveResponse{}, r.err
		}
		if junk != "" && namesFolder(dest, junk) {
			opts.UIDs, refused = s.guardJunk(ctx, acct, opts.UIDs, senders)
		}
	}
	if len(opts.UIDs) == 0 {
		return moveResponse{
			Action:               "refused",
			Account:              acct.Name,
			SourceFolder:         opts.Folder,
			DestinationFolder:    dest,
			UIDs:                 []uint32{},
			DestinationUIDs:      []uint32{},
			DestinationUIDsKnown: true,
			UIDsNotFound:         []uint32{},
			Moved:                []movedMessage{},
			Refused:              refused,
			Note:                 "the junk guard refused every message, so nothing was moved",
		}, nil
	}

	result, err := acct.Client.MoveMessages(ctx, opts)
	if err != nil {
		return moveResponse{}, err
	}
	// Thane's label claims follow the messages only to copies the server
	// named (labels_copy.go).
	s.carryLabelClaims(ctx, acct.Name, result, senders)
	resp := moveResponse{
		Action:               "moved",
		Account:              acct.Name,
		SourceFolder:         result.SourceFolder,
		DestinationFolder:    result.Destination,
		UIDs:                 nonNilUIDs(result.UIDs),
		DestinationUIDs:      nonNilUIDs(result.DestUIDs),
		DestinationUIDsKnown: result.DestUIDsKnown,
		UIDsNotFound:         []uint32{},
		Moved:                movedMessages(result, senders),
		Refused:              refused,
	}
	if resp.Refused == nil {
		resp.Refused = []refusedMessage{}
	}
	if result.DestUIDsKnown {
		// COPYUID named what moved; a UID sent to the move and absent
		// from it was not in the folder. A UID the guard refused was
		// never sent, so it is under refused, not here.
		resp.UIDsNotFound = nonNilUIDs(missingUIDs(opts.UIDs, result.UIDs))
	} else {
		resp.Note = "the server did not confirm which UIDs moved or their new UIDs; list " + result.Destination + " to check"
	}
	s.recordOp("email_move", acct.Name, result.SourceFolder, moveOperationRef(result))
	return resp, nil
}

// moveSender is what a move knows about one message before moving it:
// its envelope and the contact directory's answer about its sender.
type moveSender struct {
	envelope Envelope
	match    ContactMatch
}

// moveSenders reads the envelopes of the UIDs to move and resolves each
// sender the way list, read, and the poller do. A UID the folder does
// not hold has no entry.
func (t *Tools) moveSenders(ctx context.Context, acct ResolvedAccount, folder string, uids []uint32) (map[uint32]moveSender, error) {
	envelopes, err := acct.Client.FetchEnvelopes(ctx, folder, uids)
	if err != nil {
		return nil, err
	}
	lookup := newIdentityLookup(ctx, t.contacts, t.logger)
	senders := make(map[uint32]moveSender, len(envelopes))
	for _, env := range envelopes {
		senders[env.UID] = moveSender{envelope: env, match: lookup.resolve(env.From)}
	}
	return senders, nil
}

// movedMessage is one message a move filed: its UID in the source, its
// UID in the destination when the server confirmed it, and who sent it,
// so a later turn can find it again or undo the move. An entry past the
// result's size budget keeps only its UIDs (see marshalMoveResponse).
type movedMessage struct {
	UID            uint32 `json:"uid"`
	DestinationUID uint32 `json:"destination_uid,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	From           string `json:"from,omitempty"`
	TrustZone      string `json:"trust_zone,omitempty"`
}

// movedMessages pairs a move's result with the senders read before it.
// When the server confirmed nothing, the requested UIDs the folder held
// are listed without destination UIDs.
func movedMessages(result MoveResult, senders map[uint32]moveSender) []movedMessage {
	moved := make([]movedMessage, 0, len(result.UIDs))
	for i, uid := range result.UIDs {
		sender, ok := senders[uid]
		if !ok && !result.DestUIDsKnown {
			continue
		}
		m := movedMessage{UID: uid, MessageID: sender.envelope.MessageID, From: sender.envelope.From.String(), TrustZone: sender.match.TrustZone}
		if result.DestUIDsKnown && i < len(result.DestUIDs) {
			m.DestinationUID = result.DestUIDs[i]
		}
		moved = append(moved, m)
	}
	return moved
}

// FetchEnvelopes returns the envelopes of the given UIDs in folder,
// newest first, without changing any flag. A UID the folder does not
// hold is absent from the result rather than an error.
func (c *Client) FetchEnvelopes(ctx context.Context, folder string, uids []uint32) ([]Envelope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, err
	}
	folder = normalizeFolder(folder)
	if _, err := c.selectFolder(ctx, folder, true); err != nil {
		return nil, err
	}
	if len(uids) == 0 {
		return nil, nil
	}
	set := make([]imap.UID, len(uids))
	for i, uid := range uids {
		set[i] = imap.UID(uid)
	}
	return c.fetchEnvelopes(ctx, folder, set)
}

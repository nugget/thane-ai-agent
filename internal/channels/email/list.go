package email

import (
	"cmp"
	"context"
	"slices"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// ListMessages returns recent messages from a folder, newest first.
// When opts.Unseen is set only unseen messages are considered. When
// opts.SinceUID is set every message with a greater UID is returned
// and Limit is ignored, which is how the poller fetches exactly the
// messages newer than its mark. Otherwise the newest Limit messages
// are returned and [ListResult.TotalMatched] says how many there were.
func (c *Client) ListMessages(ctx context.Context, opts ListOptions) (ListResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return ListResult{}, err
	}

	folder := normalizeFolder(opts.Folder)
	limit := clampLimit(opts.Limit)

	c.logger.Debug("ListMessages",
		"folder", folder,
		"since_uid", opts.SinceUID,
		"unseen", opts.Unseen,
		"limit", limit,
	)

	selected, err := c.selectFolder(ctx, folder, true)
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{Folder: folder, UIDValidity: selected.UIDValidity}

	criteria := &imap.SearchCriteria{}
	if opts.Unseen {
		criteria.NotFlag = append(criteria.NotFlag, imap.FlagSeen)
	}
	// When SinceUID is set, use a server-side UID range to narrow the
	// search (avoids transferring every UID in large mailboxes). The
	// IMAP UID X:* range always matches at least the highest UID per
	// RFC 9051 §6.4.4 (when Start > highest, the range is swapped), so
	// we also filter client-side to exclude UIDs ≤ the threshold.
	if opts.SinceUID > 0 {
		criteria.UID = []imap.UIDSet{
			{imap.UIDRange{Start: imap.UID(opts.SinceUID + 1), Stop: 0}},
		}
	}

	allUIDs, err := c.searchUIDs(ctx, folder, criteria)
	if err != nil {
		return ListResult{}, err
	}
	if len(allUIDs) == 0 {
		return result, nil
	}

	var recentUIDs []imap.UID
	if opts.SinceUID > 0 {
		threshold := imap.UID(opts.SinceUID)
		for _, uid := range allUIDs {
			if uid > threshold {
				recentUIDs = append(recentUIDs, uid)
			}
		}
		result.TotalMatched = len(recentUIDs)
	} else {
		result.TotalMatched = len(allUIDs)
		start := 0
		if len(allUIDs) > limit {
			start = len(allUIDs) - limit
		}
		recentUIDs = allUIDs[start:]
	}

	if len(recentUIDs) == 0 {
		c.logger.Debug("no new UIDs after client-side filter")
		return result, nil
	}

	c.logger.Debug("fetching envelopes", "count", len(recentUIDs))
	envelopes, err := c.fetchEnvelopes(ctx, folder, recentUIDs)
	if err != nil {
		return ListResult{}, err
	}
	result.Envelopes = envelopes
	return result, nil
}

// searchUIDs runs a UID SEARCH in the selected folder and returns the
// matching UIDs ascending. Most servers return UIDs in ascending order
// but the protocol does not guarantee it, so the result is sorted.
// Caller must hold c.mu and have selected the folder.
func (c *Client) searchUIDs(ctx context.Context, folder string, criteria *imap.SearchCriteria) ([]imap.UID, error) {
	release := c.guard(ctx)
	data, err := c.client.UIDSearch(criteria, nil).Wait()
	release()
	if err != nil {
		return nil, c.wrap(ctx, "search folder", folder, 0, err)
	}
	uids := data.AllUIDs()
	slices.SortFunc(uids, func(a, b imap.UID) int {
		return cmp.Compare(a, b)
	})
	return uids, nil
}

// fetchEnvelopes fetches envelope data for the given UIDs and returns
// them newest-first. Caller must hold c.mu and have a selected folder.
func (c *Client) fetchEnvelopes(ctx context.Context, folder string, uids []imap.UID) ([]Envelope, error) {
	uidSet := imap.UIDSet{}
	uidSet.AddNum(uids...)

	fetchOpts := &imap.FetchOptions{
		UID:        true,
		Envelope:   true,
		Flags:      true,
		RFC822Size: true,
	}

	release := c.guard(ctx)
	defer release()

	fetchCmd := c.client.Fetch(uidSet, fetchOpts)
	var envelopes []Envelope
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		env, err := parseMessageData(msg)
		if err != nil {
			c.logger.Debug("skipping message", "error", err)
			continue
		}
		envelopes = append(envelopes, env)
	}
	if err := fetchCmd.Close(); err != nil {
		return nil, c.wrap(ctx, "fetch envelopes", folder, 0, err)
	}

	// Newest first: highest UID first.
	slices.SortFunc(envelopes, func(a, b Envelope) int {
		return cmp.Compare(b.UID, a.UID)
	})
	return envelopes, nil
}

// parseMessageData extracts an Envelope from IMAP fetch response items.
func parseMessageData(msg *imapclient.FetchMessageData) (Envelope, error) {
	var env Envelope

	for {
		item := msg.Next()
		if item == nil {
			break
		}

		switch data := item.(type) {
		case imapclient.FetchItemDataUID:
			env.UID = uint32(data.UID)
		case imapclient.FetchItemDataFlags:
			for _, f := range data.Flags {
				env.Flags = append(env.Flags, string(f))
			}
		case imapclient.FetchItemDataRFC822Size:
			env.Size = uint32(data.Size)
		case imapclient.FetchItemDataEnvelope:
			applyEnvelope(&env, data.Envelope)
		case imapclient.FetchItemDataBodySection:
			// Drain body section literal to avoid blocking the IMAP stream.
			drainLiteral(data.Literal)
		}
	}

	if env.UID == 0 {
		return env, errMissingUID
	}
	return env, nil
}

// applyEnvelope copies the IMAP ENVELOPE fields onto env.
func applyEnvelope(env *Envelope, data *imap.Envelope) {
	if data == nil {
		return
	}
	env.Date = data.Date
	env.Subject = data.Subject
	env.MessageID = data.MessageID
	env.InReplyTo = data.InReplyTo
	if from := imapAddresses(data.From); len(from) > 0 {
		env.From = from[0]
	} else if sender := imapAddresses(data.Sender); len(sender) > 0 {
		env.From = sender[0]
	}
	env.ReplyTo = imapAddresses(data.ReplyTo)
	env.To = imapAddresses(data.To)
	env.Cc = imapAddresses(data.Cc)
}

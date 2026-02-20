package email

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// ListMessages returns recent messages from the specified folder.
// Messages are returned newest-first. When opts.Unseen is true, only
// messages without the \Seen flag are returned.
//
// When opts.SinceUID is set, only messages with UIDs strictly greater
// than that value are returned (ignoring Limit). This enables
// efficient polling without missing messages between cycles.
func (c *Client) ListMessages(ctx context.Context, opts ListOptions) ([]Envelope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, err
	}

	folder := opts.Folder
	if folder == "" {
		folder = "INBOX"
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	if _, err := c.selectFolder(folder); err != nil {
		return nil, err
	}

	// Build search criteria.
	criteria := &imap.SearchCriteria{}
	if opts.Unseen {
		criteria.NotFlag = append(criteria.NotFlag, imap.FlagSeen)
	}
	// UID-range filter: restrict to UIDs > SinceUID.
	if opts.SinceUID > 0 {
		criteria.UID = []imap.UIDSet{
			{imap.UIDRange{Start: imap.UID(opts.SinceUID + 1), Stop: 0}},
		}
	}

	searchCmd := c.client.UIDSearch(criteria, nil)
	searchData, err := searchCmd.Wait()
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", folder, err)
	}

	allUIDs := searchData.AllUIDs()
	if len(allUIDs) == 0 {
		return nil, nil
	}

	// When SinceUID is set, return all matching UIDs (no limit).
	// Otherwise take the most recent N UIDs (highest UIDs = newest).
	recentUIDs := allUIDs
	if opts.SinceUID == 0 {
		start := 0
		if len(allUIDs) > limit {
			start = len(allUIDs) - limit
		}
		recentUIDs = allUIDs[start:]
	}

	// Build UID set for fetch.
	uidSet := imap.UIDSet{}
	for _, uid := range recentUIDs {
		uidSet.AddNum(uid)
	}

	return c.fetchEnvelopes(uidSet)
}

// fetchEnvelopes fetches envelope data for the given UIDs and returns
// them newest-first. Caller must hold c.mu and have a selected folder.
func (c *Client) fetchEnvelopes(uidSet imap.UIDSet) ([]Envelope, error) {
	fetchOpts := &imap.FetchOptions{
		UID:        true,
		Envelope:   true,
		Flags:      true,
		RFC822Size: true,
	}

	fetchCmd := c.client.Fetch(uidSet, fetchOpts)

	var envelopes []Envelope
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		env, err := c.parseMessageData(msg)
		if err != nil {
			c.logger.Debug("skipping message", "error", err)
			continue
		}
		envelopes = append(envelopes, env)
	}

	if err := fetchCmd.Close(); err != nil {
		return nil, fmt.Errorf("fetch envelopes: %w", err)
	}

	// Sort newest-first by UID (descending).
	for i, j := 0, len(envelopes)-1; i < j; i, j = i+1, j-1 {
		envelopes[i], envelopes[j] = envelopes[j], envelopes[i]
	}

	return envelopes, nil
}

// parseMessageData extracts an Envelope from IMAP fetch response items.
func (c *Client) parseMessageData(msg *imapclient.FetchMessageData) (Envelope, error) {
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
			if data.Envelope != nil {
				env.Date = data.Envelope.Date
				env.Subject = data.Envelope.Subject

				if len(data.Envelope.From) > 0 {
					env.From = formatAddress(data.Envelope.From[0])
				}
				for _, addr := range data.Envelope.To {
					env.To = append(env.To, formatAddress(addr))
				}
			}
		case imapclient.FetchItemDataBodySection:
			// Drain body section literal to avoid blocking the IMAP stream.
			drainLiteral(data.Literal)
		}
	}

	if env.UID == 0 {
		return env, fmt.Errorf("message missing UID")
	}

	return env, nil
}

// formatAddress formats an IMAP address as "Name <user@host>" or
// just "user@host" if no name is set.
func formatAddress(addr imap.Address) string {
	email := addr.Addr()
	if addr.Name != "" {
		return fmt.Sprintf("%s <%s>", addr.Name, email)
	}
	return email
}

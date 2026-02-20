package email

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
)

// SearchMessages searches for messages matching the given criteria in
// the specified folder. Results are returned newest-first, limited to
// opts.Limit messages.
func (c *Client) SearchMessages(ctx context.Context, opts SearchOptions) ([]Envelope, error) {
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

	// Build IMAP search criteria.
	criteria := &imap.SearchCriteria{}

	if opts.Query != "" {
		criteria.Text = append(criteria.Text, opts.Query)
	}
	if opts.From != "" {
		criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{
			Key:   "From",
			Value: opts.From,
		})
	}
	if !opts.Since.IsZero() {
		criteria.Since = opts.Since
	}
	if !opts.Before.IsZero() {
		criteria.Before = opts.Before
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

	// Take the most recent N UIDs.
	start := 0
	if len(allUIDs) > limit {
		start = len(allUIDs) - limit
	}
	recentUIDs := allUIDs[start:]

	uidSet := imap.UIDSet{}
	for _, uid := range recentUIDs {
		uidSet.AddNum(uid)
	}

	return c.fetchEnvelopes(uidSet)
}

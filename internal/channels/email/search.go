package email

import (
	"context"

	"github.com/emersion/go-imap/v2"
)

// SearchMessages runs a server-side search in a folder and returns the
// newest matches first, at most Limit of them, with
// [ListResult.TotalMatched] reporting the full match count.
func (c *Client) SearchMessages(ctx context.Context, opts SearchOptions) (ListResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return ListResult{}, err
	}

	folder := normalizeFolder(opts.Folder)
	limit := clampLimit(opts.Limit)

	selected, err := c.selectFolder(ctx, folder, true)
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{Folder: folder, UIDValidity: selected.UIDValidity}

	criteria := searchCriteria(opts)
	allUIDs, err := c.searchUIDs(ctx, folder, criteria)
	if err != nil {
		return ListResult{}, err
	}
	result.TotalMatched = len(allUIDs)
	if len(allUIDs) == 0 {
		return result, nil
	}

	start := 0
	if len(allUIDs) > limit {
		start = len(allUIDs) - limit
	}
	envelopes, err := c.fetchEnvelopes(ctx, folder, allUIDs[start:])
	if err != nil {
		return ListResult{}, err
	}
	result.Envelopes = envelopes
	return result, nil
}

// searchCriteria translates SearchOptions into IMAP criteria. Every
// populated option is one more AND term.
func searchCriteria(opts SearchOptions) *imap.SearchCriteria {
	criteria := &imap.SearchCriteria{}
	if opts.Query != "" {
		criteria.Text = append(criteria.Text, opts.Query)
	}
	header := func(key, value string) {
		if value == "" {
			return
		}
		criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: key, Value: value})
	}
	header("From", opts.From)
	header("To", opts.To)
	header("Subject", opts.Subject)
	header("Message-ID", bracketed(opts.MessageID))
	header("In-Reply-To", bracketed(opts.InReplyTo))
	if !opts.Since.IsZero() {
		criteria.Since = opts.Since
	}
	if !opts.Before.IsZero() {
		criteria.Before = opts.Before
	}
	if opts.Unseen {
		criteria.NotFlag = append(criteria.NotFlag, imap.FlagSeen)
	}
	if opts.Flagged {
		criteria.Flag = append(criteria.Flag, imap.FlagFlagged)
	}
	return criteria
}

// bracketed wraps a Message-ID in angle brackets for a header search
// unless it already has them or is empty. Servers match header
// substrings, so the brackets keep "abc@x" from matching "xabc@x".
func bracketed(id string) string {
	if id == "" {
		return ""
	}
	if id[0] == '<' {
		return id
	}
	return "<" + id + ">"
}
